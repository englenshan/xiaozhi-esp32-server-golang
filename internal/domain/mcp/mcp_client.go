package mcp

import (
	log "xiaozhi-esp32-server-golang/logger"

	"github.com/cloudwego/eino/components/tool"
	mcp_go "github.com/mark3labs/mcp-go/mcp"
)

func GetToolByName(deviceId string, toolName string) (tool.InvokableTool, bool) {
	// 优先从本地管理器获取
	localManager := GetLocalMCPManager()
	tool, ok := localManager.GetToolByName(toolName)
	if ok {
		return tool, ok
	}

	// 其次从全局管理器获取
	tool, ok = globalManager.GetToolByName(toolName)
	if ok {
		return tool, ok
	}

	// 最后从设备MCP客户端池获取
	tool, ok = mcpClientPool.GetToolByDeviceId(deviceId, toolName)
	if !ok {
		return nil, false
	}
	return tool, true
}

func GetDeviceMcpClient(deviceId string) *DeviceMcpSession {
	return mcpClientPool.GetMcpClient(deviceId)
}

func AddDeviceMcpClient(deviceId string, mcpClient *DeviceMcpSession) error {
	mcpClientPool.AddMcpClient(deviceId, mcpClient)
	return nil
}

func RemoveDeviceMcpClient(deviceId string) error {
	mcpClientPool.RemoveMcpClient(deviceId)
	return nil
}

func GetToolsByDeviceId(deviceId string, agentId string) (map[string]tool.InvokableTool, error) {
	retTools := make(map[string]tool.InvokableTool)

	// 优先从本地管理器获取
	localManager := GetLocalMCPManager()
	localTools := localManager.GetAllTools()
	for toolName, tool := range localTools {
		retTools[toolName] = tool
	}
	log.Infof("从本地管理器获取到 %d 个工具", len(localTools))

	// 其次从全局管理器获取
	globalTools := globalManager.GetAllTools()
	for toolName, tool := range globalTools {
		// 本地工具优先，如果已存在同名工具则不覆盖
		if _, exists := retTools[toolName]; !exists {
			retTools[toolName] = tool
		}
	}
	log.Infof("从全局管理器获取到 %d 个工具", len(globalTools))

	// 最后从MCP客户端池获取
	deviceTools, err := mcpClientPool.GetAllToolsByDeviceIdAndAgentId(deviceId, agentId)
	if err != nil {
		log.Errorf("获取设备 %s 的工具失败: %v", deviceId, err)
		return retTools, nil
	}
	for toolName, tool := range deviceTools {
		// 本地工具和全局工具优先，如果已存在同名工具则不覆盖
		if _, exists := retTools[toolName]; !exists {
			retTools[toolName] = tool
		}
	}
	log.Infof("从设备 %s 获取到 %d 个工具", deviceId, len(deviceTools))
	log.Infof("设备 %s 总共获取到 %d 个工具", deviceId, len(retTools))

	return retTools, nil
}

func GetAudioResourceByTool(tool McpTool, resourceLink mcp_go.ResourceLink) (mcp_go.ReadResourceResult, error) {
	/*client := tool.GetClient()
	resourceRequest := mcp_go.ReadResourceRequest{
		Request: mcp_go.Request{
			Params: mcp_go.ReadResourceParams{
				URI: resourceLink.URL,
			},
		},
	}
	client.ReadResource(context.Background(), resourceRequest)*/
	return mcp_go.ReadResourceResult{}, nil
}
