package pay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	wechatV3CombineJsapiURL    = "https://api.mch.weixin.qq.com/v3/combine-transactions/jsapi"
	wechatV3CombineCloseURL    = "https://api.mch.weixin.qq.com/v3/combine-transactions/out-trade-no/%s/close"
	wechatV3CombineQueryURL    = "https://api.mch.weixin.qq.com/v3/combine-transactions/out-trade-no/%s"
	wechatCombineOutTradeNoMin = 6
	wechatCombineOutTradeNoMax = 32
	wechatCombineSubOrderMin   = 2
	wechatCombineSubOrderMax   = 50
)

// MiniWechatCombineSubOrder 合单中的一笔子单。微信要求 Attach 必填
type MiniWechatCombineSubOrder struct {
	OutTradeNo  string // 子单商户单号
	Description string // 商品描述
	AmountCent  int64  // 金额（分）
	Attach      string // 附加数据，回调原样返回
}

// MiniWechatCombineJsapiParam 微信合单 JSAPI 下单。PayerClientIP 为空时使用 127.0.0.1
type MiniWechatCombineJsapiParam struct {
	CombineOutTradeNo string // 合单商户单号，6 到 32 个字符
	OpenId            string
	PayerClientIP     string
	TimeExpire        time.Time // 为零时不传 time_expire
	NotifyUrl         string    // 合单回调地址，必填；不能回落到单笔回调地址
	SubOrders         []MiniWechatCombineSubOrder
}

// CreateCombineJsapi 合单 JSAPI 下单，返回 prepay_id。不改变单笔 CreateJsapiOrder
func (c *MiniWechatPayClient) CreateCombineJsapi(ctx context.Context, param MiniWechatCombineJsapiParam) (string, error) {
	no, subs, err := normalizeWechatCombine(param.CombineOutTradeNo, param.SubOrders)
	if err != nil {
		return "", err
	}
	openID := strings.TrimSpace(param.OpenId)
	if openID == "" {
		return "", errors.New("wechat combine openid is empty")
	}
	notifyURL := strings.TrimSpace(param.NotifyUrl)
	if notifyURL == "" {
		return "", errors.New("wechat combine notify_url is empty")
	}
	req := wechatCombineJsapiRequest{
		CombineAppID:      c.client.AppId,
		CombineMchID:      c.client.MchId,
		CombineOutTradeNo: no,
		SceneInfo:         wechatCombineScene{PayerClientIP: wechatPayerIP(param.PayerClientIP)},
		SubOrders:         wechatCombineSubs(c.client.MchId, subs),
		CombinePayerInfo:  wechatCombinePayer{OpenID: openID},
		NotifyURL:         notifyURL,
	}
	if !param.TimeExpire.IsZero() {
		req.TimeExpire = param.TimeExpire.Format(time.RFC3339)
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	resp, err := c.client.doRequestWithContext(ctx, http.MethodPost, wechatV3CombineJsapiURL, body)
	if err != nil {
		return "", err
	}
	var result WechatV3AppOrderResponse
	if err = json.Unmarshal(resp, &result); err != nil {
		return "", err
	}
	if result.PrepayId == "" {
		return "", errors.New("wechat combine prepay_id is empty")
	}
	return result.PrepayId, nil
}

// CloseCombine 关闭未支付合单。订单不存在或已关闭视为成功。不改变单笔 CloseOrder
func (c *MiniWechatPayClient) CloseCombine(ctx context.Context, combineOutTradeNo string, subOutTradeNos []string) error {
	no := strings.TrimSpace(combineOutTradeNo)
	if len(no) < wechatCombineOutTradeNoMin || len(no) > wechatCombineOutTradeNoMax {
		return fmt.Errorf("wechat combine out trade no length invalid: %d", len(no))
	}
	if len(subOutTradeNos) == 0 {
		return errors.New("wechat combine close sub orders are empty")
	}
	subs := make([]wechatCombineCloseSub, 0, len(subOutTradeNos))
	for _, outTradeNo := range subOutTradeNos {
		outTradeNo = strings.TrimSpace(outTradeNo)
		if outTradeNo == "" {
			return errors.New("wechat combine close sub out trade no is empty")
		}
		subs = append(subs, wechatCombineCloseSub{MchID: c.client.MchId, OutTradeNo: outTradeNo})
	}
	body, err := json.Marshal(wechatCombineCloseRequest{CombineAppID: c.client.AppId, SubOrders: subs})
	if err != nil {
		return err
	}
	apiURL := fmt.Sprintf(wechatV3CombineCloseURL, url.PathEscape(no))
	_, err = c.client.doRequestWithContext(ctx, http.MethodPost, apiURL, body)
	if wechatCombineAlreadyClosed(err) {
		return nil
	}
	return err
}

// MiniWechatCombineNotifySub 合单回调中的一条子单
type MiniWechatCombineNotifySub struct {
	MchId         string
	OutTradeNo    string
	TransactionId string
	TradeState    string
	AmountCent    int64
	SuccessTime   time.Time
}

// MiniWechatCombineNotify 验签解密后的微信合单通知。非支付成功事件只填充 EventType
type MiniWechatCombineNotify struct {
	EventType         string
	AppId             string
	MchId             string
	CombineOutTradeNo string
	SubOrders         []MiniWechatCombineNotifySub
	Raw               string
}

// ParseCombineNotify 校验合单回调签名并解密。不改变单笔 ParseNotify 对未知事件返回错误的行为
func (c *MiniWechatPayClient) ParseCombineNotify(header http.Header, body []byte) (*MiniWechatCombineNotify, error) {
	if err := c.client.verifyNotifySignature(header, body); err != nil {
		return nil, fmt.Errorf("wechat combine notify verify failed: %w", err)
	}
	var envelope WechatV3NotifyRequest
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode wechat combine notify failed: %w", err)
	}
	if envelope.EventType != WechatNotifyTransactionSuccess {
		return &MiniWechatCombineNotify{EventType: envelope.EventType}, nil
	}
	plain, err := c.client.decryptNotifyResource(envelope.Resource)
	if err != nil {
		return nil, fmt.Errorf("decrypt wechat combine notify failed: %w", err)
	}
	result, err := decodeWechatCombineTrade([]byte(plain))
	if err != nil {
		return nil, fmt.Errorf("decode wechat combine resource failed: %w", err)
	}
	return &MiniWechatCombineNotify{
		EventType:         envelope.EventType,
		AppId:             result.AppId,
		MchId:             result.MchId,
		CombineOutTradeNo: result.CombineOutTradeNo,
		SubOrders:         result.SubOrders,
		Raw:               plain,
	}, nil
}

// MiniWechatCombineQueryResult 合单查单结果。子单 TradeState 与单笔查单含义一致
type MiniWechatCombineQueryResult struct {
	AppId             string
	MchId             string
	CombineOutTradeNo string
	SubOrders         []MiniWechatCombineNotifySub
}

// QueryCombine 按合单商户单号查单。合单子单必须走该接口，单笔 QueryOrder 查不到子单
func (c *MiniWechatPayClient) QueryCombine(ctx context.Context, combineOutTradeNo string) (*MiniWechatCombineQueryResult, error) {
	no := strings.TrimSpace(combineOutTradeNo)
	if len(no) < wechatCombineOutTradeNoMin || len(no) > wechatCombineOutTradeNoMax {
		return nil, fmt.Errorf("wechat combine out trade no length invalid: %d", len(no))
	}
	resp, err := c.client.doRequestWithContext(ctx, http.MethodGet, fmt.Sprintf(wechatV3CombineQueryURL, url.PathEscape(no)), nil)
	if err != nil {
		return nil, err
	}
	return decodeWechatCombineTrade(resp)
}

func decodeWechatCombineTrade(data []byte) (*MiniWechatCombineQueryResult, error) {
	var payload struct {
		CombineAppID      string `json:"combine_appid"`
		CombineMchID      string `json:"combine_mchid"`
		CombineOutTradeNo string `json:"combine_out_trade_no"`
		SubOrders         []struct {
			MchID         string `json:"mchid"`
			TradeState    string `json:"trade_state"`
			SuccessTime   string `json:"success_time"`
			TransactionID string `json:"transaction_id"`
			OutTradeNo    string `json:"out_trade_no"`
			Amount        struct {
				TotalAmount int64 `json:"total_amount"`
			} `json:"amount"`
		} `json:"sub_orders"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	result := &MiniWechatCombineQueryResult{
		AppId:             payload.CombineAppID,
		MchId:             payload.CombineMchID,
		CombineOutTradeNo: payload.CombineOutTradeNo,
		SubOrders:         make([]MiniWechatCombineNotifySub, 0, len(payload.SubOrders)),
	}
	for _, sub := range payload.SubOrders {
		var paidAt time.Time
		if sub.SuccessTime != "" {
			t, err := time.Parse(time.RFC3339, sub.SuccessTime)
			if err != nil {
				return nil, fmt.Errorf("invalid combine sub success_time %q: %w", sub.SuccessTime, err)
			}
			paidAt = t
		}
		amount := int64(0)
		if sub.Amount.TotalAmount > 0 {
			amount = sub.Amount.TotalAmount
		}
		result.SubOrders = append(result.SubOrders, MiniWechatCombineNotifySub{
			MchId:         sub.MchID,
			OutTradeNo:    sub.OutTradeNo,
			TransactionId: sub.TransactionID,
			TradeState:    sub.TradeState,
			AmountCent:    amount,
			SuccessTime:   paidAt,
		})
	}
	return result, nil
}

func normalizeWechatCombine(combineOutTradeNo string, orders []MiniWechatCombineSubOrder) (string, []MiniWechatCombineSubOrder, error) {
	no := strings.TrimSpace(combineOutTradeNo)
	if len(no) < wechatCombineOutTradeNoMin || len(no) > wechatCombineOutTradeNoMax {
		return "", nil, fmt.Errorf("wechat combine out trade no length invalid: %d", len(no))
	}
	if len(orders) < wechatCombineSubOrderMin || len(orders) > wechatCombineSubOrderMax {
		return "", nil, fmt.Errorf("wechat combine sub order count invalid: %d", len(orders))
	}
	subs := make([]MiniWechatCombineSubOrder, len(orders))
	seen := make(map[string]bool, len(orders))
	for i, order := range orders {
		order.OutTradeNo = strings.TrimSpace(order.OutTradeNo)
		order.Description = strings.TrimSpace(order.Description)
		order.Attach = strings.TrimSpace(order.Attach)
		if order.OutTradeNo == "" || order.Description == "" || order.Attach == "" || order.AmountCent <= 0 {
			return "", nil, errors.New("wechat combine sub order is incomplete")
		}
		if seen[order.OutTradeNo] {
			return "", nil, fmt.Errorf("wechat combine sub out trade no duplicated: %s", order.OutTradeNo)
		}
		seen[order.OutTradeNo] = true
		subs[i] = order
	}
	return no, subs, nil
}

func wechatCombineSubs(mchID string, orders []MiniWechatCombineSubOrder) []wechatCombineSub {
	subs := make([]wechatCombineSub, 0, len(orders))
	for _, order := range orders {
		subs = append(subs, wechatCombineSub{
			MchID:       mchID,
			Attach:      order.Attach,
			Amount:      wechatCombineMoney{TotalAmount: order.AmountCent, Currency: "CNY"},
			OutTradeNo:  order.OutTradeNo,
			Description: order.Description,
		})
	}
	return subs
}

func wechatPayerIP(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return "127.0.0.1"
	}
	return ip
}

func wechatCombineAlreadyClosed(err error) bool {
	var apiErr *WechatV3APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.Code {
	case "ORDER_NOT_EXIST", "ORDER_CLOSED", "RESOURCE_NOT_EXISTS":
		return true
	default:
		return false
	}
}

type wechatCombineJsapiRequest struct {
	CombineAppID      string             `json:"combine_appid"`
	CombineMchID      string             `json:"combine_mchid"`
	CombineOutTradeNo string             `json:"combine_out_trade_no"`
	SceneInfo         wechatCombineScene `json:"scene_info"`
	SubOrders         []wechatCombineSub `json:"sub_orders"`
	CombinePayerInfo  wechatCombinePayer `json:"combine_payer_info"`
	TimeExpire        string             `json:"time_expire,omitempty"`
	NotifyURL         string             `json:"notify_url"`
}

type wechatCombineScene struct {
	PayerClientIP string `json:"payer_client_ip"`
}

type wechatCombineSub struct {
	MchID       string             `json:"mchid"`
	Attach      string             `json:"attach,omitempty"`
	Amount      wechatCombineMoney `json:"amount"`
	OutTradeNo  string             `json:"out_trade_no"`
	Description string             `json:"description"`
}

type wechatCombineMoney struct {
	TotalAmount int64  `json:"total_amount"`
	Currency    string `json:"currency"`
}

type wechatCombinePayer struct {
	OpenID string `json:"openid"`
}

type wechatCombineCloseRequest struct {
	CombineAppID string                  `json:"combine_appid"`
	SubOrders    []wechatCombineCloseSub `json:"sub_orders"`
}

type wechatCombineCloseSub struct {
	MchID      string `json:"mchid"`
	OutTradeNo string `json:"out_trade_no"`
}
