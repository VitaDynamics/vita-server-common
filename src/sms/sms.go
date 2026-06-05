package sms

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aliyun/alibaba-cloud-sdk-go/services/dysmsapi"
	"github.com/redis/go-redis/v9"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	smsClient "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/sms/v20210111"
)

type SMSConfig struct {
	Provider        string         `yaml:"provider"` // aliyun | tencent | volcano
	AccessKey       string         `yaml:"access_key"`
	AccessSecret    string         `yaml:"access_secret"`
	SignName        string         `yaml:"sign_name"`
	TemplateCode    string         `yaml:"template_code"`
	SendRateLimit   int            `yaml:"send_rate_limit"`
	CooldownSeconds int            `yaml:"cooldown_seconds"`
	Aliyun          *AliyunConfig  `yaml:"aliyun"`
	Tencent         *TencentConfig `yaml:"tencent"`
	Volcano         *VolcanoConfig `yaml:"volcano"`
}

type AliyunConfig struct {
	AccessKeyId     string `yaml:"access_key_id"`
	AccessKeySecret string `yaml:"access_key_secret"`
	SignName        string `yaml:"sign_name"`
	TemplateCode    string `yaml:"template_code"`
}

type TencentConfig struct {
	SecretId   string `yaml:"secret_id"`
	SecretKey  string `yaml:"secret_key"`
	SdkAppId   string `yaml:"sdk_app_id"`
	TemplateId string `yaml:"template_id"`
	SignName   string `yaml:"sign_name"`
}

type VolcanoConfig struct {
	AccessKeyId     string `yaml:"access_key_id"`
	SecretAccessKey string `yaml:"secret_access_key"`
	Region          string `yaml:"region"`
	SmsAccount      string `yaml:"sms_account"`
	SignName        string `yaml:"sign_name"`
	TemplateId      string `yaml:"template_id"`
}

// 验证码有效期 10 分钟
const codeExpiration = 10 * time.Minute

// 默认短信发送冷却时间
const defaultCooldown = 60 * time.Second

func cooldownKey(phone string) string {
	return fmt.Sprintf("sms:cooldown:%s", phone)
}

func formatCooldownMessage(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("验证码已发送，请等待 %d 秒后重试", int(d.Seconds()))
	}
	return fmt.Sprintf("验证码已发送，请等待 %d 分钟后重试", int(d.Minutes()))
}

func SendSMS(phone string, params *SMSConfig, redisClient *redis.Client) error {
	// 检查是否处于发送冷却期
	cooldown := defaultCooldown
	if params != nil && params.CooldownSeconds > 0 {
		cooldown = time.Duration(params.CooldownSeconds) * time.Second
	}

	exists, err := redisClient.Exists(context.Background(), cooldownKey(phone)).Result()
	if err != nil {
		return fmt.Errorf("检查 Redis 失败: %w", err)
	}
	if exists > 0 {
		return fmt.Errorf(formatCooldownMessage(cooldown))
	}

	code := generateCode()
	conf := params
	// 将验证码存入 Redis 并设置过期时间
	err = redisClient.Set(context.Background(), phone, code, codeExpiration).Err()
	if err != nil {
		return fmt.Errorf("存储验证码到 Redis 失败: %w", err)
	}
	// 设置发送冷却 key
	err = redisClient.Set(context.Background(), cooldownKey(phone), "1", cooldown).Err()
	if err != nil {
		return fmt.Errorf("存储请求冷却时间到 Redis 失败: %w", err)
	}

	switch conf.Provider {
	case "aliyun":
		return sendAliyunSMS(phone, code, conf)
	case "tencent":
		return sendTencentSMS(phone, code, conf)
	case "volcano":
		return sendVolcanoSMS(phone, code, conf)
	default:
		return fmt.Errorf("不支持的短信服务商: %s", conf.Provider)
	}
}

func generateCode() string {
	return strconv.Itoa(rand.Intn(900000) + 100000)
}

func sendAliyunSMS(phone, code string, conf *SMSConfig) error {
	client, err := dysmsapi.NewClientWithAccessKey("default", conf.Aliyun.AccessKeyId, conf.Aliyun.AccessKeySecret)
	if err != nil {
		return err
	}

	request := dysmsapi.CreateSendSmsRequest()
	request.Scheme = "https"
	request.PhoneNumbers = phone
	request.SignName = conf.Aliyun.SignName
	request.TemplateCode = conf.Aliyun.TemplateCode
	request.TemplateParam = fmt.Sprintf(`{"code":"%s"}`, code)

	response, err := client.SendSms(request)
	if err != nil {
		return err
	}
	if response.Code != "OK" {
		return fmt.Errorf("发送短信失败: %s", response.Message)
	}
	return nil
}

func sendTencentSMS(phone, code string, conf *SMSConfig) error {
	cred := common.NewCredential(
		conf.Tencent.SecretId,
		conf.Tencent.SecretKey,
	)
	cpf := profile.NewClientProfile()
	cpf.HttpProfile.Endpoint = "sms.tencentcloudapi.com"
	client, err := smsClient.NewClient(cred, "ap-guangzhou", cpf)
	if err != nil {
		return err
	}

	request := smsClient.NewSendSmsRequest()
	request.PhoneNumberSet = common.StringPtrs([]string{phone})
	request.SmsSdkAppId = common.StringPtr(conf.Tencent.SdkAppId)
	request.SignName = common.StringPtr(conf.Tencent.SignName)
	request.TemplateId = common.StringPtr(conf.Tencent.TemplateId)
	request.TemplateParamSet = common.StringPtrs([]string{code})

	_, err = client.SendSms(request)
	if err != nil {
		return err
	}
	return nil
}

// VolcanoSMSRequest represents the request structure for Volcano Engine SMS API
type VolcanoSMSRequest struct {
	SmsAccount    string `json:"SmsAccount"`
	Sign          string `json:"Sign"`
	TemplateID    string `json:"TemplateID"`
	TemplateParam string `json:"TemplateParam"`
	PhoneNumbers  string `json:"PhoneNumbers"`
}

// VolcanoSMSResponse represents the response structure from Volcano Engine SMS API
type VolcanoSMSResponse struct {
	ResponseMetadata struct {
		RequestId string `json:"RequestId"`
		Action    string `json:"Action"`
		Version   string `json:"Version"`
		Service   string `json:"Service"`
		Region    string `json:"Region"`
		Error     *struct {
			Code    string `json:"Code"`
			Message string `json:"Message"`
		} `json:"Error"`
	} `json:"ResponseMetadata"`
	Result struct {
		MessageID []string `json:"MessageID"`
	} `json:"Result"`
}

func sendVolcanoSMS(phone, code string, conf *SMSConfig) error {
	// Prepare request parameters for JSON format
	// Create the template parameter as a JSON string (not object)
	templateParamJSON := fmt.Sprintf(`{"code":"%s"}`, code)

	// Create the request body structure
	requestBody := map[string]interface{}{
		"SmsAccount":    conf.Volcano.SmsAccount,
		"Sign":          conf.Volcano.SignName,
		"TemplateID":    conf.Volcano.TemplateId,
		"TemplateParam": templateParamJSON,
		"PhoneNumbers":  phone,
	}

	// Query parameters
	queryParams := map[string]string{
		"Action":  "SendSms",
		"Version": "2020-01-01",
	}

	// Create HTTP request with JSON format
	endpoint := "https://sms.volcengineapi.com"
	req, err := createVolcanoRequest("POST", endpoint, queryParams, requestBody, conf.Volcano)
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}

	// Send request
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("发送请求失败: %w", err)
	}
	defer resp.Body.Close()

	// Read response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("读取响应失败: %w", err)
	}

	// Parse response
	var volcanoResp VolcanoSMSResponse
	if err := json.Unmarshal(body, &volcanoResp); err != nil {
		return fmt.Errorf("解析响应失败: %w", err)
	}

	// Check for errors
	if volcanoResp.ResponseMetadata.Error != nil {
		return fmt.Errorf("火山引擎SMS错误: %s - %s",
			volcanoResp.ResponseMetadata.Error.Code,
			volcanoResp.ResponseMetadata.Error.Message)
	}

	return nil
}

// createVolcanoRequest creates an HTTP request for Volcano Engine SMS API using JSON format
func createVolcanoRequest(method, endpoint string, queryParams map[string]string, requestBody map[string]interface{}, conf *VolcanoConfig) (*http.Request, error) {
	now := time.Now()
	date := now.UTC().Format("20060102T150405Z")
	authDate := date[:8]

	// Create query string
	var queryParts []string
	for k, v := range queryParams {
		queryParts = append(queryParts, fmt.Sprintf("%s=%s", url.QueryEscape(k), url.QueryEscape(v)))
	}
	sort.Strings(queryParts)
	queryString := strings.Join(queryParts, "&")

	// Marshal request body to JSON
	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("JSON marshal failed: %w", err)
	}
	bodyString := string(bodyBytes)

	// Create request
	var req *http.Request
	if method == "GET" {
		req, err = http.NewRequest(method, endpoint+"?"+queryString, nil)
	} else {
		// POST request: query params in URL, JSON body
		req, err = http.NewRequest(method, endpoint+"?"+queryString, bytes.NewReader(bodyBytes))
	}

	if err != nil {
		return nil, err
	}

	// Add standard headers
	req.Header.Set("X-Date", date)
	req.Header.Set("Host", req.URL.Host)

	// Generate AWS Signature V4 authorization header for JSON
	authorization, err := generateVolcanoAuthHeader(req, bodyString, date, authDate, conf)
	if err != nil {
		return nil, fmt.Errorf("生成授权头失败: %w", err)
	}
	req.Header.Set("Authorization", authorization)

	return req, nil
}

// generateVolcanoAuthHeader generates Volcano Engine authentication header for JSON requests
func generateVolcanoAuthHeader(req *http.Request, body, date, authDate string, conf *VolcanoConfig) (string, error) {
	service := "volcSMS"
	region := conf.Region
	algorithm := "HMAC-SHA256"

	// Step 1: Hash the payload and add required headers
	hasher := sha256.New()
	hasher.Write([]byte(body))
	payloadHash := hex.EncodeToString(hasher.Sum(nil))

	// Add required headers for JSON
	req.Header.Set("X-Content-Sha256", payloadHash)
	req.Header.Set("Content-Type", "application/json")

	// Step 2: Create canonical request
	canonicalURI := req.URL.Path
	if canonicalURI == "" {
		canonicalURI = "/"
	}

	// Fix query string encoding (replace + with %20)
	canonicalQueryString := strings.Replace(req.URL.RawQuery, "+", "%20", -1)

	// Create canonical headers (must be in alphabetical order)
	signedHeaders := []string{"content-type", "host", "x-content-sha256", "x-date"}
	var headerList []string
	for _, header := range signedHeaders {
		if header == "host" {
			headerList = append(headerList, header+":"+req.Host)
		} else {
			v := req.Header.Get(header)
			headerList = append(headerList, header+":"+strings.TrimSpace(v))
		}
	}
	headerString := strings.Join(headerList, "\n")

	// Create canonical request
	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI,
		canonicalQueryString,
		headerString + "\n",
		strings.Join(signedHeaders, ";"),
		payloadHash,
	}, "\n")

	// Step 3: Create string to sign
	hasher = sha256.New()
	hasher.Write([]byte(canonicalRequest))
	canonicalRequestHash := hex.EncodeToString(hasher.Sum(nil))

	credentialScope := fmt.Sprintf("%s/%s/%s/request", authDate, region, service)
	stringToSign := strings.Join([]string{
		algorithm,
		date,
		credentialScope,
		canonicalRequestHash,
	}, "\n")

	// Step 4: Calculate signature
	signature, err := calculateVolcanoSignature(stringToSign, authDate, region, service, conf.SecretAccessKey)
	if err != nil {
		return "", err
	}

	// Step 5: Create authorization header
	authorization := fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		algorithm, conf.AccessKeyId, credentialScope, strings.Join(signedHeaders, ";"), signature)

	return authorization, nil
}

// calculateVolcanoSignature calculates Volcano Engine signature
func calculateVolcanoSignature(stringToSign, dateStamp, region, service, secretKey string) (string, error) {
	// Volcano Engine signature calculation
	kDate := hmacSHA256([]byte(secretKey), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	kSigning := hmacSHA256(kService, "request")

	signature := hmacSHA256(kSigning, stringToSign)
	return hex.EncodeToString(signature), nil
}

// hmacSHA256 computes HMAC-SHA256
func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}
