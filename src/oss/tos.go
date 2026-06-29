package oss

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos/enum"
)

// oss 上传配置
type TosConfig struct {
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	BucketName      string
	Region          string
	ObjectPrefix    string
	AccountId       string
	RoleName        string
}

func (m *TosConfig) CreateClient(logCtx map[string]interface{}) *tos.ClientV2 {
	endpoint := m.Endpoint
	accessKey := m.AccessKeyID
	secretKey := m.SecretAccessKey
	region := m.Region
	bucketName := m.BucketName
	// 初始化客户端
	client, err := tos.NewClientV2(endpoint, tos.WithRegion(region), tos.WithCredentials(tos.NewStaticCredentials(accessKey, secretKey)))
	if err != nil {
		logrus.WithFields(logCtx).Errorf("Failed to create TOS client: %v", err)
		return nil
	}
	// 查看存储桶元数据信息
	_, err = client.HeadBucket(context.Background(), &tos.HeadBucketInput{Bucket: bucketName})
	if err != nil {
		// 判断桶是否存在
		if serverErr, ok := err.(*tos.TosServerError); ok {
			if serverErr.StatusCode == http.StatusNotFound {
				// 存储桶不存在
				logrus.WithFields(logCtx).Errorf("Bucket %s does not exist", bucketName)
				return nil
			} else {
				logrus.WithFields(logCtx).Errorf("HeadBucket err: %v", err)
				return nil
			}
		} else {
			logrus.WithFields(logCtx).Errorf("HeadBucket err: %v", err)
			return nil
		}
	}
	return client
}

type TosStorage struct {
	client       *tos.ClientV2
	bucketName   string
	endpoint     string
	objectPrefix string
	accessKey    string
	secretKey    string
	region       string
	accountId    string
	roleName     string
}

func NewTos(c TosConfig, logCtx map[string]interface{}) Driver {
	client := c.CreateClient(logCtx)
	if client == nil {
		return nil
	}
	return &TosStorage{
		client:       client,
		bucketName:   c.BucketName,
		endpoint:     c.Endpoint,
		objectPrefix: c.ObjectPrefix,
		accessKey:    c.AccessKeyID,
		secretKey:    c.SecretAccessKey,
		region:       c.Region,
		accountId:    c.AccountId,
		roleName:     c.RoleName,
	}
}

// 获取objectName的前缀
func (m *TosStorage) GetObjectPrefix() string {
	return m.objectPrefix
}

// 获取UWB对象的前缀
func (c *TosStorage) GetUWBPrefix() string {
	return ""
}

func (c *TosStorage) GetHelpPrefix() string {
	return ""
}

// contentType 需要上传的文件类型 例如application/zip
func (m *TosStorage) Put(objectName, localFileName string) error {
	// 读取本地文件数据
	f, err := os.Open(localFileName)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = m.client.PutObjectV2(context.Background(), &tos.PutObjectV2Input{
		PutObjectBasicInput: tos.PutObjectBasicInput{
			Bucket: m.bucketName,
			Key:    objectName,
		},
		Content: f,
	})

	if err != nil {
		return err
	}
	return nil
}

func (m *TosStorage) PutObj(objectName string, file io.Reader) error {
	_, err := m.client.PutObjectV2(context.Background(), &tos.PutObjectV2Input{
		PutObjectBasicInput: tos.PutObjectBasicInput{
			Bucket: m.bucketName,
			Key:    objectName,
		},
		Content: file,
	})

	if err != nil {
		return err
	}
	return nil
}

func (m *TosStorage) Get(objectName, downloadedFileName string) error {
	_, err := m.client.GetObjectToFile(context.Background(), &tos.GetObjectToFileInput{
		GetObjectV2Input: tos.GetObjectV2Input{
			Bucket: m.bucketName,
			Key:    objectName,
		},
		FilePath: downloadedFileName,
	})

	if err != nil {
		return err
	}
	return nil
}

func (m *TosStorage) Del(objectName string) error {
	_, err := m.client.DeleteObjectV2(context.Background(), &tos.DeleteObjectV2Input{
		Bucket: m.bucketName,
		Key:    objectName,
	})
	return err
}

// HeadObjectSize 返回对象大小（字节）。对象不存在时返回 (0, nil)。
func (m *TosStorage) HeadObjectSize(objectName string) (int64, error) {
	out, err := m.client.HeadObjectV2(context.Background(), &tos.HeadObjectV2Input{
		Bucket: m.bucketName,
		Key:    objectName,
	})
	if err != nil {
		if serverErr, ok := err.(*tos.TosServerError); ok && serverErr.StatusCode == http.StatusNotFound {
			return 0, nil
		}
		return 0, err
	}
	return out.ContentLength, nil
}

// DeleteObjectsByPrefix 递归删除指定前缀下的所有对象，返回删除数量与最终错误。
// 使用 ListObjectsType2 分页 + DeleteMultiObjects 批量删除（每批最多 1000 个）。
func (m *TosStorage) DeleteObjectsByPrefix(prefix string) (int, error) {
	if prefix == "" {
		return 0, nil
	}
	ctx := context.Background()
	deleted := 0
	var continuationToken string
	for {
		out, err := m.client.ListObjectsType2(ctx, &tos.ListObjectsType2Input{
			Bucket:            m.bucketName,
			Prefix:            prefix,
			ContinuationToken: continuationToken,
			MaxKeys:           1000,
		})
		if err != nil {
			return deleted, fmt.Errorf("list objects with prefix %s failed: %w", prefix, err)
		}
		if len(out.Contents) > 0 {
			toDelete := make([]tos.ObjectTobeDeleted, 0, len(out.Contents))
			for _, item := range out.Contents {
				toDelete = append(toDelete, tos.ObjectTobeDeleted{Key: item.Key})
			}
			res, err := m.client.DeleteMultiObjects(ctx, &tos.DeleteMultiObjectsInput{
				Bucket:  m.bucketName,
				Objects: toDelete,
				Quiet:   true,
			})
			if err != nil {
				return deleted, fmt.Errorf("delete multi objects failed: %w", err)
			}
			if len(res.Error) > 0 {
				return deleted, fmt.Errorf("delete multi objects partial failed, first error: code=%s key=%s message=%s",
					res.Error[0].Code, res.Error[0].Key, res.Error[0].Message)
			}
			deleted += len(toDelete)
		}
		if !out.IsTruncated {
			break
		}
		continuationToken = out.NextContinuationToken
	}
	return deleted, nil
}

// PresignedUrlForBucketKey 为指定 bucket+key 生成预签名下载 URL；
// 当被预签名的对象与 TosStorage 默认 bucket 不同（如 mapping 模块独立 bucket）时可用。
func (m *TosStorage) PresignedUrlForBucketKey(bucket, objectName string, expires time.Duration) (string, error) {
	if bucket == "" {
		bucket = m.bucketName
	}
	url, err := m.client.PreSignedURL(&tos.PreSignedURLInput{
		HTTPMethod: enum.HttpMethodGet,
		Bucket:     bucket,
		Key:        objectName,
		Expires:    int64(expires.Seconds()),
	})
	if err != nil {
		return "", err
	}
	return url.SignedUrl, nil
}

func (m *TosStorage) PresignedUrl(objectName string, expires time.Duration) (string, error) {
	url, err := m.client.PreSignedURL(&tos.PreSignedURLInput{
		HTTPMethod: enum.HttpMethodGet,
		Bucket:     m.bucketName,
		Key:        objectName,
		Expires:    int64(expires.Seconds()),
	})
	if err != nil {
		return "", err
	}
	return url.SignedUrl, nil
}

func (m *TosStorage) ExtractObjectNameFromUrl(urlStr string) (string, error) {
	return ExtractObjectNameFromUrl(urlStr, m.bucketName)
}

func (m *TosStorage) GetPublicUrl(objectName string) (string, error) {
	endpoint := m.endpoint
	publicUrl := fmt.Sprintf("http://%s.%s/%s", m.bucketName, endpoint, objectName)
	return publicUrl, nil
}

// ReadObject reads object content directly using TOS SDK
func (m *TosStorage) ReadObject(objectName string) ([]byte, error) {
	result, err := m.client.GetObjectV2(context.Background(), &tos.GetObjectV2Input{
		Bucket: m.bucketName,
		Key:    objectName,
	})
	if err != nil {
		return nil, err
	}
	defer result.Content.Close()

	// Read all content from the object
	content, err := io.ReadAll(result.Content)
	if err != nil {
		return nil, err
	}

	return content, nil
}

// GetObjectETag returns the ETag of an object from metadata (efficient, no download needed)
// For simple uploads, ETag is the MD5 hash of the object
// For multipart uploads, ETag contains "-" and is NOT a valid MD5 hash
func (m *TosStorage) GetObjectETag(objectName string) (string, bool, error) {
	result, err := m.client.HeadObjectV2(context.Background(), &tos.HeadObjectV2Input{
		Bucket: m.bucketName,
		Key:    objectName,
	})
	if err != nil {
		return "", false, err
	}

	// ETag is usually quoted, remove quotes
	etag := strings.Trim(result.ETag, "\"")

	// Check if it's a multipart upload ETag (contains "-")
	// Multipart ETags are in format: md5hash-partcount (e.g., "abc123-5")
	isValidMD5 := !strings.Contains(etag, "-")

	return etag, isValidMD5, nil
}

// GetObjectMetaMD5 returns the user-defined MD5 from object metadata (x-oss-meta-md5)
func (m *TosStorage) GetObjectMetaMD5(objectName string) (string, bool, error) {
	result, err := m.client.HeadObjectV2(context.Background(), &tos.HeadObjectV2Input{
		Bucket: m.bucketName,
		Key:    objectName,
	})
	if err != nil {
		return "", false, err
	}

	if result.Meta == nil {
		return "", false, nil
	}

	if v, ok := result.Meta.Get("md5"); ok && v != "" {
		return v, true, nil
	}
	if v, ok := result.Meta.Get("x-oss-meta-md5"); ok && v != "" {
		return v, true, nil
	}

	return "", false, nil
}

// ListObjects lists objects with the given prefix (and optional suffix filter)
func (m *TosStorage) ListObjects(prefix string, suffix string) ([]string, error) {
	var objectNames []string
	ctx := context.Background()
	input := &tos.ListObjectsType2Input{
		Bucket:  m.bucketName,
		Prefix:  prefix,
		MaxKeys: 1000,
	}
	for {
		output, err := m.client.ListObjectsType2(ctx, input)
		if err != nil {
			return nil, err
		}
		for _, obj := range output.Contents {
			if suffix == "" || strings.HasSuffix(obj.Key, suffix) {
				objectNames = append(objectNames, obj.Key)
			}
		}
		if !output.IsTruncated {
			break
		}
		input.ContinuationToken = output.NextContinuationToken
	}
	return objectNames, nil
}

const (
	iso8601Layout = "20060102T150405Z"
	yyMMdd        = "20060102"
	emptySHA256   = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

func sign(key []byte, value string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(value))
	return h.Sum(nil)
}

func getSigningKey(key []byte, dateStamp string, regionName, serviceName string) []byte {
	kDate := sign(key, dateStamp)
	kRegion := sign(kDate, regionName)
	kService := sign(kRegion, serviceName)
	kSigning := sign(kService, "request")
	return kSigning
}

type SignHeaderInput struct {
	method    string
	service   string
	host      string
	region    string
	params    url.Values
	accessKey string
	secretKey string
}

func getSigningHeader(input SignHeaderInput) map[string]string {
	contentType := "application/x-www-form-urlencoded"
	accept := "application/json"
	t := time.Now().UTC()
	xdate := t.Format(iso8601Layout)
	datestamp := t.Format(yyMMdd)

	// *************  1: 拼接规范请求串*************
	canonicalUri := "/"
	canonicalQueryString := input.params.Encode()
	canonicalHeaders := "content-type:" + contentType + "\n" + "host:" + input.host + "\n" + "x-date:" + xdate + "\n"
	signedHeaders := "content-type;host;x-date"
	canonicalRequest := input.method + "\n" + canonicalUri + "\n" + canonicalQueryString + "\n" + canonicalHeaders + "\n" + signedHeaders + "\n" + emptySHA256

	// *************  2：拼接待签名字符串*************
	algorithm := "HMAC-SHA256"
	credentialScope := datestamp + "/" + input.region + "/" + input.service + "/" + "request"
	cr256 := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := algorithm + "\n" + xdate + "\n" + credentialScope + "\n" + hex.EncodeToString(cr256[:])

	// *************  3：计算签名 *************
	signingKey := getSigningKey([]byte(input.secretKey), datestamp, input.region, input.service)
	signature := sign(signingKey, stringToSign)

	// *************4：添加签名到请求header中 * ************
	authorizationHeader := algorithm + " " + "Credential=" + input.accessKey + "/" + credentialScope + ", " + "SignedHeaders=" + signedHeaders + ", " + "Signature=" + hex.EncodeToString(signature)

	headers := map[string]string{"Accept": accept, "Content-Type": contentType, "X-Date": xdate, "Authorization": authorizationHeader}
	return headers
}

type STSResponse struct {
	ResponseMetadata struct {
		RequestID string `json:"RequestId"`
	} `json:"ResponseMetadata"`
	Result struct {
		Credentials struct {
			AccessKeyId     string `json:"AccessKeyId"`
			SecretAccessKey string `json:"SecretAccessKey"`
			SessionToken    string `json:"SessionToken"`
			Expiration      string `json:"Expiration"`
		} `json:"Credentials"`
		AssumedRoleUser struct {
			AssumedRoleId string `json:"AssumedRoleId"`
			Arn           string `json:"Arn"`
		} `json:"AssumedRoleUser"`
	} `json:"Result"`
}

func (m *TosStorage) GetSTSToken() (map[string]string, error) {
	service := "sts"
	host := "open.volcengineapi.com"
	endpoint := "https://open.volcengineapi.com"
	region := m.region
	accessKey := m.accessKey
	secretKey := m.secretKey
	accountId := m.accountId
	roleName := m.roleName

	queryParams := url.Values{
		"Action":          []string{"AssumeRole"},
		"RoleSessionName": []string{"tos-sts-session"},
		"RoleTrn":         []string{"trn:iam::" + accountId + ":role/" + roleName},
		"Version":         []string{"2018-01-01"},
	}

	header := getSigningHeader(SignHeaderInput{
		method:    http.MethodGet,
		service:   service,
		host:      host,
		region:    region,
		params:    queryParams,
		accessKey: accessKey,
		secretKey: secretKey,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+queryParams.Encode(), nil)
	if err != nil {
		return nil, err
	}

	for key, value := range header {
		req.Header.Set(key, value)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GetSTSToken: unexpected status code %d: %s", resp.StatusCode, string(body))
	}

	var stsResp STSResponse
	if err := json.Unmarshal(body, &stsResp); err != nil {
		return nil, err
	}

	// 提取STS token信息
	tokenInfo := map[string]string{
		"AccessKeyId":     stsResp.Result.Credentials.AccessKeyId,
		"SecretAccessKey": stsResp.Result.Credentials.SecretAccessKey,
		"SessionToken":    stsResp.Result.Credentials.SessionToken,
		"Expiration":      stsResp.Result.Credentials.Expiration,
	}

	return tokenInfo, nil
}

func (m *TosStorage) GetObjectInfo(objectName string) (map[string]interface{}, error) {
	return nil, fmt.Errorf("GetObjectInfo not implemented for TosStorage")
}
