package pay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/smartwalle/alipay/v3"
)

const (
	// AlipayMergeBizType 支付宝小程序 my.tradePay 调起合单时的 bizType
	AlipayMergeBizType = "merge_order"
	// AlipayNotifyTypeMergePay 支付宝同事务合单异步通知类型
	AlipayNotifyTypeMergePay = "merge_pay"
	// AlipayMergePayStatusFinished 合单支付完成
	AlipayMergePayStatusFinished = "FINISHED"

	alipayMergeSubOrderMin = 2
)

// MiniAlipayMergeSubOrder 合并支付中的一笔子单
type MiniAlipayMergeSubOrder struct {
	OutTradeNo string // 子单商户单号
	Subject    string // 订单标题
	AmountCent int64  // 金额（分）
}

// MiniAlipayMergeCreateParam 支付宝同事务合并下单 alipay.trade.merge.create
type MiniAlipayMergeCreateParam struct {
	OutMergeNo  string // 商户合单号
	BuyerOpenId string
	NotifyUrl   string // 合单回调地址，必填；不能回落到单笔回调地址
	SubOrders   []MiniAlipayMergeSubOrder
}

// CreateMergeTrade 合并下单，返回 merge_no。不改变单笔 CreateTrade
func (c *MiniAlipayClient) CreateMergeTrade(ctx context.Context, param MiniAlipayMergeCreateParam) (string, error) {
	outMergeNo := strings.TrimSpace(param.OutMergeNo)
	buyerOpenID := strings.TrimSpace(param.BuyerOpenId)
	notifyURL := strings.TrimSpace(param.NotifyUrl)
	if outMergeNo == "" || buyerOpenID == "" || notifyURL == "" || len(param.SubOrders) < alipayMergeSubOrderMin {
		return "", errors.New("alipay merge request is incomplete")
	}
	details := make([]miniAlipayMergeOrderDetail, 0, len(param.SubOrders))
	seen := make(map[string]bool, len(param.SubOrders))
	for _, order := range param.SubOrders {
		outTradeNo := strings.TrimSpace(order.OutTradeNo)
		subject := strings.TrimSpace(order.Subject)
		if outTradeNo == "" || subject == "" || order.AmountCent <= 0 {
			return "", errors.New("alipay merge sub order is incomplete")
		}
		if seen[outTradeNo] {
			return "", fmt.Errorf("alipay merge sub out trade no duplicated: %s", outTradeNo)
		}
		seen[outTradeNo] = true
		details = append(details, miniAlipayMergeOrderDetail{
			AppID:       c.appId,
			OutTradeNo:  outTradeNo,
			ProductCode: AlipayProductCodeJsapiPay,
			TotalAmount: CentToYuan(order.AmountCent),
			Subject:     subject,
			OpAppID:     c.appId,
		})
	}
	var rsp miniAlipayMergeCreateRsp
	err := c.client.Request(ctx, miniAlipayTradeMergeCreate{
		NotifyURL:    notifyURL,
		OutMergeNo:   outMergeNo,
		BuyerOpenID:  buyerOpenID,
		OrderDetails: details,
	}, &rsp)
	if err != nil {
		return "", err
	}
	if rsp.Code == "" {
		return "", errors.New("alipay empty response")
	}
	if !rsp.IsSuccess() {
		return "", &rsp.Error
	}
	if rsp.MergeNo == "" {
		return "", errors.New("alipay merge_no is empty")
	}
	return rsp.MergeNo, nil
}

// MiniAlipayMergeNotifySub 合单通知中的一条子单
type MiniAlipayMergeNotifySub struct {
	AppId       string
	OutTradeNo  string
	TradeNo     string
	TradeStatus string
	AmountCent  int64
}

// MiniAlipayMergeNotify 验签后的支付宝同事务合单通知
type MiniAlipayMergeNotify struct {
	NotifyType     string
	AppId          string
	OutMergeNo     string
	MergePayStatus string
	NotifyTime     time.Time
	SubOrders      []MiniAlipayMergeNotifySub
	Raw            string
}

// ParseMergeNotify 校验支付宝合单通知签名并解析子单。不改变单笔 ParseNotify
func (c *MiniAlipayClient) ParseMergeNotify(ctx context.Context, values url.Values) (*MiniAlipayMergeNotify, error) {
	if err := c.client.VerifySign(ctx, values); err != nil {
		return nil, fmt.Errorf("verify alipay merge notify sign failed: %w", err)
	}
	notify := &MiniAlipayMergeNotify{
		NotifyType:     values.Get("notify_type"),
		AppId:          values.Get("app_id"),
		OutMergeNo:     values.Get("out_merge_no"),
		MergePayStatus: values.Get("merge_pay_status"),
		Raw:            values.Encode(),
	}
	if t := parseAlipayTime(values.Get("notify_time")); t != nil {
		notify.NotifyTime = *t
	}
	if notify.NotifyType != AlipayNotifyTypeMergePay && notify.OutMergeNo == "" {
		return notify, nil
	}
	rawSubs := strings.TrimSpace(values.Get("order_detail_results"))
	if rawSubs == "" {
		return notify, nil
	}
	var details []struct {
		AppID       string `json:"app_id"`
		OutTradeNo  string `json:"out_trade_no"`
		TradeNo     string `json:"trade_no"`
		TradeStatus string `json:"trade_status"`
		TotalAmount string `json:"total_amount"`
	}
	if err := json.Unmarshal([]byte(rawSubs), &details); err != nil {
		return nil, fmt.Errorf("decode alipay merge order_detail_results failed: %w", err)
	}
	notify.SubOrders = make([]MiniAlipayMergeNotifySub, 0, len(details))
	for _, detail := range details {
		cent, err := YuanToCent(detail.TotalAmount)
		if err != nil {
			return nil, err
		}
		if cent < 0 {
			cent = 0
		}
		notify.SubOrders = append(notify.SubOrders, MiniAlipayMergeNotifySub{
			AppId:       detail.AppID,
			OutTradeNo:  detail.OutTradeNo,
			TradeNo:     detail.TradeNo,
			TradeStatus: detail.TradeStatus,
			AmountCent:  cent,
		})
	}
	return notify, nil
}

type miniAlipayMergeOrderDetail struct {
	AppID       string `json:"app_id"`
	OutTradeNo  string `json:"out_trade_no"`
	ProductCode string `json:"product_code"`
	TotalAmount string `json:"total_amount"`
	Subject     string `json:"subject"`
	OpAppID     string `json:"op_app_id"`
}

type miniAlipayTradeMergeCreate struct {
	alipay.AuxParam
	NotifyURL    string                       `json:"-"`
	OutMergeNo   string                       `json:"out_merge_no"`
	BuyerOpenID  string                       `json:"buyer_open_id"`
	OrderDetails []miniAlipayMergeOrderDetail `json:"order_details"`
}

func (p miniAlipayTradeMergeCreate) APIName() string { return "alipay.trade.merge.create" }

func (p miniAlipayTradeMergeCreate) Params() map[string]string {
	values := map[string]string{}
	if p.NotifyURL != "" {
		values["notify_url"] = p.NotifyURL
	}
	return values
}

type miniAlipayMergeCreateRsp struct {
	alipay.Error
	MergeNo    string `json:"merge_no"`
	OutMergeNo string `json:"out_merge_no"`
}
