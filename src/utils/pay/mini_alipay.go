package pay

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/smartwalle/alipay/v3"
)

// 支付宝小程序支付销售产品码
const (
	AlipayProductCodeJsapiPay      = "JSAPI_PAY"
	AlipayProductCodePreAuthOnline = "PRE_AUTH_ONLINE"
)

// 支付宝异步通知类型
const (
	AlipayNotifyTypeTradeStatusSync  = "trade_status_sync"
	AlipayNotifyTypeFundAuthFreeze   = "fund_auth_freeze"
	AlipayNotifyTypeFundAuthUnfreeze = "fund_auth_unfreeze"
)

// AlipayNotifyAck 支付宝异步通知处理成功后必须返回的纯文本
const AlipayNotifyAck = "success"

// AlipaySubCodeTradeNotExist 交易不存在（查单 / 关单）
const AlipaySubCodeTradeNotExist = "ACQ.TRADE_NOT_EXIST"

const alipayTimeLayout = "2006-01-02 15:04:05"

// 支付宝接口时间按北京时间解析与传参
var alipayLocation = time.FixedZone("CST", 8*3600)

// MiniAlipayClient 支付宝小程序支付与资金授权客户端
type MiniAlipayClient struct {
	notifyUrl string
	client    *alipay.Client
}

type GenMiniAlipayClientParam struct {
	AppId      string        // 小程序 appid
	PrivateKey string        // 应用私钥
	PublicKey  string        // 支付宝公钥
	Production bool          // 是否生产网关
	NotifyUrl  string        // 默认回调地址，参数未指定时使用
	Timeout    time.Duration // 请求超时，默认 10 秒
}

func NewMiniAlipayClient(param GenMiniAlipayClientParam) (*MiniAlipayClient, error) {
	if param.AppId == "" || param.PrivateKey == "" || param.PublicKey == "" {
		return nil, errors.New("alipay mini app_id/private_key/public_key missing")
	}
	timeout := param.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	client, err := alipay.New(param.AppId, param.PrivateKey, param.Production,
		alipay.WithHTTPClient(&http.Client{Timeout: timeout}),
		alipay.WithTimeLocation(alipayLocation))
	if err != nil {
		return nil, fmt.Errorf("init alipay mini client failed: %w", err)
	}
	if err = client.LoadAliPayPublicKey(param.PublicKey); err != nil {
		return nil, fmt.Errorf("load alipay public key failed: %w", err)
	}
	return &MiniAlipayClient{notifyUrl: param.NotifyUrl, client: client}, nil
}

// AlipaySubCode 返回支付宝业务错误的 sub_code，非支付宝业务错误返回空
func AlipaySubCode(err error) string {
	var aliErr *alipay.Error
	if errors.As(err, &aliErr) {
		return aliErr.SubCode
	}
	return ""
}

type MiniAlipayTradeCreateParam struct {
	OutTradeNo  string     // 商户交易号
	Subject     string     // 订单标题
	AmountCent  int64      // 金额（分）
	BuyerOpenId string     // 买家 open_id，与 BuyerId 二选一
	BuyerId     string     // 买家 user_id（未开通 open_id 时）
	TimeExpire  *time.Time // 支付截止时间
	NotifyUrl   string     // 为空时使用客户端默认回调地址
}

// CreateTrade alipay.trade.create 小程序下单，返回 trade_no 供 my.tradePay 调起
func (c *MiniAlipayClient) CreateTrade(ctx context.Context, param MiniAlipayTradeCreateParam) (string, error) {
	if param.OutTradeNo == "" || param.AmountCent <= 0 || (param.BuyerOpenId == "" && param.BuyerId == "") {
		return "", errors.New("out_trade_no/amount/buyer invalid")
	}
	p := alipay.TradeCreate{
		Trade: alipay.Trade{
			NotifyURL:   c.notify(param.NotifyUrl),
			Subject:     param.Subject,
			OutTradeNo:  param.OutTradeNo,
			TotalAmount: CentToYuan(param.AmountCent),
			ProductCode: AlipayProductCodeJsapiPay,
		},
		BuyerOpenId: param.BuyerOpenId,
		BuyerId:     param.BuyerId,
	}
	if param.TimeExpire != nil {
		p.TimeExpire = param.TimeExpire.In(alipayLocation).Format(alipayTimeLayout)
	}
	rsp, err := c.client.TradeCreate(ctx, p)
	if err = checkAlipayRsp(rsp, err); err != nil {
		return "", err
	}
	if rsp.TradeNo == "" {
		return "", errors.New("alipay.trade.create trade_no is empty")
	}
	return rsp.TradeNo, nil
}

// MiniAlipayTradeQueryResult TradeStatus：WAIT_BUYER_PAY、TRADE_CLOSED、TRADE_SUCCESS、TRADE_FINISHED
type MiniAlipayTradeQueryResult struct {
	TradeNo         string
	OutTradeNo      string
	TradeStatus     string
	TotalAmountCent int64
	BuyerOpenId     string
	PayTime         *time.Time
}

// QueryTrade 按商户交易号查单；交易不存在时 AlipaySubCode(err) 为 ACQ.TRADE_NOT_EXIST
func (c *MiniAlipayClient) QueryTrade(ctx context.Context, outTradeNo string) (*MiniAlipayTradeQueryResult, error) {
	rsp, err := c.client.TradeQuery(ctx, alipay.TradeQuery{OutTradeNo: outTradeNo})
	if err = checkAlipayRsp(rsp, err); err != nil {
		return nil, err
	}
	amount, err := YuanToCent(rsp.TotalAmount)
	if err != nil {
		return nil, err
	}
	return &MiniAlipayTradeQueryResult{
		TradeNo:         rsp.TradeNo,
		OutTradeNo:      rsp.OutTradeNo,
		TradeStatus:     string(rsp.TradeStatus),
		TotalAmountCent: amount,
		BuyerOpenId:     rsp.BuyerOpenId,
		PayTime:         parseAlipayTime(rsp.SendPayDate),
	}, nil
}

// CloseTrade 关闭未支付交易；交易不存在时 AlipaySubCode(err) 为 ACQ.TRADE_NOT_EXIST
func (c *MiniAlipayClient) CloseTrade(ctx context.Context, outTradeNo string) error {
	rsp, err := c.client.TradeClose(ctx, alipay.TradeClose{OutTradeNo: outTradeNo})
	return checkAlipayRsp(rsp, err)
}

type MiniAlipayRefundParam struct {
	OutTradeNo   string // 原交易商户单号，与 TradeNo 二选一
	TradeNo      string // 原交易支付宝单号
	OutRequestNo string // 退款请求号，结果不明时必须复用
	RefundCent   int64  // 退款金额（分）
	Reason       string
}

type MiniAlipayRefundResult struct {
	TradeNo       string
	OutTradeNo    string
	FundChange    bool  // 本次退款是否发生资金变化，为 false 时需查询确认
	RefundFeeCent int64 // 该交易累计退款金额（分）
}

// Refund alipay.trade.refund 同步退款
func (c *MiniAlipayClient) Refund(ctx context.Context, param MiniAlipayRefundParam) (*MiniAlipayRefundResult, error) {
	if param.OutTradeNo == "" && param.TradeNo == "" {
		return nil, errors.New("out_trade_no and trade_no are both empty")
	}
	if param.OutRequestNo == "" || param.RefundCent <= 0 {
		return nil, errors.New("out_request_no/refund amount invalid")
	}
	rsp, err := c.client.TradeRefund(ctx, alipay.TradeRefund{
		OutTradeNo:   param.OutTradeNo,
		TradeNo:      param.TradeNo,
		RefundAmount: CentToYuan(param.RefundCent),
		RefundReason: param.Reason,
		OutRequestNo: param.OutRequestNo,
	})
	if err = checkAlipayRsp(rsp, err); err != nil {
		return nil, err
	}
	refundFee, err := YuanToCent(rsp.RefundFee)
	if err != nil {
		return nil, err
	}
	return &MiniAlipayRefundResult{
		TradeNo:       rsp.TradeNo,
		OutTradeNo:    rsp.OutTradeNo,
		FundChange:    rsp.FundChange == "Y",
		RefundFeeCent: refundFee,
	}, nil
}

type MiniAlipayRefundQueryResult struct {
	TradeNo          string
	OutTradeNo       string
	OutRequestNo     string
	Success          bool // refund_status=REFUND_SUCCESS；未返回表示退款未收到或失败
	RefundAmountCent int64
	RefundTime       *time.Time
}

// QueryRefund alipay.trade.fastpay.refund.query 查询退款
func (c *MiniAlipayClient) QueryRefund(ctx context.Context, outTradeNo, outRequestNo string) (*MiniAlipayRefundQueryResult, error) {
	rsp, err := c.client.TradeFastPayRefundQuery(ctx, alipay.TradeFastPayRefundQuery{
		OutTradeNo:   outTradeNo,
		OutRequestNo: outRequestNo,
	})
	if err = checkAlipayRsp(rsp, err); err != nil {
		return nil, err
	}
	amount, err := YuanToCent(rsp.RefundAmount)
	if err != nil {
		return nil, err
	}
	return &MiniAlipayRefundQueryResult{
		TradeNo:          rsp.TradeNo,
		OutTradeNo:       rsp.OutTradeNo,
		OutRequestNo:     rsp.OutRequestNo,
		Success:          rsp.RefundStatus == "REFUND_SUCCESS",
		RefundAmountCent: amount,
		RefundTime:       parseAlipayTime(rsp.GMTRefundPay),
	}, nil
}

type MiniAlipayFreezeParam struct {
	OutOrderNo   string        // 商户授权资金订单号
	OutRequestNo string        // 商户资金操作流水号
	OrderTitle   string        // 业务订单描述
	AmountCent   int64         // 冻结金额（分）
	PayTimeout   time.Duration // 授权截止时长，按分钟取整，为 0 时支付宝默认 15 分钟
	ExtraParam   string        // 业务扩展参数 JSON，如 {"category":"RENT_..."}，信用免押类目需与支付宝约定
	NotifyUrl    string        // 为空时使用客户端默认回调地址
}

// CreateFreezeOrderStr alipay.fund.auth.order.app.freeze 生成 orderStr，前端 my.tradePay({orderStr}) 调起授权
func (c *MiniAlipayClient) CreateFreezeOrderStr(param MiniAlipayFreezeParam) (string, error) {
	if param.OutOrderNo == "" || param.OutRequestNo == "" || param.AmountCent <= 0 {
		return "", errors.New("out_order_no/out_request_no/amount invalid")
	}
	p := alipay.FundAuthOrderAppFreeze{
		NotifyURL:    c.notify(param.NotifyUrl),
		OutOrderNo:   param.OutOrderNo,
		OutRequestNo: param.OutRequestNo,
		OrderTitle:   param.OrderTitle,
		Amount:       CentToYuan(param.AmountCent),
		ProductCode:  AlipayProductCodePreAuthOnline,
		ExtraParam:   param.ExtraParam,
	}
	if minutes := int64(param.PayTimeout / time.Minute); minutes > 0 {
		p.PayTimeout = fmt.Sprintf("%dm", minutes)
	}
	return c.client.FundAuthOrderAppFreeze(p)
}

type MiniAlipayUnfreezeParam struct {
	AuthNo       string // 冻结成功返回的支付宝授权订单号
	OutRequestNo string // 商户资金操作流水号，结果不明时必须复用
	AmountCent   int64  // 解冻金额（分）
	Remark       string
	ExtraParam   string // 解冻扩展信息 JSON，如 {"unfreezeBizInfo":"{\"bizComplete\":\"true\"}"}
	NotifyUrl    string // 为空时使用客户端默认回调地址
}

// MiniAlipayFundOperationResult 资金授权操作结果，Status：INIT、SUCCESS、CLOSED
type MiniAlipayFundOperationResult struct {
	AuthNo         string
	OutOrderNo     string
	OperationId    string
	OutRequestNo   string
	OperationType  string // 仅查询返回：FREEZE、UNFREEZE、PAY
	Status         string
	AmountCent     int64
	OrderStatus    string // 仅查询返回：INIT、AUTHORIZED、FINISH、CLOSED
	RestAmountCent int64  // 仅查询返回：剩余冻结金额（分）
	PayerUserId    string // 仅查询返回
	TransTime      *time.Time
}

// Unfreeze alipay.fund.auth.order.unfreeze 解冻
func (c *MiniAlipayClient) Unfreeze(ctx context.Context, param MiniAlipayUnfreezeParam) (*MiniAlipayFundOperationResult, error) {
	if param.AuthNo == "" || param.OutRequestNo == "" || param.AmountCent <= 0 {
		return nil, errors.New("auth_no/out_request_no/amount invalid")
	}
	rsp, err := c.client.FundAuthOrderUnfreeze(ctx, alipay.FundAuthOrderUnfreeze{
		NotifyURL:    c.notify(param.NotifyUrl),
		AuthNo:       param.AuthNo,
		OutRequestNo: param.OutRequestNo,
		Amount:       CentToYuan(param.AmountCent),
		Remark:       param.Remark,
		ExtraParam:   param.ExtraParam,
	})
	if err = checkAlipayRsp(rsp, err); err != nil {
		return nil, err
	}
	amount, err := YuanToCent(rsp.Amount)
	if err != nil {
		return nil, err
	}
	return &MiniAlipayFundOperationResult{
		AuthNo:       rsp.AuthNo,
		OutOrderNo:   rsp.OutOrderNo,
		OperationId:  rsp.OperationId,
		OutRequestNo: rsp.OutRequestNo,
		Status:       rsp.Status,
		AmountCent:   amount,
		TransTime:    parseAlipayTime(rsp.GMTTrans),
	}, nil
}

// QueryFundOperation alipay.fund.auth.operation.detail.query 按商户订单号 + 流水号查询冻结 / 解冻结果
func (c *MiniAlipayClient) QueryFundOperation(ctx context.Context, outOrderNo, outRequestNo string) (*MiniAlipayFundOperationResult, error) {
	rsp, err := c.client.FundAuthOperationDetailQuery(ctx, alipay.FundAuthOperationDetailQuery{
		OutOrderNo:   outOrderNo,
		OutRequestNo: outRequestNo,
	})
	if err = checkAlipayRsp(rsp, err); err != nil {
		return nil, err
	}
	amount, err := YuanToCent(rsp.Amount)
	if err != nil {
		return nil, err
	}
	rest, err := YuanToCent(rsp.RestAmount)
	if err != nil {
		return nil, err
	}
	return &MiniAlipayFundOperationResult{
		AuthNo:         rsp.AuthNo,
		OutOrderNo:     rsp.OutOrderNo,
		OperationId:    rsp.OperationId,
		OutRequestNo:   rsp.OutRequestNo,
		OperationType:  rsp.OperationType,
		Status:         rsp.Status,
		AmountCent:     amount,
		OrderStatus:    string(rsp.OrderStatus),
		RestAmountCent: rest,
		PayerUserId:    rsp.PayerUserId,
		TransTime:      parseAlipayTime(rsp.GMTTrans),
	}, nil
}

// MiniAlipayNotify 验签后的异步通知；交易通知填充交易字段，资金授权通知填充授权字段
type MiniAlipayNotify struct {
	NotifyType string
	NotifyId   string
	AppId      string

	// 交易（trade_status_sync）
	TradeNo         string
	OutTradeNo      string
	TradeStatus     string
	TotalAmountCent int64
	RefundFeeCent   int64
	BuyerOpenId     string
	PayTime         *time.Time

	// 资金授权（fund_auth_freeze / fund_auth_unfreeze）
	AuthNo        string
	OutOrderNo    string
	OperationId   string
	OutRequestNo  string
	OperationType string
	Status        string
	AmountCent    int64
	TransTime     *time.Time

	Values url.Values // 原始通知参数
}

// ParseNotify 校验支付宝异步通知签名并解析，处理成功后需向支付宝返回 AlipayNotifyAck
func (c *MiniAlipayClient) ParseNotify(ctx context.Context, values url.Values) (*MiniAlipayNotify, error) {
	if err := c.client.VerifySign(ctx, values); err != nil {
		return nil, fmt.Errorf("verify alipay notify sign failed: %w", err)
	}
	notify := &MiniAlipayNotify{
		NotifyType:    values.Get("notify_type"),
		NotifyId:      values.Get("notify_id"),
		AppId:         values.Get("app_id"),
		TradeNo:       values.Get("trade_no"),
		OutTradeNo:    values.Get("out_trade_no"),
		TradeStatus:   values.Get("trade_status"),
		BuyerOpenId:   values.Get("buyer_open_id"),
		PayTime:       parseAlipayTime(values.Get("gmt_payment")),
		AuthNo:        values.Get("auth_no"),
		OutOrderNo:    values.Get("out_order_no"),
		OperationId:   values.Get("operation_id"),
		OutRequestNo:  values.Get("out_request_no"),
		OperationType: values.Get("operation_type"),
		Status:        values.Get("status"),
		TransTime:     parseAlipayTime(values.Get("gmt_trans")),
		Values:        values,
	}
	var err error
	if notify.TotalAmountCent, err = YuanToCent(values.Get("total_amount")); err != nil {
		return nil, err
	}
	if notify.RefundFeeCent, err = YuanToCent(values.Get("refund_fee")); err != nil {
		return nil, err
	}
	if notify.AmountCent, err = YuanToCent(values.Get("amount")); err != nil {
		return nil, err
	}
	return notify, nil
}

func (c *MiniAlipayClient) notify(override string) string {
	if override != "" {
		return override
	}
	return c.notifyUrl
}

// checkAlipayRsp SDK 仅在 error_response 时返回 err，业务失败（code≠10000）需另行判断
func checkAlipayRsp(rsp any, err error) error {
	if err != nil {
		return err
	}
	aliErr := alipayRspError(rsp)
	if aliErr == nil {
		return errors.New("alipay empty response")
	}
	if !aliErr.IsSuccess() {
		return aliErr
	}
	return nil
}

func alipayRspError(rsp any) *alipay.Error {
	switch r := rsp.(type) {
	case *alipay.TradeCreateRsp:
		if r != nil {
			return &r.Error
		}
	case *alipay.TradeQueryRsp:
		if r != nil {
			return &r.Error
		}
	case *alipay.TradeCloseRsp:
		if r != nil {
			return &r.Error
		}
	case *alipay.TradeRefundRsp:
		if r != nil {
			return &r.Error
		}
	case *alipay.TradeFastPayRefundQueryRsp:
		if r != nil {
			return &r.Error
		}
	case *alipay.FundAuthOrderUnfreezeRsp:
		if r != nil {
			return &r.Error
		}
	case *alipay.FundAuthOperationDetailQueryRsp:
		if r != nil {
			return &r.Error
		}
	}
	return nil
}

// CentToYuan 分转元字符串，保留两位小数
func CentToYuan(cent int64) string {
	sign := ""
	if cent < 0 {
		sign, cent = "-", -cent
	}
	return fmt.Sprintf("%s%d.%02d", sign, cent/100, cent%100)
}

// YuanToCent 元字符串转分，空字符串返回 0，超过两位小数视为错误
func YuanToCent(yuan string) (int64, error) {
	yuan = strings.TrimSpace(yuan)
	if yuan == "" {
		return 0, nil
	}
	intPart, fracPart, _ := strings.Cut(yuan, ".")
	if len(fracPart) > 2 {
		return 0, fmt.Errorf("invalid yuan amount: %s", yuan)
	}
	fracPart += strings.Repeat("0", 2-len(fracPart))
	cent, err := strconv.ParseInt(intPart+fracPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid yuan amount: %s", yuan)
	}
	return cent, nil
}

func parseAlipayTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.ParseInLocation(alipayTimeLayout, s, alipayLocation)
	if err != nil {
		return nil
	}
	return &t
}
