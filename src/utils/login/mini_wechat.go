package login

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultMiniTimeout = 5 * time.Second

// WechatCode2SessionURL 微信 jscode2session 接口地址，测试时可替换为本地服务
var WechatCode2SessionURL = "https://api.weixin.qq.com/sns/jscode2session"

type WechatCode2SessionParam struct {
	AppId   string        // 小程序 appid
	Secret  string        // 小程序 secret
	JsCode  string        // wx.login() 返回的 code
	Timeout time.Duration // 请求超时，默认 5 秒
}

type WechatCode2SessionResult struct {
	OpenId     string
	UnionId    string
	SessionKey string // 仅服务端使用，不得下发前端
}

type wechatCode2SessionResp struct {
	OpenId     string `json:"openid"`
	SessionKey string `json:"session_key"`
	UnionId    string `json:"unionid"`
	ErrCode    int    `json:"errcode"`
	ErrMsg     string `json:"errmsg"`
}

// WechatCode2Session 用 wx.login() 的 code 换取 openid
func WechatCode2Session(ctx context.Context, param WechatCode2SessionParam) (*WechatCode2SessionResult, error) {
	jsCode := strings.TrimSpace(param.JsCode)
	if jsCode == "" {
		return nil, errors.New("wechat js_code is empty")
	}
	if param.AppId == "" || param.Secret == "" {
		return nil, errors.New("wechat mini app_id/secret missing")
	}
	query := url.Values{}
	query.Set("appid", param.AppId)
	query.Set("secret", param.Secret)
	query.Set("js_code", jsCode)
	query.Set("grant_type", "authorization_code")

	timeout := param.Timeout
	if timeout <= 0 {
		timeout = defaultMiniTimeout
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodGet, WechatCode2SessionURL+"?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	httpResp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("wechat jscode2session request failed: %w", err)
	}
	defer httpResp.Body.Close()
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("wechat jscode2session read body failed: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wechat jscode2session http status %d", httpResp.StatusCode)
	}

	var result wechatCode2SessionResp
	if err = json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("wechat jscode2session decode failed: %w", err)
	}
	if result.ErrCode != 0 {
		return nil, fmt.Errorf("wechat jscode2session errcode=%d errmsg=%s", result.ErrCode, result.ErrMsg)
	}
	if result.OpenId == "" {
		return nil, errors.New("wechat jscode2session openid is empty")
	}
	return &WechatCode2SessionResult{
		OpenId:     result.OpenId,
		UnionId:    result.UnionId,
		SessionKey: result.SessionKey,
	}, nil
}
