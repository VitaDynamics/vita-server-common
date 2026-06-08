package pay

import (
	"bytes"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

const (
	WechatV3AppOrderURL     = "https://api.mch.weixin.qq.com/v3/pay/transactions/app"
	WechatV3QueryOrderByWx  = "https://api.mch.weixin.qq.com/v3/pay/transactions/id/%s?mchid=%s"
	WechatV3QueryOrderByMch = "https://api.mch.weixin.qq.com/v3/pay/transactions/out-trade-no/%s?mchid=%s"
	WechatV3CloseOrderURL   = "https://api.mch.weixin.qq.com/v3/pay/transactions/out-trade-no/%s/close"
)

type WechatClient struct {
	AppId           string
	MchId           string
	ApiKeyV3        string
	NotifyUrl       string
	PrivateKey      *rsa.PrivateKey
	WechatPublicKey *rsa.PublicKey
	SerialNo        string
	HttpClient      *http.Client
}

type GenWechatClientParam struct {
	AppId         string
	MchId         string
	ApiKeyV3      string
	NotifyUrl     string
	PrivateKeyPEM string
	LogCtx        map[string]any
}

func NewWechatClient(params GenWechatClientParam) *WechatClient {
	client := &WechatClient{
		AppId:     params.AppId,
		MchId:     params.MchId,
		ApiKeyV3:  params.ApiKeyV3,
		NotifyUrl: params.NotifyUrl,
		HttpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}

	if params.PrivateKeyPEM != "" {
		if err := client.loadPrivateKey(params.PrivateKeyPEM); err != nil {
			logrus.WithFields(logrus.Fields(params.LogCtx)).Errorf("load private key failed: %v", err)
			return nil
		}
	}

	return client
}

func (c *WechatClient) LoadPublicKey(publicKeyPEM string) error {
	block, _ := pem.Decode([]byte(publicKeyPEM))
	if block == nil {
		return errors.New("failed to parse public key PEM block")
	}

	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return fmt.Errorf("parse public key failed: %w", err)
	}

	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return errors.New("public key is not RSA")
	}

	c.WechatPublicKey = rsaPub
	return nil
}

func (c *WechatClient) loadPrivateKey(privateKeyPEM string) error {
	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return errors.New("failed to parse PEM block")
	}

	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return err
	}

	resKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return errors.New("private key is not RSA")
	}
	c.PrivateKey = resKey
	return nil
}

type WechatV3AppOrderRequest struct {
	AppId       string         `json:"appid"`
	MchId       string         `json:"mchid"`
	Description string         `json:"description"`
	OutTradeNo  string         `json:"out_trade_no"`
	TimeExpire  string         `json:"time_expire,omitempty"`
	NotifyUrl   string         `json:"notify_url"`
	Amount      WechatV3Amount `json:"amount"`
	Attach      string         `json:"attach,omitempty"`
}

type WechatV3Amount struct {
	Total    int    `json:"total"`
	Currency string `json:"currency,omitempty"`
}

type WechatV3AppOrderResponse struct {
	PrepayId string `json:"prepay_id"`
}

type WechatV3QueryOrderResponse struct {
	AppId          string                `json:"appid"`
	MchId          string                `json:"mchid"`
	OutTradeNo     string                `json:"out_trade_no"`
	TransactionId  string                `json:"transaction_id"`
	TradeType      string                `json:"trade_type"`
	TradeState     string                `json:"trade_state"`
	TradeStateDesc string                `json:"trade_state_desc"`
	BankType       string                `json:"bank_type,omitempty"`
	Attach         string                `json:"attach,omitempty"`
	SuccessTime    string                `json:"success_time,omitempty"`
	Payer          *WechatV3Payer        `json:"payer,omitempty"`
	Amount         *WechatV3AmountDetail `json:"amount,omitempty"`
}

type WechatV3Payer struct {
	OpenId string `json:"openid"`
}

type WechatV3AmountDetail struct {
	Total         int    `json:"total"`
	PayerTotal    int    `json:"payer_total,omitempty"`
	Currency      string `json:"currency"`
	PayerCurrency string `json:"payer_currency,omitempty"`
}

type WechatV3NotifyRequest struct {
	Id           string                 `json:"id"`
	CreateTime   string                 `json:"create_time"`
	EventType    string                 `json:"event_type"`
	ResourceType string                 `json:"resource_type"`
	Summary      string                 `json:"summary"`
	Resource     WechatV3NotifyResource `json:"resource"`
}

type WechatV3NotifyResource struct {
	OriginalType   string `json:"original_type"`
	Algorithm      string `json:"algorithm"`
	Ciphertext     string `json:"ciphertext"`
	AssociatedData string `json:"associated_data"`
	Nonce          string `json:"nonce"`
}

type CreateWechatPayOrderParams struct {
	OrderNo     string     `json:"orderNo"`
	Description string     `json:"description"`
	TotalFee    int        `json:"totalFee"`
	TimeExpire  *time.Time `json:"timeExpire,omitempty"`
	LogCtx      map[string]any
}

func (c *WechatClient) CreateAppOrder(params CreateWechatPayOrderParams) (*WechatV3AppOrderResponse, error) {
	req := WechatV3AppOrderRequest{
		AppId:       c.AppId,
		MchId:       c.MchId,
		Description: params.Description,
		OutTradeNo:  params.OrderNo,
		NotifyUrl:   c.NotifyUrl,
		Amount: WechatV3Amount{
			Total:    params.TotalFee,
			Currency: "CNY",
		},
	}

	if params.TimeExpire != nil {
		req.TimeExpire = params.TimeExpire.Format(time.RFC3339)
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	resp, err := c.doRequest("POST", WechatV3AppOrderURL, body)
	if err != nil {
		logrus.WithFields(logrus.Fields(params.LogCtx)).Errorf("create wechat app order failed: %v", err)
		return nil, err
	}

	var result WechatV3AppOrderResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

func (c *WechatClient) QueryOrderByTransactionId(transactionId string) (*WechatV3QueryOrderResponse, error) {
	apiUrl := fmt.Sprintf(WechatV3QueryOrderByWx, transactionId, c.MchId)

	resp, err := c.doRequest("GET", apiUrl, nil)
	if err != nil {
		return nil, err
	}

	var result WechatV3QueryOrderResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

func (c *WechatClient) QueryOrderByOutTradeNo(outTradeNo string) (*WechatV3QueryOrderResponse, error) {
	apiUrl := fmt.Sprintf(WechatV3QueryOrderByMch, outTradeNo, c.MchId)

	resp, err := c.doRequest("GET", apiUrl, nil)
	if err != nil {
		return nil, err
	}

	var result WechatV3QueryOrderResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

func (c *WechatClient) CloseOrder(outTradeNo string) error {
	apiUrl := fmt.Sprintf(WechatV3CloseOrderURL, outTradeNo)

	req := map[string]string{
		"mchid": c.MchId,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return err
	}

	_, err = c.doRequest("POST", apiUrl, body)
	return err
}

func (c *WechatClient) QueryOrder(orderNo string, logCtx map[string]any) (bool, *time.Time, float64, string, error) {
	result, err := c.QueryOrderByOutTradeNo(orderNo)
	if err != nil {
		return false, nil, 0, "", err
	}

	if result.TradeState == "SUCCESS" {
		payTime, err := time.Parse(time.RFC3339, result.SuccessTime)
		var payTimePtr *time.Time
		if err == nil {
			payTimePtr = &payTime
		} else {
			logrus.WithFields(logrus.Fields(logCtx)).Warnf("parse success_time failed: %v", err)
		}
		amount := 0.0
		if result.Amount != nil {
			amount = float64(result.Amount.Total) / 100.0
		}
		return true, payTimePtr, amount, result.TransactionId, nil
	}

	return false, nil, 0, "", nil
}

func (c *WechatClient) CreatePayParams(orderNo, packageType string, amount float64, logCtx map[string]any) map[string]string {
	description := fmt.Sprintf("服务包-%s", packageType)
	totalFee := int(math.Round(amount * 100))

	resp, err := c.CreateAppOrder(CreateWechatPayOrderParams{
		OrderNo:     orderNo,
		Description: description,
		TotalFee:    totalFee,
		LogCtx:      logCtx,
	})
	if err != nil {
		logrus.WithFields(logrus.Fields(logCtx)).Errorf("create wechat pay order failed: %v", err)
		return nil
	}

	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	nonceStr := generateNonceStr()

	prepayId := resp.PrepayId

	message := fmt.Sprintf("%s\n%s\n%s\n%s\n", c.AppId, timestamp, nonceStr, prepayId)

	signature, err := c.signSHA256(message)
	if err != nil {
		logrus.WithFields(logrus.Fields(logCtx)).Errorf("sign wechat pay message failed: %v", err)
		return nil
	}

	return map[string]string{
		"appid":     c.AppId,
		"partnerid": c.MchId,
		"prepayid":  prepayId,
		"package":   "Sign=WXPay",
		"noncestr":  nonceStr,
		"timestamp": timestamp,
		"sign":      signature,
	}
}

func (c *WechatClient) VerifyNotify(r *http.Request) error {
	wechatpayTimestamp := r.Header.Get("Wechatpay-Timestamp")
	wechatpayNonce := r.Header.Get("Wechatpay-Nonce")
	wechatpaySignature := r.Header.Get("Wechatpay-Signature")

	if wechatpayTimestamp == "" || wechatpayNonce == "" || wechatpaySignature == "" {
		return errors.New("missing Wechatpay headers for verification")
	}

	ts, err := strconv.ParseInt(wechatpayTimestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid Wechatpay-Timestamp: %w", err)
	}
	signedAt := time.Unix(ts, 0)
	now := time.Now()
	if signedAt.Before(now.Add(-5*time.Minute)) || signedAt.After(now.Add(5*time.Minute)) {
		return errors.New("stale Wechatpay-Timestamp")
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("read notify body failed: %w", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	if c.WechatPublicKey == nil {
		return errors.New("wechat public key not loaded, cannot verify signature")
	}

	message := fmt.Sprintf("%s\n%s\n%s\n", wechatpayTimestamp, wechatpayNonce, string(body))

	signatureBytes, err := base64.StdEncoding.DecodeString(wechatpaySignature)
	if err != nil {
		return fmt.Errorf("decode signature failed: %w", err)
	}

	hashed := sha256.Sum256([]byte(message))
	if err := rsa.VerifyPKCS1v15(c.WechatPublicKey, crypto.SHA256, hashed[:], signatureBytes); err != nil {
		return fmt.Errorf("verify wechat pay signature failed: %w", err)
	}

	return nil
}

func (c *WechatClient) ParseNotify(r *http.Request) (map[string]string, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()

	var notifyReq WechatV3NotifyRequest
	if err := json.Unmarshal(body, &notifyReq); err != nil {
		return nil, err
	}

	if notifyReq.EventType != "TRANSACTION.SUCCESS" {
		result := make(map[string]string)
		result["event_type"] = notifyReq.EventType
		result["trade_state"] = "NOTPAY"
		return result, nil
	}

	decryptedData, err := c.decryptNotifyResource(notifyReq.Resource)
	if err != nil {
		return nil, fmt.Errorf("decrypt notify resource failed: %w", err)
	}

	var orderData WechatV3QueryOrderResponse
	if err := json.Unmarshal([]byte(decryptedData), &orderData); err != nil {
		return nil, err
	}

	result := make(map[string]string)
	result["event_type"] = notifyReq.EventType
	result["out_trade_no"] = orderData.OutTradeNo
	result["transaction_id"] = orderData.TransactionId
	result["trade_state"] = orderData.TradeState
	result["success_time"] = orderData.SuccessTime
	if orderData.Amount != nil {
		result["total_fee"] = strconv.Itoa(orderData.Amount.Total)
	}
	if orderData.Payer != nil {
		result["openid"] = orderData.Payer.OpenId
	}

	return result, nil
}

func (c *WechatClient) decryptNotifyResource(resource WechatV3NotifyResource) (string, error) {
	if resource.Algorithm != "AEAD_AES_256_GCM" {
		return "", fmt.Errorf("unsupported algorithm: %s", resource.Algorithm)
	}

	key := []byte(c.ApiKeyV3)
	if len(key) != 32 {
		return "", fmt.Errorf("invalid ApiKeyV3 length: expected 32, got %d", len(key))
	}

	ciphertext, err := base64.StdEncoding.DecodeString(resource.Ciphertext)
	if err != nil {
		return "", fmt.Errorf("base64 decode ciphertext failed: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create aes cipher failed: %w", err)
	}

	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create aes-gcm failed: %w", err)
	}

	nonce := []byte(resource.Nonce)
	if len(nonce) != aesgcm.NonceSize() {
		return "", fmt.Errorf("invalid nonce length: expected %d, got %d", aesgcm.NonceSize(), len(nonce))
	}

	associatedData := []byte(resource.AssociatedData)

	plaintext, err := aesgcm.Open(nil, nonce, ciphertext, associatedData)
	if err != nil {
		return "", fmt.Errorf("aes-gcm decrypt failed: %w", err)
	}

	return string(plaintext), nil
}

func (c *WechatClient) doRequest(method, reqUrl string, body []byte) ([]byte, error) {
	if c.PrivateKey == nil {
		return nil, errors.New("private key not loaded, cannot sign request")
	}
	if c.SerialNo == "" {
		return nil, errors.New("serial_no not set, cannot build Authorization header")
	}

	u, err := url.Parse(reqUrl)
	if err != nil {
		return nil, err
	}

	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	nonceStr := generateNonceStr()

	var bodyStr string
	if body != nil {
		bodyStr = string(body)
	}

	signature, err := c.buildSignature(method, u.Path, u.RawQuery, timestamp, nonceStr, bodyStr)
	if err != nil {
		return nil, err
	}

	authorization := fmt.Sprintf(
		`WECHATPAY2-SHA256-RSA2048 mchid="%s",serial_no="%s",nonce_str="%s",timestamp="%s",signature="%s"`,
		c.MchId, c.SerialNo, nonceStr, timestamp, signature,
	)

	req, err := http.NewRequest(method, reqUrl, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", authorization)
	req.Header.Set("Accept", "application/json")
	if method != "GET" || body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "PandaX-WechatPay-V3/1.0")

	resp, err := c.HttpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("wechat api error (status %d): %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

func (c *WechatClient) buildSignature(method, path, query, timestamp, nonceStr, body string) (string, error) {
	var urlStr string
	if query != "" {
		urlStr = fmt.Sprintf("%s?%s", path, query)
	} else {
		urlStr = path
	}

	message := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n",
		method,
		urlStr,
		timestamp,
		nonceStr,
		body,
	)

	return c.signSHA256(message)
}

func (c *WechatClient) signSHA256(message string) (string, error) {
	if c.PrivateKey == nil {
		return "", errors.New("private key not loaded")
	}

	hashed := sha256.Sum256([]byte(message))
	signature, err := rsa.SignPKCS1v15(rand.Reader, c.PrivateKey, crypto.SHA256, hashed[:])
	if err != nil {
		return "", err
	}

	return base64.StdEncoding.EncodeToString(signature), nil
}

func GenerateOrderNo() string {
	return fmt.Sprintf("ORD%s%s", time.Now().Format("20060102150405"), uuid.NewString()[0:8])
}

func generateNonceStr() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func (c *WechatClient) SetSerialNo(serialNo string) {
	c.SerialNo = serialNo
}

type WechatV3Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *WechatV3Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}
