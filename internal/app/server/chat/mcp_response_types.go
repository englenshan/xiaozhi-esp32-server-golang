package chat

import (
	"encoding/json"
	"time"
)

// MCPResponseType 定义MCP响应的类型
type MCPResponseType string

const (
	// 动作类：需要执行特定动作，通常会终止后续处理
	MCPResponseTypeAction MCPResponseType = "action"
	// 音频资源类：需要执行特定动作，通常会终止后续处理, 也不需要返回stop
	MCPResponseTypeAudio MCPResponseType = "audio"

	// 内容类：返回信息内容，允许后续处理
	MCPResponseTypeContent MCPResponseType = "content"
	// 错误类：处理错误情况
	MCPResponseTypeError MCPResponseType = "error"
)

// MCPResponseBase 所有MCP响应的基础结构
type MCPResponseBase struct {
	Type      MCPResponseType `json:"type"`
	Success   bool            `json:"success"`
	Timestamp int64           `json:"timestamp"`
	ToolName  string          `json:"tool_name"`
}

// MCPActionResponse 动作类响应 - 用于播放音乐、退出对话等需要执行动作的场景
type MCPActionResponse struct {
	MCPResponseBase
	Action   string            `json:"action"`
	Message  string            `json:"message"`
	Status   string            `json:"status"`
	Metadata map[string]string `json:"metadata,omitempty"`
	// 控制标志
	FinalAction       bool   `json:"final_action"`
	NoFurtherResponse bool   `json:"no_further_response"`
	SilenceLLM        bool   `json:"silence_llm"`
	UserState         string `json:"user_state"`
	Instruction       string `json:"instruction,omitempty"`
}

// MCPActionResponse 动作类响应 - 用于播放音乐、退出对话等需要执行动作的场景
type MCPAudioResponse struct {
	MCPResponseBase
	Action   string            `json:"action"`
	Message  string            `json:"message"`
	Status   string            `json:"status"`
	Metadata map[string]string `json:"metadata,omitempty"`
	// 控制标志
	FinalAction       bool   `json:"final_action"`
	NoFurtherResponse bool   `json:"no_further_response"`
	SilenceLLM        bool   `json:"silence_llm"`
	UserState         string `json:"user_state"`
	Instruction       string `json:"instruction,omitempty"`
}

// MCPContentResponse 内容类响应 - 用于获取时间、查询信息等返回数据的场景
type MCPContentResponse struct {
	MCPResponseBase
	Data        interface{} `json:"data"`
	Message     string      `json:"message"`
	Format      string      `json:"format,omitempty"`       // 数据格式说明
	DisplayHint string      `json:"display_hint,omitempty"` // 显示提示
}

// MCPErrorResponse 错误类响应 - 统一的错误处理
type MCPErrorResponse struct {
	MCPResponseBase
	Error      string `json:"error"`
	ErrorCode  string `json:"error_code,omitempty"`
	Details    string `json:"details,omitempty"`
	Suggestion string `json:"suggestion,omitempty"` // 给用户的建议
}

// MCPResponse 统一的MCP响应接口
type MCPResponse interface {
	GetType() MCPResponseType
	GetSuccess() bool
	IsTerminal() bool // 是否是终止性操作
	ToJSON() (string, error)
	GetContent() string
}

// 实现MCPResponse接口
func (r *MCPActionResponse) GetType() MCPResponseType { return MCPResponseTypeAction }
func (r *MCPActionResponse) GetSuccess() bool         { return r.Success }
func (r *MCPActionResponse) IsTerminal() bool         { return r.FinalAction || r.NoFurtherResponse }
func (r *MCPActionResponse) GetContent() string       { return r.Message }

// 为MCPAudioResponse添加接口方法实现
func (r *MCPAudioResponse) GetType() MCPResponseType { return MCPResponseTypeAudio }
func (r *MCPAudioResponse) GetSuccess() bool         { return r.Success }
func (r *MCPAudioResponse) IsTerminal() bool         { return r.FinalAction || r.NoFurtherResponse }
func (r *MCPAudioResponse) GetContent() string       { return r.Message }

func (r *MCPContentResponse) GetType() MCPResponseType { return MCPResponseTypeContent }
func (r *MCPContentResponse) GetSuccess() bool         { return r.Success }
func (r *MCPContentResponse) IsTerminal() bool         { return false } // 内容类通常不终止
func (r *MCPContentResponse) GetContent() string       { return r.Message }

func (r *MCPErrorResponse) GetType() MCPResponseType { return MCPResponseTypeError }
func (r *MCPErrorResponse) GetSuccess() bool         { return r.Success }
func (r *MCPErrorResponse) IsTerminal() bool         { return false } // 错误类允许后续处理
func (r *MCPErrorResponse) GetContent() string       { return r.Error }

// ToJSON 方法实现
func (r *MCPActionResponse) ToJSON() (string, error) {
	data, err := json.Marshal(r)
	return string(data), err
}

// 为MCPAudioResponse添加ToJSON方法
func (r *MCPAudioResponse) ToJSON() (string, error) {
	data, err := json.Marshal(r)
	return string(data), err
}

func (r *MCPContentResponse) ToJSON() (string, error) {
	data, err := json.Marshal(r)
	return string(data), err
}

func (r *MCPErrorResponse) ToJSON() (string, error) {
	data, err := json.Marshal(r)
	return string(data), err
}

// 便利构造函数

// NewActionResponse 创建动作类响应
func NewActionResponse(toolName, action, message, status string, terminal bool) *MCPActionResponse {
	return &MCPActionResponse{
		MCPResponseBase: MCPResponseBase{
			Type:      MCPResponseTypeAction,
			Success:   true,
			Timestamp: time.Now().Unix(),
			ToolName:  toolName,
		},
		Action:            action,
		Message:           message,
		Status:            status,
		FinalAction:       terminal,
		NoFurtherResponse: terminal,
		SilenceLLM:        terminal,
	}
}

// NewAudioResponse 创建音频类响应 - 修正返回类型
func NewAudioResponse(toolName, action, message, status string, terminal bool) *MCPAudioResponse {
	return &MCPAudioResponse{
		MCPResponseBase: MCPResponseBase{
			Type:      MCPResponseTypeAudio,
			Success:   true,
			Timestamp: time.Now().Unix(),
			ToolName:  toolName,
		},
		Action:            action,
		Message:           message,
		Status:            status,
		FinalAction:       terminal,
		NoFurtherResponse: terminal,
		SilenceLLM:        terminal,
	}
}

// NewContentResponse 创建内容类响应
func NewContentResponse(toolName string, data interface{}, message string) *MCPContentResponse {
	return &MCPContentResponse{
		MCPResponseBase: MCPResponseBase{
			Type:      MCPResponseTypeContent,
			Success:   true,
			Timestamp: time.Now().Unix(),
			ToolName:  toolName,
		},
		Data:    data,
		Message: message,
	}
}

// NewErrorResponse 创建错误类响应
func NewErrorResponse(toolName, error, errorCode, suggestion string) *MCPErrorResponse {
	return &MCPErrorResponse{
		MCPResponseBase: MCPResponseBase{
			Type:      MCPResponseTypeError,
			Success:   false,
			Timestamp: time.Now().Unix(),
			ToolName:  toolName,
		},
		Error:      error,
		ErrorCode:  errorCode,
		Suggestion: suggestion,
	}
}

// ParseMCPResponse 从JSON字符串解析MCP响应
func ParseMCPResponse(jsonStr string) (MCPResponse, error) {
	var base MCPResponseBase
	if err := json.Unmarshal([]byte(jsonStr), &base); err != nil {
		return nil, err
	}

	switch base.Type {
	case MCPResponseTypeAction:
		var response MCPActionResponse
		if err := json.Unmarshal([]byte(jsonStr), &response); err != nil {
			return nil, err
		}
		return &response, nil
	case MCPResponseTypeAudio:
		var response MCPAudioResponse
		if err := json.Unmarshal([]byte(jsonStr), &response); err != nil {
			return nil, err
		}
		return &response, nil
	case MCPResponseTypeContent:
		var response MCPContentResponse
		if err := json.Unmarshal([]byte(jsonStr), &response); err != nil {
			return nil, err
		}
		return &response, nil
	case MCPResponseTypeError:
		var response MCPErrorResponse
		if err := json.Unmarshal([]byte(jsonStr), &response); err != nil {
			return nil, err
		}
		return &response, nil
	default:
		return NewErrorResponse("unknown", "未知的响应类型", "INVALID_TYPE", "请检查工具实现"), nil
	}
}
