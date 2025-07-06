package mqtt_udp

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"xiaozhi-esp32-server-golang/internal/app/server/common"
	"xiaozhi-esp32-server-golang/internal/data/client"
	. "xiaozhi-esp32-server-golang/internal/data/client"
	. "xiaozhi-esp32-server-golang/internal/data/msg"
	. "xiaozhi-esp32-server-golang/logger"
)

type MqttConfig struct {
	Broker   string
	Type     string
	Port     int
	ClientID string
	Username string
	Password string
}

// MqttSession 表示一个MQTT会话
type MqttSession struct {
	ID        string
	ClientID  string
	Key       string
	Nonce     string
	CreatedAt time.Time
}

// MqttServer MQTT服务器结构
type MqttServer struct {
	client               mqtt.Client
	udpServer            *UdpServer
	mqttConfig           *MqttConfig
	deviceId2ChatManager *sync.Map
	sync.RWMutex
}

// NewMqttServer 创建新的MQTT服务器
func NewMqttServer(config *MqttConfig, udpServer *UdpServer) *MqttServer {
	return &MqttServer{
		udpServer:            udpServer,
		mqttConfig:           config,
		deviceId2ChatManager: &sync.Map{},
	}
}

// Start 启动MQTT服务器
func (s *MqttServer) Start() error {
	opts := mqtt.NewClientOptions()
	opts.AddBroker(fmt.Sprintf("%s://%s:%d", s.mqttConfig.Type, s.mqttConfig.Broker, s.mqttConfig.Port))
	opts.SetClientID(s.mqttConfig.ClientID)
	opts.SetUsername(s.mqttConfig.Username)
	opts.SetPassword(s.mqttConfig.Password)

	opts.SetConnectionLostHandler(func(client mqtt.Client, err error) {
		Errorf("MQTT连接丢失: %v", err)
	})

	opts.SetOnConnectHandler(func(client mqtt.Client) {
		Info("MQTT已连接")
		// 订阅客户端消息主题
		topic := ServerSubTopicPrefix // 默认主题前缀
		if token := client.Subscribe(topic, 0, s.handleMessage); token.Wait() && token.Error() != nil {
			Errorf("订阅主题失败: %v", token.Error())
		}
	})

	s.client = mqtt.NewClient(opts)
	if token := s.client.Connect(); token.Wait() && token.Error() != nil {
		return fmt.Errorf("连接MQTT服务器失败: %v", token.Error())
	}

	err := s.checkClientActive()
	if err != nil {
		Errorf("检查客户端活跃失败: %v", err)
		return err
	}

	return nil
}

func (s *MqttServer) checkClientActive() error {
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.deviceId2ChatManager.Range(func(key, value interface{}) bool {
					chatManager := value.(*common.ChatManager)
					clientState := chatManager.GetClientState()
					if !clientState.IsActive() {
						Infof("clientState is not active, clear deviceId: %s", clientState.DeviceID)
						//解除udp会话
						s.udpServer.CloseSession(clientState.UdpInfo.ID)
						//删除mqtt关联关系
						s.deviceId2ChatManager.Delete(key)
						//销毁clientState
						clientState.Cancel()
						clientState.Destroy()
					}
					return true
				})
			}
		}
	}()
	return nil
}

func (s *MqttServer) SetChatManager(chatManager *common.ChatManager) {
	s.deviceId2ChatManager.Store(chatManager.GetDeviceId(), chatManager)
}

func (s *MqttServer) getChatManager(deviceId string) *common.ChatManager {
	if chatManager, ok := s.deviceId2ChatManager.Load(deviceId); ok {
		return chatManager.(*common.ChatManager)
	}
	return nil
}

// handleMessage 处理MQTT消息
func (s *MqttServer) handleMessage(client mqtt.Client, msg mqtt.Message) {
	Debugf("mqtt handleMessage, topic: %s, payload: %s", msg.Topic(), string(msg.Payload()))
	var clientMsg ClientMessage
	if err := json.Unmarshal(msg.Payload(), &clientMsg); err != nil {
		Errorf("解析JSON失败: %v", err)
		return
	}

	if clientMsg.Type == MessageTypeHello {
		s.handleHello(msg, clientMsg)
		return
	}

	_, deviceId := s.getDeviceIdByTopic(msg.Topic())
	if deviceId == "" {
		Errorf("deviceId is empty, msg: %+v", msg)
		return
	}

	chatManager := s.getChatManager(deviceId)
	if chatManager != nil {
		chatManager.GetClientState().UpdateLastActiveTs()
	}

	switch clientMsg.Type {
	case MessageTypeHello:
		s.handleHello(msg, clientMsg)
	default:
		chatManager.HandleTextMessage(msg.Payload())
	}
}

func (s *MqttServer) getDeviceIdByTopic(topic string) (string, string) {
	var topicMacAddr, deviceId string
	//根据topic(/p2p/device_public/mac_addr)解析出来mac_addr
	strList := strings.Split(topic, "/")
	if len(strList) == 4 {
		topicMacAddr = strList[3]
		deviceId = strings.ReplaceAll(topicMacAddr, "_", ":")
	}
	return topicMacAddr, deviceId
}

// handleHello 处理hello消息
func (s *MqttServer) handleHello(msg mqtt.Message, clientMsg client.ClientMessage) {
	// 检查传输协议
	if clientMsg.Transport != "udp" {
		Warnf("不支持的传输协议: %v", clientMsg.Transport)
		return
	}

	// 从UDP服务端获取会话信息
	session := s.udpServer.CreateSession(msg.Topic())
	if session == nil {
		Error("创建会话失败")
		return
	}

	topicMacAddr, deviceId := s.getDeviceIdByTopic(msg.Topic())
	if deviceId == "" {
		Errorf("mac_addr解析失败: %v", msg.Topic())
		return
	}

	publicTopic := fmt.Sprintf("%s%s", client.ServerPubTopicPrefix, topicMacAddr)

	//生成clientState结构
	clientState, err := client.GenMqttUdpClientState(deviceId, publicTopic, s.client, session, &clientMsg)
	if err != nil {
		Errorf("生成clientState失败: %v", err)
		return
	}

	chatManager := common.NewChatManager(clientState)

	//赋值给session
	session.ClientState = clientState

	//保存至deviceId2ChatManager
	s.SetChatManager(chatManager)

	clientState.InputAudioFormat = *clientMsg.AudioParams
	clientState.SetAsrPcmFrameSize(clientState.InputAudioFormat.SampleRate, clientState.InputAudioFormat.Channels, clientState.InputAudioFormat.FrameDuration)

	common.ProcessVadAudio(clientState)

	strAesKey := hex.EncodeToString(session.AesKey[:])

	// 构造 fullNonce: 前缀2字节0100 + 长度2字节0000 + 真实nonce(8字节) + seq(4字节00000000)
	prefix := []byte{0x01, 0x00}
	length := []byte{0x00, 0x00}
	seq := []byte{0x00, 0x00, 0x00, 0x00}
	fullNonce := append(append(append(prefix, length...), session.Nonce[:]...), seq...)
	strFullNonce := hex.EncodeToString(fullNonce)
	// 构建响应
	response := map[string]interface{}{
		"type":       MessageTypeHello,
		"version":    3,
		"session_id": session.ID,
		"transport":  "udp",
		"udp": map[string]interface{}{
			"server": s.udpServer.externalHost,
			"port":   s.udpServer.externalPort,
			"key":    strAesKey,
			"nonce":  strFullNonce,
		},
		"audio_params": map[string]interface{}{
			"format":         clientState.OutputAudioFormat.Format,
			"sample_rate":    clientState.OutputAudioFormat.SampleRate,
			"channels":       clientState.OutputAudioFormat.Channels,
			"frame_duration": clientState.OutputAudioFormat.FrameDuration, // 固定20ms帧长
		},
	}

	// 发送响应
	clientState.Conn.WriteJSON(response)
}

// handleGoodbye 处理goodbye消息
func (s *MqttServer) handleGoodbye(msg mqtt.Message, clientMsg client.ClientMessage) {
	/*sessionID, ok := clientMsg.SessionID
	if !ok {
		Warn("会话ID无效")
		return
	}

	s.udpServer.CloseSession(sessionID)
	Infof("会话已关闭: %s", sessionID)*/
}
