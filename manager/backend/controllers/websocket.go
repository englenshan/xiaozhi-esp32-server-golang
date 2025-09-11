package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	cmap "github.com/orcaman/concurrent-map/v2"
	"gorm.io/gorm"

	"xiaozhi/manager/backend/models"
)

type WebSocketController struct {
	DB         *gorm.DB
	upgrader   websocket.Upgrader
	clientsMap cmap.ConcurrentMap[string, *WebSocketClient]
}

// WebSocketClient 连接到Manager Backend的客户端
type WebSocketClient struct {
	ID           string
	conn         *websocket.Conn
	controller   *WebSocketController
	requestChans map[string]chan *WebSocketResponse
	callbacks    map[string]func(*WebSocketResponse)
	mu           sync.RWMutex
	isConnected  bool
	stopChan     chan struct{} // 停止信号通道
}

type WebSocketRequest struct {
	ID      string                 `json:"id"`
	Method  string                 `json:"method"`
	Path    string                 `json:"path"`
	Headers map[string]string      `json:"headers,omitempty"`
	Body    map[string]interface{} `json:"body,omitempty"`
}

type WebSocketResponse struct {
	ID      string                 `json:"id"`
	Status  int                    `json:"status"`
	Headers map[string]string      `json:"headers,omitempty"`
	Body    map[string]interface{} `json:"body,omitempty"`
	Error   string                 `json:"error,omitempty"`
}

// NewWebSocketController 创建WebSocket控制器
func NewWebSocketController(db *gorm.DB) *WebSocketController {
	return &WebSocketController{
		DB: db,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true // 允许所有来源，生产环境应该限制
			},
		},
		clientsMap: cmap.New[*WebSocketClient](),
	}
}

// HandleWebSocket 处理WebSocket连接升级
func (ctrl *WebSocketController) HandleWebSocket(c *gin.Context) {
	// 获取UUID header
	clientUUID := c.GetHeader("UUID")
	if clientUUID == "" {
		log.Printf("WebSocket连接缺少UUID header")
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少UUID header"})
		return
	}

	// 升级HTTP连接为WebSocket连接
	conn, err := ctrl.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("WebSocket升级失败: %v", err)
		return
	}

	// 检查是否已存在相同UUID的连接
	if existingClient, exists := ctrl.clientsMap.Get(clientUUID); exists {
		log.Printf("断开现有连接: %s", clientUUID)
		existingClient.conn.Close()
		existingClient.isConnected = false
	}

	// 创建新的客户端
	client := &WebSocketClient{
		ID:           clientUUID,
		conn:         conn,
		controller:   ctrl,
		requestChans: make(map[string]chan *WebSocketResponse),
		callbacks:    make(map[string]func(*WebSocketResponse)),
		isConnected:  true,
		stopChan:     make(chan struct{}),
	}

	// 存储到clientsMap中
	ctrl.clientsMap.Set(clientUUID, client)

	log.Printf("新的WebSocket客户端已连接: %s", clientUUID)

	// 启动客户端消息处理
	go client.handleMessages()

	// 启动心跳检测
	go client.heartbeat()
}

// 移除客户端
func (ctrl *WebSocketController) removeClient(clientID string) {
	if client, exists := ctrl.clientsMap.Get(clientID); exists {
		// 发送停止信号给心跳检测
		select {
		case client.stopChan <- struct{}{}:
			log.Printf("已发送停止信号给客户端: %s", clientID)
		default:
			// 通道可能已满或已关闭，忽略
		}

		// 确保客户端状态正确设置
		client.isConnected = false
		// 从映射中移除
		ctrl.clientsMap.Remove(clientID)
		log.Printf("WebSocket客户端已断开: %s", clientID)
	}
}

// 获取客户端通过UUID
func (ctrl *WebSocketController) GetClient(uuid string) *WebSocketClient {
	if client, exists := ctrl.clientsMap.Get(uuid); exists {
		return client
	}
	return nil
}

// 检查指定UUID的客户端是否连接
func (ctrl *WebSocketController) IsClientConnected(uuid string) bool {
	if client, exists := ctrl.clientsMap.Get(uuid); exists {
		return client.isConnected
	}
	return false
}

// 向指定UUID的客户端发送消息
func (ctrl *WebSocketController) SendToClient(uuid string, message interface{}) error {
	if client, exists := ctrl.clientsMap.Get(uuid); exists && client.isConnected {
		return client.conn.WriteJSON(message)
	}
	return fmt.Errorf("客户端 %s 未连接", uuid)
}

// 广播消息给所有连接的客户端
func (ctrl *WebSocketController) Broadcast(message interface{}) {
	for item := range ctrl.clientsMap.IterBuffered() {
		if client := item.Val; client.isConnected {
			if err := client.conn.WriteJSON(message); err != nil {
				log.Printf("向客户端 %s 广播消息失败: %v", client.ID, err)
			}
		}
	}
}

// 客户端消息处理
func (client *WebSocketClient) handleMessages() {
	defer func() {
		client.conn.Close()
		client.isConnected = false
		client.controller.removeClient(client.ID)
	}()

	for {
		if !client.isConnected {
			return
		}

		// 读取消息类型
		messageType, reader, err := client.conn.NextReader()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket读取错误: %v", err)
			}
			return
		}

		// 处理不同类型的消息
		switch messageType {
		case websocket.TextMessage:
			// 处理JSON消息
			var rawMessage map[string]interface{}
			if err := json.NewDecoder(reader).Decode(&rawMessage); err != nil {
				log.Printf("解析JSON消息失败: %v", err)
				continue
			}
			// 处理消息
			client.handleMessage(rawMessage)

		case websocket.PingMessage:
			// 处理ping消息，自动回复pong
			log.Printf("收到ping消息，自动回复pong")
			if err := client.conn.WriteControl(websocket.PongMessage, []byte{}, time.Now().Add(10*time.Second)); err != nil {
				log.Printf("发送pong失败: %v", err)
			}

		case websocket.PongMessage:
			// 处理pong消息
			log.Printf("收到pong消息")

		case websocket.CloseMessage:
			// 处理关闭消息
			log.Printf("收到关闭消息")
			return

		default:
			log.Printf("收到未知类型的WebSocket消息: %d", messageType)
		}
	}
}

// 处理收到的消息
func (client *WebSocketClient) handleMessage(rawMessage map[string]interface{}) {
	// 检查是否是请求消息
	if method, exists := rawMessage["method"]; exists && method != nil {
		client.handleRequest(rawMessage)
		return
	}

	// 检查是否是响应消息
	if status, exists := rawMessage["status"]; exists && status != nil {
		client.handleResponse(rawMessage)
		return
	}

	log.Printf("收到无法识别的消息: %+v", rawMessage)
}

// 处理请求消息
func (client *WebSocketClient) handleRequest(rawMessage map[string]interface{}) {
	var request WebSocketRequest
	if err := mapToStruct(rawMessage, &request); err != nil {
		log.Printf("解析请求失败: %v", err)
		return
	}

	log.Printf("收到请求: ID=%s, Method=%s, Path=%s", request.ID, request.Method, request.Path)

	// 处理请求并发送响应
	client.processRequest(&request)
}

// 处理响应消息
func (client *WebSocketClient) handleResponse(rawMessage map[string]interface{}) {
	var response WebSocketResponse
	if err := mapToStruct(rawMessage, &response); err != nil {
		log.Printf("解析响应失败: %v", err)
		return
	}

	log.Printf("收到响应: ID=%s, Status=%d", response.ID, response.Status)

	// 查找对应的响应通道
	client.mu.RLock()
	responseChan, exists := client.requestChans[response.ID]
	callback, callbackExists := client.callbacks[response.ID]
	client.mu.RUnlock()

	if exists {
		select {
		case responseChan <- &response:
		default:
			log.Printf("响应通道已满，丢弃响应: %s", response.ID)
		}
	}

	if callbackExists {
		go callback(&response)
	}

	if !exists && !callbackExists {
		log.Printf("收到未知的响应ID: %s", response.ID)
	}
}

// 处理请求
func (client *WebSocketClient) processRequest(request *WebSocketRequest) {
	switch request.Path {
	case "/api/server/info":
		client.handleServerInfoRequest(request)

	case "/api/server/ping":
		client.handlePingRequest(request)

	case "/api/device/active":
		client.handleDeviceActiveRequest(request)

	case "/api/device/inactive":
		client.handleDeviceInactiveRequest(request)

	default:
		log.Printf("未知的请求路径: %s", request.Path)
		client.sendResponse(request.ID, 404, nil, "Unknown endpoint")
	}
}

// 处理服务器信息请求
func (client *WebSocketClient) handleServerInfoRequest(request *WebSocketRequest) {
	response := map[string]interface{}{
		"server_name": "xiaozhi-manager-backend",
		"version":     "1.0.0",
		"uptime":      time.Now().Format(time.RFC3339),
		"request_id":  request.ID,
		"client_id":   client.ID,
	}

	client.sendResponse(request.ID, 200, response, "")
}

// 处理ping请求
func (client *WebSocketClient) handlePingRequest(request *WebSocketRequest) {
	response := map[string]interface{}{
		"message":   "pong from manager backend",
		"time":      time.Now().Format(time.RFC3339),
		"client_id": client.ID,
	}

	client.sendResponse(request.ID, 200, response, "")
}

// 处理设备活跃时间更新请求
func (client *WebSocketClient) handleDeviceActiveRequest(request *WebSocketRequest) {
	// 从请求体中获取device_id
	deviceID := ""
	if request.Body != nil {
		if id, ok := request.Body["device_id"].(string); ok {
			deviceID = id
		}
	}

	if deviceID == "" {
		log.Printf("收到设备活跃请求，但缺少device_id")
		client.sendResponse(request.ID, 400, nil, "缺少device_id参数")
		return
	}

	log.Printf("处理设备活跃时间更新请求，device_id: %s", deviceID)

	// 更新设备最后活跃时间
	now := time.Now()
	result := client.controller.DB.Model(&models.Device{}).
		Where("device_name = ?", deviceID).
		Update("last_active_at", now)

	if result.Error != nil {
		log.Printf("更新设备活跃时间失败: %v", result.Error)
		client.sendResponse(request.ID, 500, nil, fmt.Sprintf("更新设备活跃时间失败: %v", result.Error))
		return
	}

	if result.RowsAffected == 0 {
		log.Printf("设备不存在: %s", deviceID)
		client.sendResponse(request.ID, 404, nil, "设备不存在")
		return
	}

	// 构造成功响应
	response := map[string]interface{}{
		"device_id":      deviceID,
		"last_active_at": now.Format(time.RFC3339),
		"message":        "设备活跃时间更新成功",
	}

	client.sendResponse(request.ID, 200, response, "")
	log.Printf("设备 %s 活跃时间已更新为: %s", deviceID, now.Format(time.RFC3339))
}

// 处理设备离线请求
func (client *WebSocketClient) handleDeviceInactiveRequest(request *WebSocketRequest) {
	// 从请求体中获取device_id
	deviceID := ""
	if request.Body != nil {
		if id, ok := request.Body["device_id"].(string); ok {
			deviceID = id
		}
	}

	if deviceID == "" {
		log.Printf("收到设备离线请求，但缺少device_id")
		client.sendResponse(request.ID, 400, nil, "缺少device_id参数")
		return
	}

	log.Printf("处理设备离线请求，device_id: %s", deviceID)

	// 将设备最后活跃时间设置为0（离线状态）
	result := client.controller.DB.Model(&models.Device{}).
		Where("device_name = ?", deviceID).
		Update("last_active_at", nil) // 设置为NULL表示离线

	if result.Error != nil {
		log.Printf("更新设备离线状态失败: %v", result.Error)
		client.sendResponse(request.ID, 500, nil, fmt.Sprintf("更新设备离线状态失败: %v", result.Error))
		return
	}

	if result.RowsAffected == 0 {
		log.Printf("设备不存在: %s", deviceID)
		client.sendResponse(request.ID, 404, nil, "设备不存在")
		return
	}

	// 构造成功响应
	response := map[string]interface{}{
		"device_id":      deviceID,
		"last_active_at": nil, // 离线状态
		"message":        "设备离线状态更新成功",
	}

	client.sendResponse(request.ID, 200, response, "")
	log.Printf("设备 %s 已设置为离线状态", deviceID)
}

// 发送响应
func (client *WebSocketClient) sendResponse(requestID string, status int, body map[string]interface{}, errorMsg string) {
	response := WebSocketResponse{
		ID:     requestID,
		Status: status,
		Body:   body,
		Error:  errorMsg,
	}

	if err := client.conn.WriteJSON(response); err != nil {
		log.Printf("发送响应失败: %v", err)
	} else {
		log.Printf("已发送响应: ID=%s, Status=%d", requestID, status)
	}
}

// 心跳检测 - 使用WebSocket原生ping/pong
func (client *WebSocketClient) heartbeat() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// 连续ping失败计数
	pingFailCount := 0
	maxPingFailCount := 3 // 允许连续失败3次

	for {
		select {
		case <-client.stopChan:
			log.Printf("收到停止信号，停止心跳检测")
			return
		case <-ticker.C:
			if !client.isConnected {
				return
			}

			// 检查连接是否仍然有效
			if client.conn == nil {
				log.Printf("WebSocket连接已为空，停止心跳检测")
				return
			}

			// 发送WebSocket原生ping
			if err := client.conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(10*time.Second)); err != nil {
				pingFailCount++
				log.Printf("发送ping失败 (第%d次): %v", pingFailCount, err)

				// 只有连续失败超过阈值才断开连接
				if pingFailCount >= maxPingFailCount {
					log.Printf("连续ping失败%d次，断开WebSocket连接", maxPingFailCount)
					client.conn.Close()
					return
				}
			} else {
				// ping成功，重置失败计数
				if pingFailCount > 0 {
					log.Printf("ping恢复成功，重置失败计数")
					pingFailCount = 0
				}
			}
		}
	}
}

// 发送请求到客户端（用于主动推送）
func (client *WebSocketClient) SendRequest(method, path string, body map[string]interface{}) error {
	request := WebSocketRequest{
		ID:     uuid.New().String(),
		Method: method,
		Path:   path,
		Body:   body,
	}

	return client.conn.WriteJSON(request)
}

// 发送请求并等待响应
func (client *WebSocketClient) SendRequestWithResponse(ctx context.Context, method, path string, body map[string]interface{}) (*WebSocketResponse, error) {
	requestID := uuid.New().String()

	request := WebSocketRequest{
		ID:     requestID,
		Method: method,
		Path:   path,
		Body:   body,
	}

	// 创建响应通道
	responseChan := make(chan *WebSocketResponse, 1)
	client.mu.Lock()
	client.requestChans[requestID] = responseChan
	client.mu.Unlock()

	// 清理响应通道
	defer func() {
		client.mu.Lock()
		delete(client.requestChans, requestID)
		client.mu.Unlock()
		close(responseChan)
	}()

	// 发送请求
	if err := client.conn.WriteJSON(request); err != nil {
		return nil, fmt.Errorf("发送请求失败: %v", err)
	}

	// 等待响应
	select {
	case response := <-responseChan:
		return response, nil
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("请求超时")
	case <-ctx.Done():
		return nil, fmt.Errorf("上下文取消")
	}
}

// mapToStruct 辅助函数：将map转换为struct
func mapToStruct(data map[string]interface{}, target interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(jsonData, target)
}

// 向指定UUID的客户端发送请求并等待响应
func (ctrl *WebSocketController) SendRequestToClient(ctx context.Context, uuid string, method, path string, body map[string]interface{}) (*WebSocketResponse, error) {
	if client, exists := ctrl.clientsMap.Get(uuid); exists && client.isConnected {
		return client.SendRequestWithResponse(ctx, method, path, body)
	}
	return nil, fmt.Errorf("客户端 %s 未连接", uuid)
}

// 请求客户端MCP工具列表（广播方式，等待第一个非空列表响应）
func (ctrl *WebSocketController) RequestMcpToolsFromClient(ctx context.Context, agentID string) ([]string, error) {
	log.Printf("开始请求客户端MCP工具列表，agentID: %s", agentID)

	body := map[string]interface{}{
		"agent_id": agentID,
	}

	// 创建响应通道用于接收响应
	responseChan := make(chan *WebSocketResponse, 10)
	
	// 创建唯一的请求ID
	requestID := uuid.New().String()
	
	// 响应处理器
	responseHandler := func(response *WebSocketResponse) {
		select {
		case responseChan <- response:
		default:
			log.Printf("响应通道已满，丢弃响应: %s", response.ID)
		}
	}

	// 为每个客户端注册回调
	var callbackMutex sync.Mutex
	callbacksRegistered := 0
	
	for item := range ctrl.clientsMap.IterBuffered() {
		client := item.Val
		if client.isConnected {
			callbackMutex.Lock()
			client.mu.Lock()
			client.callbacks[requestID] = responseHandler
			client.mu.Unlock()
			callbacksRegistered++
			callbackMutex.Unlock()
			
			// 发送请求到客户端
			request := WebSocketRequest{
				ID:     requestID,
				Method: "GET",
				Path:   "/api/mcp/tools",
				Body:   body,
			}
			
			if err := client.conn.WriteJSON(request); err != nil {
				log.Printf("向客户端 %s 发送MCP工具列表请求失败: %v", client.ID, err)
			} else {
				log.Printf("向客户端 %s 发送MCP工具列表请求成功", client.ID)
			}
		}
	}

	if callbacksRegistered == 0 {
		return nil, fmt.Errorf("没有连接的客户端")
	}

	// 清理回调
	defer func() {
		for item := range ctrl.clientsMap.IterBuffered() {
			client := item.Val
			client.mu.Lock()
			delete(client.callbacks, requestID)
			client.mu.Unlock()
		}
		close(responseChan)
	}()

	// 等待所有客户端响应或超时，寻找第一个非空工具列表
	timeout := time.After(30 * time.Second)
	responsesReceived := 0
	
	for {
		select {
		case response := <-responseChan:
			responsesReceived++
			log.Printf("收到第%d个客户端响应: status=%d", responsesReceived, response.Status)

			// 检查响应状态
			if response.Status != http.StatusOK {
				log.Printf("客户端返回错误状态: %d", response.Status)
				continue // 继续等待其他客户端响应
			}

			// 解析响应体中的工具列表
			if response.Body == nil {
				log.Printf("客户端响应体为空")
				continue // 继续等待其他客户端响应
			}

			// 尝试从响应体中提取工具列表
			toolsData, ok := response.Body["tools"]
			if !ok {
				log.Printf("客户端响应体中未找到tools字段")
				continue // 继续等待其他客户端响应
			}

			log.Printf("找到tools数据: %+v (类型: %T)", toolsData, toolsData)

			// 将工具数据转换为字符串切片
			var tools []string
			switch v := toolsData.(type) {
			case []interface{}:
				for _, tool := range v {
					if toolStr, ok := tool.(string); ok {
						tools = append(tools, toolStr)
					} else if toolMap, ok := tool.(map[string]interface{}); ok {
						// 如果工具是对象格式，提取name字段
						if name, ok := toolMap["name"].(string); ok {
							tools = append(tools, name)
						}
					}
				}
			case []string:
				tools = v
			default:
				log.Printf("无法解析工具列表格式: %T", toolsData)
				continue // 继续等待其他客户端响应
			}

			// 检查是否为非空列表
			if len(tools) > 0 {
				log.Printf("成功解析非空工具列表: %v", tools)
				return tools, nil
			} else {
				log.Printf("工具列表为空，继续等待其他客户端响应")
			}

			// 如果已经收到所有客户端的响应且都为空，返回空列表
			if responsesReceived >= callbacksRegistered {
				log.Printf("已收到所有客户端响应，工具列表均为空")
				return []string{}, nil
			}

		case <-timeout:
			log.Printf("请求超时，已收到%d个响应", responsesReceived)
			return nil, fmt.Errorf("请求超时")
			
		case <-ctx.Done():
			log.Printf("上下文取消，已收到%d个响应", responsesReceived)
			return nil, fmt.Errorf("上下文取消")
		}
	}
}

// 请求客户端服务器信息
func (ctrl *WebSocketController) RequestServerInfoFromClient(ctx context.Context, uuid string) (*WebSocketResponse, error) {
	return ctrl.SendRequestToClient(ctx, uuid, "GET", "/api/server/info", nil)
}

func (ctrl *WebSocketController) RequestDeviceActivation(ctx context.Context, uuid, deviceID string) (*WebSocketResponse, error) {
	return ctrl.SendRequestToClient(ctx, uuid, "GET", "/api/device/activation", map[string]interface{}{
		"device_id": deviceID,
	})
}

// 请求客户端ping
func (ctrl *WebSocketController) RequestPingFromClient(ctx context.Context, uuid string) (*WebSocketResponse, error) {
	return ctrl.SendRequestToClient(ctx, uuid, "GET", "/api/server/ping", nil)
}

// InjectMessageToDevice 向设备注入消息（广播方式）
func (ctrl *WebSocketController) InjectMessageToDevice(ctx context.Context, deviceID, message string, skipLlm bool) error {
	body := map[string]interface{}{
		"device_id": deviceID,
		"message":   message,
		"skip_llm":  skipLlm,
	}

	// 创建请求
	request := WebSocketRequest{
		ID:     uuid.New().String(),
		Method: "POST",
		Path:   "/api/device/inject_msg",
		Body:   body,
	}

	// 广播给所有连接的客户端
	var lastError error
	clientCount := 0

	for item := range ctrl.clientsMap.IterBuffered() {
		client := item.Val
		if client.isConnected {
			clientCount++
			if err := client.conn.WriteJSON(request); err != nil {
				log.Printf("向客户端 %s 广播注入消息失败: %v", client.ID, err)
				lastError = err
			} else {
				log.Printf("向客户端 %s 广播注入消息成功", client.ID)
			}
		}
	}

	if clientCount == 0 {
		return fmt.Errorf("没有连接的客户端")
	}

	return lastError
}

// 异步发送请求到客户端（不等待响应）
func (ctrl *WebSocketController) SendRequestToClientAsync(uuid string, method, path string, body map[string]interface{}) error {
	if client, exists := ctrl.clientsMap.Get(uuid); exists && client.isConnected {
		return client.SendRequest(method, path, body)
	}
	return fmt.Errorf("客户端 %s 未连接", uuid)
}

// 获取所有客户端连接状态
func (ctrl *WebSocketController) GetClientConnectionStatus() map[string]interface{} {
	clients := make([]map[string]interface{}, 0)
	for item := range ctrl.clientsMap.IterBuffered() {
		client := item.Val
		clients = append(clients, map[string]interface{}{
			"uuid":      client.ID,
			"connected": client.isConnected,
		})
	}

	return map[string]interface{}{
		"clients": clients,
		"count":   len(clients),
	}
}

// 获取指定客户端连接状态
func (ctrl *WebSocketController) GetClientStatus(uuid string) map[string]interface{} {
	if client, exists := ctrl.clientsMap.Get(uuid); exists {
		return map[string]interface{}{
			"uuid":      client.ID,
			"connected": client.isConnected,
			"message":   "客户端已连接",
		}
	}

	return map[string]interface{}{
		"uuid":      uuid,
		"connected": false,
		"message":   "客户端未连接",
	}
}
