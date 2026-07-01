package artc

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

type GenerateTokenParams struct {
	AppId     string `yaml:"app_id"`
	AppSecret string `yaml:"app_secret"`
	UserId    string `yaml:"user_id"`
	ChannelId string `yaml:"channel_id"`
	ExpiresTS int64  `yaml:"expires_ts"`
}

// GenerateToken 生成 ARTC token
// 计算逻辑对齐 C++ 版本：
// 1) tokenHash = SHA256(appId + appSecret + channelId + userId + timestamp)
// 2) payload = {appid, channelid, userid, nonce:"", timestamp, token:tokenHash}
// 3) 返回 Base64(payloadJSON)
func GenerateToken(params *GenerateTokenParams) (string, error) {
	if params == nil {
		return "", fmt.Errorf("ARTC config not found in global configuration")
	}

	if params.AppId == "" || params.AppSecret == "" {
		return "", fmt.Errorf("AppId or AppSecret not configured")
	}

	expiresTS := params.ExpiresTS
	if expiresTS == 0 {
		// 默认 24 小时后过期
		expiresTS = time.Now().Unix() + 24*3600
	}

	timestamp := strconv.FormatInt(expiresTS, 10)
	data := params.AppId + params.AppSecret + params.ChannelId + params.UserId + timestamp
	hash := sha256.Sum256([]byte(data))
	tokenHash := hex.EncodeToString(hash[:])

	payload := map[string]any{
		"appid":     params.AppId,
		"channelid": params.ChannelId,
		"userid":    params.UserId,
		"nonce":     "",
		"timestamp": expiresTS,
		"token":     tokenHash,
	}

	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal ARTC token payload: %v", err)
	}

	return base64.StdEncoding.EncodeToString(jsonBytes), nil
}

// GenerateTokenWithPrivileges 兼容旧调用，当前 ARTC 计算不区分权限位。
// 参数 audio/video/screen 保留用于兼容。
func GenerateTokenWithPrivileges(userId, channelId string, expiresTS int64, audio, video, screen bool) (string, error) {
	_ = audio
	_ = video
	_ = screen
	params := &GenerateTokenParams{
		UserId:    userId,
		ChannelId: channelId,
		ExpiresTS: expiresTS,
	}
	return GenerateToken(params)
}
