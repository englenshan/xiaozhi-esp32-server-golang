package manager

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"xiaozhi-esp32-server-golang/internal/domain/config/types"
	log "xiaozhi-esp32-server-golang/logger"
)

// HTTP接口响应结构体

// CheckActivationResponse 检查激活状态响应
type CheckActivationResponse struct {
	Activated bool   `json:"activated"`
	Message   string `json:"message"`
}

// GetActivationInfoResponse 获取激活信息响应
type GetActivationInfoResponse struct {
	Activated bool   `json:"activated"`
	Code      string `json:"code,omitempty"` // 修改为string类型以匹配后端API
	Challenge string `json:"challenge,omitempty"`
	Message   string `json:"message,omitempty"`
}

// ActivateDeviceRequest 设备激活请求
type ActivateDeviceRequest struct {
	DeviceId     string `json:"device_id"`
	ClientId     string `json:"client_id"`
	Code         string `json:"code"`
	Challenge    string `json:"challenge"`
	Algorithm    string `json:"algorithm"`
	SerialNumber string `json:"serial_number"`
	Hmac         string `json:"hmac"`
}

// ActivateDeviceResponse 设备激活响应
type ActivateDeviceResponse struct {
	Success bool        `json:"success"`
	Message string      `json:"message"`
	Error   string      `json:"error,omitempty"`
	Data    interface{} `json:"data,omitempty"`
}

// IsDeviceActivated 检查设备是否已激活
func (am *ConfigManager) IsDeviceActivated(ctx context.Context, deviceId string, clientId string) (bool, error) {
	// 直接调用后端管理系统的HTTP接口
	activated, err := am.callCheckActivationAPI(ctx, deviceId, clientId)
	if err != nil {
		log.Log().Errorf("检查设备 %s 激活状态失败: %v", deviceId, err)
		return false, err
	}

	log.Log().Debugf("设备 %s 激活状态: %v", deviceId, activated)
	return activated, nil
}

// GetActivationInfo 获取设备激活信息
func (am *ConfigManager) GetActivationInfo(ctx context.Context, deviceId string, clientId string) (int, string, string, int) {
	// 直接调用后端管理系统的HTTP接口
	activated, codeStr, challenge, message, err := am.callGetActivationInfoAPI(ctx, deviceId, clientId)
	if err != nil {
		log.Log().Errorf("获取设备 %s 激活信息失败: %v", deviceId, err)
		return 0, "", "", 0
	}

	// 如果设备已激活，直接返回
	if activated {
		log.Log().Debugf("设备 %s 已激活", deviceId)
		return 0, "", message, 0
	}

	// 检查Challenge是否为空
	if challenge == "" {
		log.Log().Errorf("设备 %s 的Challenge字段为空", deviceId)
		return 0, "", "Challenge字段为空，请联系管理员", 0
	}

	// 设备未激活，返回激活信息
	timeoutMs := 300 // 默认5分钟超时
	log.Log().Debugf("获取设备 %s 激活信息: code=%s, challenge=%s", deviceId, codeStr, challenge)

	// 将字符串类型的code转换为int
	code := 0
	if codeStr != "" {
		if parsedCode, err := strconv.Atoi(codeStr); err == nil {
			code = parsedCode
		} else {
			log.Log().Warnf("设备 %s 激活码格式错误: %s, 错误: %v", deviceId, codeStr, err)
		}
		log.Log().Debugf("设备 %s 激活码: %s (转换为: %d)", deviceId, codeStr, code)
	}

	return code, challenge, message, timeoutMs
}

// VerifyChallenge 验证挑战码和HMAC
func (am *ConfigManager) VerifyChallenge(ctx context.Context, deviceId string, clientId string, activationPayload types.ActivationPayload) (bool, error) {
	// 验证HMAC（如果提供了HMAC）
	if activationPayload.HMAC != "" {
		if !am.verifyHMAC(activationPayload.Challenge, activationPayload.HMAC) {
			log.Log().Warnf("设备 %s HMAC验证失败", deviceId)
			return false, fmt.Errorf("HMAC验证失败")
		}
	}

	// 直接调用后端管理系统的激活接口
	verified, err := am.callActivateDeviceAPI(ctx, deviceId, clientId, activationPayload)
	if err != nil {
		log.Log().Errorf("设备激活失败: %v", err)
		return false, err
	}

	if verified {
		log.Log().Infof("设备 %s 激活验证成功", deviceId)
	}

	return verified, nil
}

// verifyHMAC 验证HMAC签名
func (am *ConfigManager) verifyHMAC(challenge, providedHmac string) bool {
	// 这里可以根据实际需求配置密钥
	// 暂时使用空密钥，实际应用中应该从配置中获取
	secretKey := ""

	if secretKey == "" {
		// 如果没有配置密钥，直接通过验证
		return true
	}

	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(challenge))
	expectedHmac := hex.EncodeToString(mac.Sum(nil))

	return expectedHmac == providedHmac
}

// HTTP API 调用方法

// callCheckActivationAPI 调用检查激活状态接口
func (am *ConfigManager) callCheckActivationAPI(ctx context.Context, deviceId, clientId string) (bool, error) {
	// 构建请求URL
	reqURL := fmt.Sprintf("%s/api/public/device/check-activation", am.baseURL)

	// 添加查询参数
	params := url.Values{}
	params.Add("device_id", deviceId)
	params.Add("client_id", clientId)
	reqURL += "?" + params.Encode()

	// 创建HTTP请求
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return false, fmt.Errorf("创建请求失败: %v", err)
	}

	// 发送请求
	resp, err := am.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	// 读取响应
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("读取响应失败: %v", err)
	}

	// 解析响应
	var response CheckActivationResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return false, fmt.Errorf("解析响应失败: %v", err)
	}

	log.Log().Debugf("检查激活状态响应: %+v", response)
	return response.Activated, nil
}

// callGetActivationInfoAPI 调用获取激活信息接口
func (am *ConfigManager) callGetActivationInfoAPI(ctx context.Context, deviceId, clientId string) (bool, string, string, string, error) {
	// 构建请求URL
	reqURL := fmt.Sprintf("%s/api/public/device/activation-info", am.baseURL)

	// 添加查询参数
	params := url.Values{}
	params.Add("device_id", deviceId)
	params.Add("client_id", clientId)
	reqURL += "?" + params.Encode()

	// 创建HTTP请求
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return false, "", "", "", fmt.Errorf("创建请求失败: %v", err)
	}

	// 发送请求
	resp, err := am.httpClient.Do(req)
	if err != nil {
		return false, "", "", "", fmt.Errorf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	// 读取响应
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, "", "", "", fmt.Errorf("读取响应失败: %v", err)
	}

	// 添加更详细的响应日志
	log.Log().Debugf("激活信息API响应状态码: %d, 响应体: %s", resp.StatusCode, string(body))

	// 解析响应
	var response GetActivationInfoResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return false, "", "", "", fmt.Errorf("解析响应失败: %v", err)
	}

	log.Log().Debugf("获取激活信息响应: %+v", response)

	if response.Activated {
		return true, "", "", response.Message, nil
	}

	return false, response.Code, response.Challenge, response.Message, nil
}

// callActivateDeviceAPI 调用设备激活接口
func (am *ConfigManager) callActivateDeviceAPI(ctx context.Context, deviceId, clientId string, activationPayload types.ActivationPayload) (bool, error) {
	// 构建请求URL
	reqURL := fmt.Sprintf("%s/api/public/device/activate", am.baseURL)

	// 构建请求体
	request := ActivateDeviceRequest{
		DeviceId:     deviceId,
		ClientId:     clientId,
		Challenge:    activationPayload.Challenge,
		Algorithm:    activationPayload.Algorithm,
		SerialNumber: activationPayload.SerialNumber,
		Hmac:         activationPayload.HMAC,
	}

	// 序列化请求体
	requestBody, err := json.Marshal(request)
	if err != nil {
		return false, fmt.Errorf("序列化请求失败: %v", err)
	}

	// 创建HTTP请求
	req, err := http.NewRequestWithContext(ctx, "POST", reqURL, bytes.NewBuffer(requestBody))
	if err != nil {
		return false, fmt.Errorf("创建请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// 发送请求
	resp, err := am.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	// 读取响应
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("读取响应失败: %v", err)
	}

	// 解析响应
	var response ActivateDeviceResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return false, fmt.Errorf("解析响应失败: %v", err)
	}

	log.Log().Debugf("设备激活响应: %+v", response)

	if !response.Success {
		return false, nil
	}

	return response.Success, nil
}
