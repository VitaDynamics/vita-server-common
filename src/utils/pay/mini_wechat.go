package pay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

const (
	WechatV3JsapiOrderURL = "https://api.mch.weixin.qq.com/v3/pay/transactions/jsapi"
	WechatV3RefundURL     = "https://api.mch.weixin.qq.com/v3/refund/domestic/refunds"
	WechatV3QueryRefund   = "https://api.mch.weixin.qq.com/v3/refund/domestic/refunds/%s"
)

// 微信支付 V3 回调事件类型
const (
	WechatNotifyTransactionSuccess = "TRANSACTION.SUCCESS"
	WechatNotifyRefundSuccess      = "REFUND.SUCCESS"
	WechatNotifyRefundAbnormal     = "REFUND.ABNORMAL"
	WechatNotifyRefundClosed       = "REFUND.CLOSED"
)

// MiniWechatPayClient 微信小程序 JSAPI 支付客户端
type MiniWechatPayClient struct {
	client *WechatClient
}

type GenMiniWechatPayClientParam struct {
	AppId         string        // 小程序 appid
	MchId         string        // 商户号
	ApiKeyV3      string        // APIv3 密钥，用于解密回调
	SerialNo      string        // 商户 API 证书序列号
	PrivateKeyPEM string        // 商户 API 私钥（PKCS8）
	PublicKeyPEM  string        // 微信支付公钥，用于回调验签
	NotifyUrl     string        // 默认回调地址，下单 / 退款参数未指定时使用
	Timeout       time.Duration // 请求超时，默认 10 秒
}

func NewMiniWechatPayClient(param GenMiniWechatPayClientParam) (*MiniWechatPayClient, error) {
	if param.AppId == "" || param.MchId == "" || param.SerialNo == "" || param.PrivateKeyPEM == "" {
		return nil, errors.New("wechat mini pay app_id/mch_id/serial_no/private_key missing")
	}
	if len(param.ApiKeyV3) != 32 {
		return nil, errors.New("wechat mini pay api_key_v3 must be 32 bytes")
	}
	timeout := param.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	client := &WechatClient{
		AppId:      param.AppId,
		MchId:      param.MchId,
		ApiKeyV3:   param.ApiKeyV3,
		NotifyUrl:  param.NotifyUrl,
		SerialNo:   param.SerialNo,
		HttpClient: &http.Client{Timeout: timeout},
	}
	if err := client.loadPrivateKey(param.PrivateKeyPEM); err != nil {
		return nil, fmt.Errorf("load wechat mini pay private key failed: %w", err)
	}
	if param.PublicKeyPEM != "" {
		if err := client.LoadPublicKey(param.PublicKeyPEM); err != nil {
			return nil, fmt.Errorf("load wechat mini pay public key failed: %w", err)
		}
	}
	return &MiniWechatPayClient{client: client}, nil
}

type MiniWechatJsapiOrderParam struct {
	OutTradeNo  string     // 商户交易号
	Description string     // 商品描述
	AmountCent  int64      // 金额（分）
	OpenId      string     // 小程序用户 openid
	TimeExpire  *time.Time // 支付截止时间
	Attach      string     // 附加数据，回调原样返回
	NotifyUrl   string     // 为空时使用客户端默认回调地址
}

type wechatV3JsapiOrderRequest struct {
	AppId       string            `json:"appid"`
	MchId       string            `json:"mchid"`
	Description string            `json:"description"`
	OutTradeNo  string            `json:"out_trade_no"`
	TimeExpire  string            `json:"time_expire,omitempty"`
	Attach      string            `json:"attach,omitempty"`
	NotifyUrl   string            `json:"notify_url"`
	Amount      wechatV3AmountReq `json:"amount"`
	Payer       WechatV3Payer     `json:"payer"`
}

type wechatV3AmountReq struct {
	Total    int64  `json:"total"`
	Currency string `json:"currency"`
}

// CreateJsapiOrder JSAPI 下单，返回 prepay_id
func (c *MiniWechatPayClient) CreateJsapiOrder(ctx context.Context, param MiniWechatJsapiOrderParam) (string, error) {
	if param.OutTradeNo == "" || param.OpenId == "" || param.AmountCent <= 0 {
		return "", errors.New("out_trade_no/openid/amount invalid")
	}
	req := wechatV3JsapiOrderRequest{
		AppId:       c.client.AppId,
		MchId:       c.client.MchId,
		Description: param.Description,
		OutTradeNo:  param.OutTradeNo,
		Attach:      param.Attach,
		NotifyUrl:   c.notifyUrl(param.NotifyUrl),
		Amount:      wechatV3AmountReq{Total: param.AmountCent, Currency: "CNY"},
		Payer:       WechatV3Payer{OpenId: param.OpenId},
	}
	if param.TimeExpire != nil {
		req.TimeExpire = param.TimeExpire.Format(time.RFC3339)
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	resp, err := c.client.doRequestWithContext(ctx, http.MethodPost, WechatV3JsapiOrderURL, body)
	if err != nil {
		return "", err
	}
	var result WechatV3AppOrderResponse
	if err = json.Unmarshal(resp, &result); err != nil {
		return "", err
	}
	if result.PrepayId == "" {
		return "", errors.New("wechat jsapi order prepay_id is empty")
	}
	return result.PrepayId, nil
}

// MiniWechatRequestPayment wx.requestPayment 调起参数
type MiniWechatRequestPayment struct {
	TimeStamp string `json:"timeStamp"`
	NonceStr  string `json:"nonceStr"`
	Package   string `json:"package"`
	SignType  string `json:"signType"`
	PaySign   string `json:"paySign"`
}

// BuildRequestPayment 用 prepay_id 生成小程序调起参数，可对同一 prepay_id 重复签名
func (c *MiniWechatPayClient) BuildRequestPayment(prepayId string) (*MiniWechatRequestPayment, error) {
	if prepayId == "" {
		return nil, errors.New("prepay_id is empty")
	}
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	nonceStr := generateNonceStr()
	pkg := "prepay_id=" + prepayId
	message := fmt.Sprintf("%s\n%s\n%s\n%s\n", c.client.AppId, timestamp, nonceStr, pkg)
	paySign, err := c.client.signSHA256(message)
	if err != nil {
		return nil, err
	}
	return &MiniWechatRequestPayment{
		TimeStamp: timestamp,
		NonceStr:  nonceStr,
		Package:   pkg,
		SignType:  "RSA",
		PaySign:   paySign,
	}, nil
}

// QueryOrder 按商户交易号查单；交易不存在时返回 Code=ORDER_NOT_EXIST 的 *WechatV3APIError
func (c *MiniWechatPayClient) QueryOrder(ctx context.Context, outTradeNo string) (*WechatV3QueryOrderResponse, error) {
	apiUrl := fmt.Sprintf(WechatV3QueryOrderByMch, url.PathEscape(outTradeNo), c.client.MchId)
	resp, err := c.client.doRequestWithContext(ctx, http.MethodGet, apiUrl, nil)
	if err != nil {
		return nil, err
	}
	var result WechatV3QueryOrderResponse
	if err = json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// CloseOrder 关闭未支付交易
func (c *MiniWechatPayClient) CloseOrder(ctx context.Context, outTradeNo string) error {
	apiUrl := fmt.Sprintf(WechatV3CloseOrderURL, url.PathEscape(outTradeNo))
	body, err := json.Marshal(map[string]string{"mchid": c.client.MchId})
	if err != nil {
		return err
	}
	_, err = c.client.doRequestWithContext(ctx, http.MethodPost, apiUrl, body)
	return err
}

type MiniWechatRefundParam struct {
	OutTradeNo    string // 原交易商户单号，与 TransactionId 二选一
	TransactionId string // 原交易微信单号
	OutRefundNo   string // 商户退款单号，结果不明时必须复用
	Reason        string
	RefundCent    int64  // 退款金额（分）
	TotalCent     int64  // 原交易金额（分）
	NotifyUrl     string // 为空时使用客户端默认回调地址
}

type wechatV3RefundRequest struct {
	TransactionId string                  `json:"transaction_id,omitempty"`
	OutTradeNo    string                  `json:"out_trade_no,omitempty"`
	OutRefundNo   string                  `json:"out_refund_no"`
	Reason        string                  `json:"reason,omitempty"`
	NotifyUrl     string                  `json:"notify_url,omitempty"`
	Amount        wechatV3RefundAmountReq `json:"amount"`
}

type wechatV3RefundAmountReq struct {
	Refund   int64  `json:"refund"`
	Total    int64  `json:"total"`
	Currency string `json:"currency"`
}

// WechatV3RefundResponse 退款申请 / 查询返回，Status：SUCCESS、CLOSED、PROCESSING、ABNORMAL
type WechatV3RefundResponse struct {
	RefundId            string                `json:"refund_id"`
	OutRefundNo         string                `json:"out_refund_no"`
	TransactionId       string                `json:"transaction_id"`
	OutTradeNo          string                `json:"out_trade_no"`
	Channel             string                `json:"channel"`
	UserReceivedAccount string                `json:"user_received_account"`
	SuccessTime         string                `json:"success_time"`
	CreateTime          string                `json:"create_time"`
	Status              string                `json:"status"`
	Amount              *WechatV3RefundAmount `json:"amount,omitempty"`
}

type WechatV3RefundAmount struct {
	Total       int64  `json:"total"`
	Refund      int64  `json:"refund"`
	PayerTotal  int64  `json:"payer_total"`
	PayerRefund int64  `json:"payer_refund"`
	Currency    string `json:"currency,omitempty"`
}

// Refund 申请退款，结果以退款回调或 QueryRefund 为准
func (c *MiniWechatPayClient) Refund(ctx context.Context, param MiniWechatRefundParam) (*WechatV3RefundResponse, error) {
	if param.OutTradeNo == "" && param.TransactionId == "" {
		return nil, errors.New("out_trade_no and transaction_id are both empty")
	}
	if param.OutRefundNo == "" || param.RefundCent <= 0 || param.TotalCent < param.RefundCent {
		return nil, errors.New("out_refund_no/refund amount invalid")
	}
	req := wechatV3RefundRequest{
		TransactionId: param.TransactionId,
		OutTradeNo:    param.OutTradeNo,
		OutRefundNo:   param.OutRefundNo,
		Reason:        param.Reason,
		NotifyUrl:     c.notifyUrl(param.NotifyUrl),
		Amount:        wechatV3RefundAmountReq{Refund: param.RefundCent, Total: param.TotalCent, Currency: "CNY"},
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.doRequestWithContext(ctx, http.MethodPost, WechatV3RefundURL, body)
	if err != nil {
		return nil, err
	}
	var result WechatV3RefundResponse
	if err = json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// QueryRefund 按商户退款单号查询退款；退款单不存在时返回 Code=RESOURCE_NOT_EXISTS 的 *WechatV3APIError
func (c *MiniWechatPayClient) QueryRefund(ctx context.Context, outRefundNo string) (*WechatV3RefundResponse, error) {
	apiUrl := fmt.Sprintf(WechatV3QueryRefund, url.PathEscape(outRefundNo))
	resp, err := c.client.doRequestWithContext(ctx, http.MethodGet, apiUrl, nil)
	if err != nil {
		return nil, err
	}
	var result WechatV3RefundResponse
	if err = json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// WechatV3RefundNotifyResource 退款回调解密后的内容，RefundStatus：SUCCESS、CLOSED、ABNORMAL
type WechatV3RefundNotifyResource struct {
	MchId               string                `json:"mchid"`
	OutTradeNo          string                `json:"out_trade_no"`
	TransactionId       string                `json:"transaction_id"`
	OutRefundNo         string                `json:"out_refund_no"`
	RefundId            string                `json:"refund_id"`
	RefundStatus        string                `json:"refund_status"`
	SuccessTime         string                `json:"success_time"`
	UserReceivedAccount string                `json:"user_received_account"`
	Amount              *WechatV3RefundAmount `json:"amount,omitempty"`
}

// MiniWechatNotify 验签解密后的回调；支付事件填充 Transaction，退款事件填充 Refund
type MiniWechatNotify struct {
	Id          string
	EventType   string
	Transaction *WechatV3QueryOrderResponse
	Refund      *WechatV3RefundNotifyResource
}

// ParseNotify 校验回调签名并解密，支付与退款共用同一回调地址
func (c *MiniWechatPayClient) ParseNotify(header http.Header, body []byte) (*MiniWechatNotify, error) {
	if err := c.client.verifyNotifySignature(header, body); err != nil {
		return nil, err
	}
	var notifyReq WechatV3NotifyRequest
	if err := json.Unmarshal(body, &notifyReq); err != nil {
		return nil, fmt.Errorf("decode notify body failed: %w", err)
	}
	plaintext, err := c.client.decryptNotifyResource(notifyReq.Resource)
	if err != nil {
		return nil, fmt.Errorf("decrypt notify resource failed: %w", err)
	}

	notify := &MiniWechatNotify{Id: notifyReq.Id, EventType: notifyReq.EventType}
	switch notifyReq.EventType {
	case WechatNotifyTransactionSuccess:
		notify.Transaction = &WechatV3QueryOrderResponse{}
		err = json.Unmarshal([]byte(plaintext), notify.Transaction)
	case WechatNotifyRefundSuccess, WechatNotifyRefundAbnormal, WechatNotifyRefundClosed:
		notify.Refund = &WechatV3RefundNotifyResource{}
		err = json.Unmarshal([]byte(plaintext), notify.Refund)
	default:
		return nil, fmt.Errorf("unsupported wechat notify event_type: %s", notifyReq.EventType)
	}
	if err != nil {
		return nil, fmt.Errorf("decode notify resource failed: %w", err)
	}
	return notify, nil
}

func (c *MiniWechatPayClient) notifyUrl(override string) string {
	if override != "" {
		return override
	}
	return c.client.NotifyUrl
}
