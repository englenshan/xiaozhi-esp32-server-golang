package doubao

import (
	"context"
	"fmt"
	"time"

	"xiaozhi-esp32-server-golang/internal/domain/asr/doubao/client"
	"xiaozhi-esp32-server-golang/internal/domain/asr/doubao/response"
	"xiaozhi-esp32-server-golang/internal/domain/asr/types"
	log "xiaozhi-esp32-server-golang/logger"
)

// DoubaoV2ASR 豆包ASR实现
type DoubaoV2ASR struct {
	config      DoubaoV2Config
	isStreaming bool
	reqID       string
	connectID   string

	// 流式识别相关字段
	result      string
	err         error
	sendDataCnt int
}

// NewDoubaoV2ASR 创建一个新的豆包ASR实例
func NewDoubaoV2ASR(config DoubaoV2Config) (*DoubaoV2ASR, error) {
	log.Info("创建豆包ASR实例")
	log.Info(fmt.Sprintf("配置: %+v", config))

	if config.AppID == "" {
		log.Error("缺少appid配置")
		return nil, fmt.Errorf("缺少appid配置")
	}
	if config.AccessToken == "" {
		log.Error("缺少access_token配置")
		return nil, fmt.Errorf("缺少access_token配置")
	}

	// 使用默认配置填充缺失的字段
	if config.WsURL == "" {
		config.WsURL = DefaultConfig.WsURL
	}
	if config.ModelName == "" {
		config.ModelName = DefaultConfig.ModelName
	}
	if config.EndWindowSize == 0 {
		config.EndWindowSize = DefaultConfig.EndWindowSize
	}
	if config.ChunkDuration == 0 {
		config.ChunkDuration = DefaultConfig.ChunkDuration
	}
	if config.Timeout == 0 {
		config.Timeout = DefaultConfig.Timeout
	}

	connectID := fmt.Sprintf("%d", time.Now().UnixNano())

	return &DoubaoV2ASR{
		config:    config,
		connectID: connectID,
	}, nil
}

// StreamingRecognize 实现流式识别接口
func (d *DoubaoV2ASR) StreamingRecognize(ctx context.Context, audioStream <-chan []float32) (chan types.StreamingResult, error) {
	// 建立连接
	c := client.NewAsrWsClient(d.config.WsURL, d.config.AppID, d.config.AccessToken)

	// 豆包返回的识别结果
	doubaoResultChan := make(chan *response.AsrResponse, 10)
	//程序内部的结果通道
	resultChan := make(chan types.StreamingResult, 10)

	err := c.CreateConnection(ctx)
	if err != nil {
		log.Errorf("doubao asr failed to create connection: %v", err)
		return nil, fmt.Errorf("create connection err: %w", err)
	}
	err = c.SendFullClientRequest()
	if err != nil {
		log.Errorf("doubao asr failed to send full request: %v", err)
		c.Close() // 确保连接被关闭
		return nil, fmt.Errorf("send full request err: %w", err)
	}

	go func() {
		if err := c.StartAudioStream(ctx, audioStream, doubaoResultChan); err != nil {
			log.Errorf("豆包ASR音频流发送失败: %v", err)
		}
	}()

	// 启动结果接收goroutine，传递client用于清理
	go d.receiveStreamResults(ctx, resultChan, doubaoResultChan, c)

	return resultChan, nil
}

// receiveStreamResults 接收流式识别结果
func (d *DoubaoV2ASR) receiveStreamResults(ctx context.Context, resultChan chan types.StreamingResult, asrResponseChan chan *response.AsrResponse, client *client.AsrWsClient) {
	defer func() {
		close(resultChan)
		if client != nil {
			client.Close() // 确保WebSocket连接被关闭
		}
	}()
	for {
		select {
		case <-ctx.Done():
			log.Debugf("receiveStreamResults 上下文已取消")
			return
		case result, ok := <-asrResponseChan:
			if !ok {
				log.Debugf("receiveStreamResults asrResponseChan 已关闭")
				return
			}

			// 处理所有结果，包括中间结果和最终结果
			if result.PayloadMsg != nil && result.PayloadMsg.Result.Text != "" {
				resultChan <- types.StreamingResult{
					Text:    result.PayloadMsg.Result.Text,
					IsFinal: result.IsLastPackage,
				}
			}

			// 如果是最终结果，结束处理
			if result.IsLastPackage {
				return
			}
		}
	}
}

// Reset 重置ASR状态
func (d *DoubaoV2ASR) Reset() error {

	log.Info("ASR状态已重置")
	return nil
}
