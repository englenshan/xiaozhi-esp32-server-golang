package mcp

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/spf13/viper"

	log "xiaozhi-esp32-server-golang/logger"
)

// MCPServerConfig MCP服务器配置
type MCPServerConfig struct {
	Name    string `json:"name" mapstructure:"name"`
	Type    string `json:"type" mapstructure:"type"`
	Url     string `json:"url" mapstructure:"url"`
	SSEUrl  string `json:"sse_url" mapstructure:"sse_url"` //向后兼容sse_url字段
	Enabled bool   `json:"enabled" mapstructure:"enabled"`
}

// GlobalMCPManager 全局MCP管理器
type GlobalMCPManager struct {
	servers       map[string]*MCPServerConnection
	tools         map[string]tool.InvokableTool
	mu            sync.RWMutex
	ctx           context.Context
	cancel        context.CancelFunc
	reconnectConf ReconnectConfig
	httpClient    *http.Client
}

// ReconnectConfig 重连配置
type ReconnectConfig struct {
	Interval    time.Duration
	MaxAttempts int
}

// MCPServerConnection MCP服务器连接
type MCPServerConnection struct {
	config     MCPServerConfig
	client     *client.Client
	tools      map[string]tool.InvokableTool
	connected  bool
	mu         sync.RWMutex
	lastError  error
	retryCount int
	lastPing   time.Time
}

var (
	globalManager *GlobalMCPManager
	once          sync.Once
)

// GetGlobalMCPManager 获取全局MCP管理器单例
func GetGlobalMCPManager() *GlobalMCPManager {
	once.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		globalManager = &GlobalMCPManager{
			servers: make(map[string]*MCPServerConnection),
			tools:   make(map[string]tool.InvokableTool),
			ctx:     ctx,
			cancel:  cancel,
			reconnectConf: ReconnectConfig{
				Interval:    time.Duration(viper.GetInt("mcp.global.reconnect_interval")) * time.Second,
				MaxAttempts: viper.GetInt("mcp.global.max_reconnect_attempts"),
			},
			httpClient: &http.Client{
				Timeout: 600 * time.Second,
			},
		}
	})
	return globalManager
}

// Start 启动全局MCP管理器
func (g *GlobalMCPManager) Start() error {
	// 首先检查配置
	CheckMCPConfig()

	if !viper.GetBool("mcp.global.enabled") {
		log.Info("全局MCP管理器已禁用")
		return nil
	}

	var serverConfigs []MCPServerConfig
	if err := viper.UnmarshalKey("mcp.global.servers", &serverConfigs); err != nil {
		log.Errorf("解析MCP服务器配置失败: %v", err)
		return fmt.Errorf("解析MCP服务器配置失败: %v", err)
	}

	log.Infof("从配置中读取到 %d 个MCP服务器配置", len(serverConfigs))

	// 详细记录每个服务器配置
	for i, config := range serverConfigs {
		log.Infof("MCP服务器[%d]: Type=%s, Name=%s, Url=%s, SSEUrl=%s, Enabled=%v",
			i+1, config.Type, config.Name, config.Url, config.SSEUrl, config.Enabled)
	}

	// 连接启用的服务器
	connectedCount := 0
	for _, config := range serverConfigs {
		if config.Enabled {
			if err := g.connectToServer(config); err != nil {
				log.Errorf("连接到MCP服务器 %s 失败: %v", config.Name, err)
			} else {
				connectedCount++
			}
		} else {
			log.Infof("MCP服务器 %s 已禁用，跳过连接", config.Name)
		}
	}

	log.Infof("成功连接了 %d 个MCP服务器", connectedCount)

	// 启动监控goroutine
	go g.monitorConnections()

	log.Info("全局MCP管理器已启动")
	return nil
}

// Stop 停止全局MCP管理器
func (g *GlobalMCPManager) Stop() error {
	g.cancel()

	g.mu.Lock()
	defer g.mu.Unlock()

	for name, conn := range g.servers {
		if err := conn.disconnect(); err != nil {
			log.Errorf("断开MCP服务器 %s 连接失败: %v", name, err)
		}
	}

	g.servers = make(map[string]*MCPServerConnection)
	g.tools = make(map[string]tool.InvokableTool)

	log.Info("全局MCP管理器已停止")
	return nil
}

// createFailedConnection 创建失败的连接对象用于后续重连
func (g *GlobalMCPManager) createFailedConnection(config MCPServerConfig) {
	conn := &MCPServerConnection{
		config:     config,
		tools:      make(map[string]tool.InvokableTool),
		connected:  false,
		lastError:  fmt.Errorf("初始化连接失败"),
		retryCount: 0,
	}

	g.mu.Lock()
	g.servers[config.Name] = conn
	g.mu.Unlock()

	log.Infof("已为失败的MCP服务器创建连接对象: %s", config.Name)
}

// connectToServer 连接到MCP服务器
func (g *GlobalMCPManager) connectToServer(config MCPServerConfig) error {
	// 验证配置
	if config.Name == "" {
		return fmt.Errorf("MCP服务器名称不能为空")
	}

	if !config.Enabled {
		log.Infof("MCP服务器 %s 已禁用，跳过连接", config.Name)
		return nil
	}

	log.Infof("正在连接MCP服务器: %s (URL: %s)", config.Name, config.SSEUrl)

	conn := &MCPServerConnection{
		config: config,
		tools:  make(map[string]tool.InvokableTool),
	}

	g.mu.Lock()
	g.servers[config.Name] = conn
	g.mu.Unlock()

	// 连接到服务器
	if err := conn.connect(); err != nil {
		return fmt.Errorf("连接MCP服务器失败: %v", err)
	}

	log.Infof("已连接到MCP服务器: %s", config.Name)
	return nil
}

// connect 连接到MCP服务器
func (conn *MCPServerConnection) connect() error {
	// 使用背景上下文，不设置超时，让SSE连接长期保持
	ctx := context.Background()

	config := conn.config
	var transportInstance transport.Interface
	var err error

	if config.SSEUrl != "" || config.Type == "sse" {
		var endpoint string
		if config.SSEUrl != "" {
			endpoint = config.SSEUrl
		}
		if config.Type == "sse" && config.Url != "" {
			endpoint = config.Url
		}
		sseTransport, err := transport.NewSSE(endpoint)
		if err != nil {
			return fmt.Errorf("创建SSE传输层失败: %v", err)
		}
		transportInstance = sseTransport
	} else if config.Type == "streamablehttp" {
		transportInstance, err = transport.NewStreamableHTTP(config.Url)
		if err != nil {
			return fmt.Errorf("创建StreamableHTTP传输层失败: %v", err)
		}
	}

	// 使用 client.NewClient 创建 MCP 客户端
	mcpClient := client.NewClient(transportInstance)

	conn.client = mcpClient

	log.Infof("开始连接MCP服务器: %s, %s URL: %s", conn.config.Name, conn.config.Type, conn.config.SSEUrl)

	// 启动客户端
	if err := conn.client.Start(ctx); err != nil {
		log.Errorf("启动MCP客户端失败，服务器: %s, 错误: %v", conn.config.Name, err)
		return fmt.Errorf("启动客户端失败: %v", err)
	}

	log.Infof("MCP客户端启动成功: %s", conn.config.Name)

	// 初始化客户端
	initRequest := mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo: mcp.Implementation{
				Name:    "xiaozhi-esp32-server",
				Version: "1.0.0",
			},
			Capabilities: mcp.ClientCapabilities{
				Experimental: make(map[string]any),
			},
		},
	}

	log.Infof("正在初始化MCP服务器: %s", conn.config.Name)
	initResult, err := conn.client.Initialize(ctx, initRequest)
	if err != nil {
		log.Errorf("初始化MCP服务器失败，服务器: %s, 错误: %v", conn.config.Name, err)
		return fmt.Errorf("初始化失败: %v", err)
	}

	log.Infof("MCP服务器初始化成功: %s, 结果: %+v", conn.config.Name, initResult)

	// 获取工具列表
	if err := conn.refreshTools(ctx); err != nil {
		log.Errorf("获取工具列表失败: %v", err)
		// 不直接返回错误，因为工具列表获取失败不应该阻止连接建立
	}

	conn.mu.Lock()
	conn.connected = true
	conn.lastError = nil
	conn.retryCount = 0
	conn.mu.Unlock()

	log.Infof("MCP服务器连接建立完成: %s", conn.config.Name)
	return nil
}

// refreshTools 刷新工具列表
func (conn *MCPServerConnection) refreshTools(ctx context.Context) error {
	// 获取工具列表
	listRequest := mcp.ListToolsRequest{}
	toolsResult, err := conn.client.ListTools(ctx, listRequest)
	if err != nil {
		return fmt.Errorf("获取工具列表失败: %v", err)
	}

	conn.mu.Lock()
	defer conn.mu.Unlock()

	conn.tools = ConvertMcpToolListToInvokableToolList(toolsResult.Tools, conn.config.Name, conn.client)

	// 更新全局工具列表
	globalManager.updateGlobalTools(conn.config.Name, conn.tools)

	log.Infof("MCP服务器 %s 工具列表已更新，共 %d 个工具", conn.config.Name, len(conn.tools))
	return nil
}

func ConvertMcpToolListToInvokableToolList(tools []mcp.Tool, serverName string, client *client.Client) map[string]tool.InvokableTool {
	invokeTools := make(map[string]tool.InvokableTool)
	for _, tool := range tools {

		marshaledInputSchema, err := sonic.Marshal(tool.InputSchema)
		if err != nil {
			log.Errorf("convert mcp tool to invokeable tool err: %+v", err)
			continue
		}
		inputSchema := &openapi3.Schema{}
		err = sonic.Unmarshal(marshaledInputSchema, inputSchema)
		if err != nil {
			log.Errorf("convert mcp tool to invokeable tool err: %+v", err)
			continue
		}

		mcpToolInstance := &McpTool{
			info: &schema.ToolInfo{
				Name:        tool.Name,
				Desc:        tool.Description,
				ParamsOneOf: schema.NewParamsOneOfByOpenAPIV3(inputSchema),
			},
			serverName: serverName,
			client:     client,
		}
		invokeTools[tool.Name] = mcpToolInstance
	}
	return invokeTools
}

// disconnect 断开连接
func (conn *MCPServerConnection) disconnect() error {
	conn.mu.Lock()
	defer conn.mu.Unlock()

	if conn.client != nil {
		// 关闭客户端
		if err := conn.client.Close(); err != nil {
			log.Errorf("关闭MCP客户端失败: %v", err)
		}
		conn.client = nil
	}

	conn.connected = false
	conn.tools = make(map[string]tool.InvokableTool)

	return nil
}

// updateGlobalTools 更新全局工具列表
func (g *GlobalMCPManager) updateGlobalTools(serverName string, tools map[string]tool.InvokableTool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// 移除该服务器的旧工具
	for name, mcpToolInterface := range g.tools {
		if mt, ok := mcpToolInterface.(*McpTool); ok && mt.serverName == serverName {
			delete(g.tools, name)
		}
	}

	// 添加新工具
	for name, mcpToolInterface := range tools {
		g.tools[fmt.Sprintf("%s_%s", serverName, name)] = mcpToolInterface
	}
}

// GetAllTools 获取所有可用工具
func (g *GlobalMCPManager) GetAllTools() map[string]tool.InvokableTool {
	g.mu.RLock()
	defer g.mu.RUnlock()

	result := make(map[string]tool.InvokableTool)
	for name, mcpToolInterface := range g.tools {
		result[name] = mcpToolInterface
	}
	return result
}

// GetToolByName 根据名称获取工具
func (g *GlobalMCPManager) GetToolByName(name string) (tool.InvokableTool, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	//所有的server
	for _, conn := range g.servers {
		name = fmt.Sprintf("%s_%s", conn.config.Name, name)
		mcpToolInterface, exists := g.tools[name]
		if exists {
			return mcpToolInterface, true
		}
	}
	return nil, false
}

// isSessionClosedError 判断是否为session closed错误
func isSessionClosedError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "session closed")
}

// monitorConnections 监控连接状态
func (g *GlobalMCPManager) monitorConnections() {
	pingTicker := time.NewTicker(20 * time.Second) // 每60秒ping一次
	defer pingTicker.Stop()

	for {
		select {
		case <-g.ctx.Done():
			return
		case <-pingTicker.C:
			// 执行ping检测
			g.mu.RLock()
			for name, conn := range g.servers {
				go func(name string, conn *MCPServerConnection) {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()

					if err := conn.ping(ctx); err != nil {
						log.Warnf("MCP服务器 %s ping失败，开始重连: %v", name, err)
						// ping失败时直接标记为断开并触发重连
						conn.mu.Lock()
						conn.connected = false
						conn.lastError = err
						conn.mu.Unlock()

						// 直接触发重连
						go g.reconnectServer(name)
					} else {
						//log.Debugf("MCP服务器 %s ping成功", name)
					}
				}(name, conn)
			}
			g.mu.RUnlock()
		}
	}
}

// reconnectServer 重连服务器并返回新的client
func (g *GlobalMCPManager) reconnectServer(serverName string) (*client.Client, error) {
	g.mu.RLock()
	var conn *MCPServerConnection
	for _, c := range g.servers {
		if c.config.Name == serverName {
			conn = c
			break
		}
	}
	g.mu.RUnlock()

	if conn == nil {
		return nil, fmt.Errorf("未找到服务器连接: %s", serverName)
	}

	// 断开连接
	if err := conn.disconnect(); err != nil {
		log.Errorf("断开连接失败: %v", err)
	}

	// 等待一小段时间确保资源释放
	time.Sleep(time.Second)

	// 重新连接
	if err := conn.connect(); err != nil {
		return nil, fmt.Errorf("重连失败: %v", err)
	}

	return conn.client, nil
}

// ping 发送ping请求检测连接状态
func (conn *MCPServerConnection) ping(ctx context.Context) error {
	if conn.client == nil {
		return fmt.Errorf("client未初始化")
	}

	// 使用空的Ping请求作为ping
	err := conn.client.Ping(ctx)
	if err != nil {
		return fmt.Errorf("ping失败: %v", err)
	}

	conn.mu.Lock()
	conn.lastPing = time.Now()
	conn.mu.Unlock()

	return nil
}
