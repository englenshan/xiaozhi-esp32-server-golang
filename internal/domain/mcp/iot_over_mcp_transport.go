package mcp

import (
	"context"
	"encoding/json"

	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

/**
// Interface for the transport layer.
type Interface interface {
	// Start the connection. Start should only be called once.
	Start(ctx context.Context) error

	// SendRequest sends a json RPC request and returns the response synchronously.
	SendRequest(ctx context.Context, request JSONRPCRequest) (*JSONRPCResponse, error)

	// SendNotification sends a json RPC Notification to the server.
	SendNotification(ctx context.Context, notification mcp.JSONRPCNotification) error

	// SetNotificationHandler sets the handler for notifications.
	// Any notification before the handler is set will be discarded.
	SetNotificationHandler(handler func(notification mcp.JSONRPCNotification))

	// Close the connection.
	Close() error
}
*/

type ConnInterface interface {
	SendMcpMsg(payload []byte) error
	RecvMcpMsg(ctx context.Context, timeOut int) ([]byte, error)
}

type IotOverMcpTransport struct {
	conn ConnInterface

	notifyHandler func(notification mcp.JSONRPCNotification)
	// 添加关闭回调
	onCloseHandler func(reason string)
}

func (t *IotOverMcpTransport) Send(ctx context.Context, msg []byte) error {
	return t.conn.SendMcpMsg(msg)
}

func NewIotOverMcpTransport(conn ConnInterface) (*IotOverMcpTransport, error) {
	return &IotOverMcpTransport{conn: conn}, nil
}

// 实现 Interface 接口
func (t *IotOverMcpTransport) Start(ctx context.Context) error {
	// TODO: 启动连接/监听消息等

	return nil
}

func (t *IotOverMcpTransport) SendRequest(ctx context.Context, request transport.JSONRPCRequest) (*transport.JSONRPCResponse, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	// TODO: 发送请求并同步等待响应
	err = t.conn.SendMcpMsg(payload)
	if err != nil {
		return nil, err
	}

	var response transport.JSONRPCResponse
	msg, err := t.conn.RecvMcpMsg(ctx, 15000) //15秒超时
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(msg, &response)
	return &response, nil
}

func (t *IotOverMcpTransport) SendNotification(ctx context.Context, notification mcp.JSONRPCNotification) error {
	// TODO: 发送通知消息
	if t.notifyHandler != nil {
		t.notifyHandler(notification)
	}
	return nil
}

func (t *IotOverMcpTransport) SetNotificationHandler(handler func(notification mcp.JSONRPCNotification)) {
	t.notifyHandler = handler
}

// SetOnCloseHandler 设置连接关闭回调
func (t *IotOverMcpTransport) SetOnCloseHandler(handler func(reason string)) {
	t.onCloseHandler = handler
}

func (t *IotOverMcpTransport) Close() error {
	// 通知client层连接即将关闭
	if t.onCloseHandler != nil {
		t.onCloseHandler("manual_close")
	}
	return nil
}

func (t *IotOverMcpTransport) GetSessionId() string {
	return ""
}
