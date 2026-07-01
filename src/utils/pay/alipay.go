package pay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/smartwalle/alipay/v3"
)

const AlipayProductionGateway = "https://openapi.alipay.com/gateway.do"
const AlipaySandboxGateway = "https://openapi.alipaydev.com/gateway.do"

// AlipayClient 支付宝支付客户端
type AlipayClient struct {
	AppId        string
	PrivateKey   string
	PublicKey    string
	NotifyUrl    string
	GatewayUrl   string
	isProduction bool

	client *alipay.Client
}

type GenAlipayClientParam struct {
	AppId      string
	PrivateKey string
	PublicKey  string
	NotifyUrl  string
	GatewayUrl string
	LogCtx     map[string]any
}

// NewAlipayClient 创建支付宝支付客户端
func NewAlipayClient(param GenAlipayClientParam) *AlipayClient {
	isProduction := !strings.Contains(param.GatewayUrl, "alipaydev")

	var aliClient *alipay.Client
	var err error

	if isProduction {
		aliClient, err = alipay.New(param.AppId, param.PrivateKey, true)
	} else {
		aliClient, err = alipay.New(param.AppId, param.PrivateKey, false)
	}

	if err != nil {
		logrus.WithFields(logrus.Fields(param.LogCtx)).Errorf("Failed to initialize alipay client: %v", err)
		return nil
	} else {
		if err = aliClient.LoadAliPayPublicKey(param.PublicKey); err != nil {
			logrus.WithFields(logrus.Fields(param.LogCtx)).Errorf("Failed to load alipay public key: %v", err)
			return nil
		}
	}

	return &AlipayClient{
		AppId:        param.AppId,
		PrivateKey:   param.PrivateKey,
		PublicKey:    param.PublicKey,
		NotifyUrl:    param.NotifyUrl,
		GatewayUrl:   param.GatewayUrl,
		isProduction: isProduction,
		client:       aliClient,
	}
}

type CreatePayOrderParams struct {
	OrderNo     string  `json:"orderNo"`
	PackageType string  `json:"packageType"`
	Amount      float64 `json:"amount"`
	AppId       string  `json:"appId,omitempty"`
	PayType     string  `json:"payType"` // wechat or alipay
	LogCtx      map[string]any
}

// CreatePayParams 创建 APP 支付参数
func (c *AlipayClient) CreatePayParams(params CreatePayOrderParams) map[string]string {
	params.AppId = ""
	return c.CreatePayParamsWithAppId(params)
}

// CreatePayParamsWithAppId 创建 APP 支付参数，支持指定 appId
// 如果 appId 为空则使用客户端默认的 AppId
func (c *AlipayClient) CreatePayParamsWithAppId(params CreatePayOrderParams) map[string]string {
	var cli *alipay.Client
	if params.AppId != "" && params.AppId != c.AppId {
		var err error
		cli, err = alipay.New(params.AppId, c.PrivateKey, c.isProduction)
		if err != nil {
			logrus.WithFields(logrus.Fields(params.LogCtx)).Errorf("Failed to create alipay client for appId %s: %v", params.AppId, err)
			return nil
		}
		if err = cli.LoadAliPayPublicKey(c.PublicKey); err != nil {
			logrus.WithFields(logrus.Fields(params.LogCtx)).Errorf("Failed to load alipay public key for appId %s: %v", params.AppId, err)
			return nil
		}
	} else {
		if c.client == nil {
			logrus.WithFields(logrus.Fields(params.LogCtx)).Errorf("Alipay client not initialized")
			return nil
		}
		cli = c.client
	}

	var p = alipay.TradeAppPay{}
	p.NotifyURL = c.NotifyUrl
	p.OutTradeNo = params.OrderNo
	p.Subject = fmt.Sprintf("服务包-%s", params.PackageType)
	p.TotalAmount = fmt.Sprintf("%.2f", params.Amount)
	p.ProductCode = "QUICK_MSECURITY_PAY"

	payString, err := cli.TradeAppPay(p)
	if err != nil {
		logrus.WithFields(logrus.Fields(params.LogCtx)).Errorf("Failed to create alipay app pay params: %v", err)
		return nil
	}

	return map[string]string{
		"orderString": payString,
		"outTradeNo":  params.OrderNo,
	}
}

// CancelDeductAgreement 取消代扣协议（mock，保持接口兼容）
func (c *AlipayClient) CancelDeductAgreement(contractId string, logCtx map[string]any) {
	logrus.WithFields(logrus.Fields(logCtx)).Infof("CancelDeductAgreement: %s", contractId)
}

// QueryOrder 查询订单状态
func (c *AlipayClient) QueryOrder(orderNo string, logCtx map[string]any) (bool, *time.Time, float64, string, error) {
	if c.client == nil {
		return false, nil, 0, "", errors.New("alipay client not initialized")
	}

	var p = alipay.TradeQuery{}
	p.OutTradeNo = orderNo

	rsp, err := c.client.TradeQuery(context.Background(), p)
	if err != nil {
		logrus.WithFields(logrus.Fields(logCtx)).Errorf("Query order failed: orderNo=%s, err=%v", orderNo, err)
		return false, nil, 0, "", err
	}

	if !rsp.IsSuccess() {
		return false, nil, 0, "", fmt.Errorf("query order failed: %s - %s", rsp.Code, rsp.Msg)
	}

	success := rsp.TradeStatus == "TRADE_SUCCESS" || rsp.TradeStatus == "TRADE_FINISHED"

	var payTime *time.Time
	if rsp.SendPayDate != "" {
		t, err := time.ParseInLocation("2006-01-02 15:04:05", rsp.SendPayDate, time.Local)
		if err == nil {
			payTime = &t
		}
	}

	var amount float64
	if rsp.TotalAmount != "" {
		amount, _ = strconv.ParseFloat(rsp.TotalAmount, 64)
	}

	return success, payTime, amount, rsp.TradeNo, nil
}

// CloseOrder 关闭订单
func (c *AlipayClient) CloseOrder(orderNo string, logCtx map[string]any) error {
	if c.client == nil {
		return errors.New("alipay client not initialized")
	}

	var p = alipay.TradeClose{}
	p.OutTradeNo = orderNo

	rsp, err := c.client.TradeClose(context.Background(), p)
	if err != nil {
		logrus.WithFields(logrus.Fields(logCtx)).Errorf("Close order failed: orderNo=%s, err=%v", orderNo, err)
		return err
	}

	if !rsp.IsSuccess() {
		return fmt.Errorf("close order failed: %s - %s", rsp.Code, rsp.Msg)
	}

	return nil
}

// VerifyNotify 验证支付宝回调签名
func (c *AlipayClient) VerifyNotify(notifyData map[string]string, logCtx map[string]any) bool {
	if c.client == nil {
		logrus.WithFields(logrus.Fields(logCtx)).Errorf("Alipay client not initialized")
		return false
	}

	values := url.Values{}
	for k, v := range notifyData {
		values.Set(k, v)
	}

	err := c.client.VerifySign(context.Background(), values)
	if err != nil {
		logrus.WithFields(logrus.Fields(logCtx)).Errorf("Verify notify sign failed: %v", err)
		return false
	}

	return true
}

// ParseNotify 解析支付宝回调数据
func (c *AlipayClient) ParseNotify(r *http.Request) (map[string]string, error) {
	err := r.ParseForm()
	if err != nil {
		return nil, err
	}

	result := make(map[string]string)
	for k, v := range r.Form {
		if len(v) > 0 {
			result[k] = v[0]
		}
	}

	return result, nil
}

// ParseNotification 用 SDK 解析通知（新方法，可选）
func (c *AlipayClient) ParseNotification(r *http.Request, logCtx map[string]any) (*alipay.Notification, error) {
	if c.client == nil {
		return nil, errors.New("alipay client not initialized")
	}

	noti, err := c.client.DecodeNotification(context.Background(), r.Form)
	if err != nil {
		logrus.WithFields(logrus.Fields(logCtx)).Errorf("Decode notify failed: %v", err)
		return nil, err
	}

	return noti, nil
}

// ACKNotify 向支付宝确认通知已收到（新方法，可选）
func (c *AlipayClient) ACKNotify(w http.ResponseWriter) {
	alipay.ACKNotification(w)
}

// NotificationToMap 将 Notification 结构体转换为 map[string]string
// 通过 JSON 序列化/反序列化实现，空值字段会被忽略
func (c *AlipayClient) NotificationToMap(n *alipay.Notification) map[string]string {
	if n == nil {
		return nil
	}

	b, err := json.Marshal(n)
	if err != nil {
		return nil
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil
	}

	result := make(map[string]string, len(raw))
	for k, v := range raw {
		if v == nil {
			continue
		}
		s := fmt.Sprintf("%v", v)
		if s != "" && s != "<nil>" && s != "0" && s != "%!s(alipay.TradeStatus=)" {
			result[k] = s
		}
	}

	result["trade_status"] = string(n.TradeStatus)

	return result
}
