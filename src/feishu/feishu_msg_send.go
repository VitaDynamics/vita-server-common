package feishu

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// FeishuBotConfig 机器人配置
type FeishuBotConfig struct {
	WebhookUrl string // webhook地址
	SignKey    string // 签名密钥，不开启签名填空
	Timeout    time.Duration
	RetryTimes int // 失败重试次数
}

// FeishuBot 飞书机器人实例
type FeishuBot struct {
	cfg    FeishuBotConfig
	client *http.Client
}

// 飞书通用返回结构
type feishuResp struct {
	Code    int    `json:"code"`
	Message string `json:"msg"`
}

// TextContent 文本消息内容
type TextContent struct {
	Text string `json:"text"`
}

// MarkdownContent Markdown消息内容
type MarkdownContent struct {
	Text string `json:"text"`
}

// BaseReq 带签名基础请求体
type BaseReq struct {
	Timestamp int64       `json:"timestamp,omitempty"`
	Sign      string      `json:"sign,omitempty"`
	MsgType   string      `json:"msg_type"`
	Content   interface{} `json:"content,omitempty"`
	Card      interface{} `json:"card,omitempty"` // 卡片专用
}

// NewFeishuBot 新建机器人实例
func NewFeishuBot(cfg FeishuBotConfig) *FeishuBot {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.RetryTimes < 0 {
		cfg.RetryTimes = 2
	}
	return &FeishuBot{
		cfg: cfg,
		client: &http.Client{
			Timeout: cfg.Timeout,
		},
	}
}

// genSign 生成签名
func (b *FeishuBot) genSign(ts int64) string {
	if b.cfg.SignKey == "" {
		return ""
	}
	//src := fmt.Sprintf("%d\n%s", ts, b.cfg.SignKey)
	stringToSign := fmt.Sprintf("%v", ts) + "\n" + b.cfg.SignKey

	var data []byte
	h := hmac.New(sha256.New, []byte(stringToSign))
	h.Write(data)
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// SendText 发送纯文本消息
func (b *FeishuBot) SendText(text string) error {
	req := BaseReq{
		MsgType: "text",
		Content: TextContent{Text: text},
	}
	return b.sendRequest(req)
}

// SendMarkdown 发送Markdown富文本消息
func (b *FeishuBot) SendMarkdown(mdText string) error {
	req := BaseReq{
		MsgType: "markdown",
		Content: MarkdownContent{Text: mdText},
	}
	return b.sendRequest(req)
}

// SendCard 发送交互式卡片消息
func (b *FeishuBot) SendCard(cardData interface{}) error {
	req := BaseReq{
		MsgType: "interactive",
		Card:    cardData,
	}
	return b.sendRequest(req)
}

// sendRequest 底层发送+重试逻辑
func (b *FeishuBot) sendRequest(req BaseReq) error {
	var lastErr error
	for i := 0; i <= b.cfg.RetryTimes; i++ {
		// 有签名则填充时间戳和签名
		if b.cfg.SignKey != "" {
			ts := time.Now().Unix()
			req.Timestamp = ts
			req.Sign = b.genSign(ts)
		}

		bodyBuf, err := json.Marshal(req)
		if err != nil {
			return fmt.Errorf("json marshal failed: %w", err)
		}

		httpReq, err := http.NewRequest(http.MethodPost, b.cfg.WebhookUrl, bytes.NewBuffer(bodyBuf))
		if err != nil {
			lastErr = err
			time.Sleep(500 * time.Millisecond)
			continue
		}
		httpReq.Header.Set("Content-Type", "application/json")

		resp, err := b.client.Do(httpReq)
		if err != nil {
			lastErr = err
			time.Sleep(500 * time.Millisecond)
			continue
		}

		// 解析飞书返回
		var ret feishuResp
		decoder := json.NewDecoder(resp.Body)
		_ = decoder.Decode(&ret)
		_ = resp.Body.Close()

		if ret.Code == 0 {
			return nil // 发送成功
		}
		lastErr = fmt.Errorf("feishu api error, code:%d msg:%s", ret.Code, ret.Message)
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("send feishu msg failed after %d retries, last err: %w", b.cfg.RetryTimes, lastErr)
}
