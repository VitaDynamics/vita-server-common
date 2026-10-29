package login

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/smartwalle/alipay/v3"
)

// MiniAlipayLoginClient 支付宝小程序登录客户端，初始化需解析密钥，业务层应复用同一实例
type MiniAlipayLoginClient struct {
	client *alipay.Client
}

type GenMiniAlipayLoginClientParam struct {
	AppId      string        // 小程序 appid
	PrivateKey string        // 应用私钥
	PublicKey  string        // 支付宝公钥
	Production bool          // 是否生产网关
	Timeout    time.Duration // 请求超时，默认 5 秒
}

func NewMiniAlipayLoginClient(param GenMiniAlipayLoginClientParam) (*MiniAlipayLoginClient, error) {
	if param.AppId == "" || param.PrivateKey == "" || param.PublicKey == "" {
		return nil, errors.New("alipay mini app_id/private_key/public_key missing")
	}
	timeout := param.Timeout
	if timeout <= 0 {
		timeout = defaultMiniTimeout
	}
	client, err := alipay.New(param.AppId, param.PrivateKey, param.Production,
		alipay.WithHTTPClient(&http.Client{Timeout: timeout}))
	if err != nil {
		return nil, fmt.Errorf("init alipay mini login client failed: %w", err)
	}
	if err = client.LoadAliPayPublicKey(param.PublicKey); err != nil {
		return nil, fmt.Errorf("load alipay public key failed: %w", err)
	}
	return &MiniAlipayLoginClient{client: client}, nil
}

type AlipayOauthTokenResult struct {
	OpenId string // 应用开通 open_id 后返回
	UserId string // 未开通 open_id 时返回
}

// OauthToken 用 my.getAuthCode() 的 authCode 调用 alipay.system.oauth.token 换取用户标识
func (c *MiniAlipayLoginClient) OauthToken(ctx context.Context, authCode string) (*AlipayOauthTokenResult, error) {
	authCode = strings.TrimSpace(authCode)
	if authCode == "" {
		return nil, errors.New("alipay auth_code is empty")
	}
	result, err := c.client.SystemOauthToken(ctx, alipay.SystemOauthToken{
		GrantType: "authorization_code",
		Code:      authCode,
	})
	if err != nil {
		return nil, fmt.Errorf("alipay.system.oauth.token failed: %w", err)
	}
	if result == nil {
		return nil, errors.New("alipay.system.oauth.token empty response")
	}
	if result.OpenId == "" && result.UserId == "" {
		return nil, fmt.Errorf("alipay.system.oauth.token no open_id/user_id: code=%s msg=%s sub_code=%s sub_msg=%s",
			result.Code, result.Msg, result.SubCode, result.SubMsg)
	}
	return &AlipayOauthTokenResult{OpenId: result.OpenId, UserId: result.UserId}, nil
}
