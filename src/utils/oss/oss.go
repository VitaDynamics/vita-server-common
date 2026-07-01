package oss

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/pkg/errors"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss/credentials"
)

// oss 上传配置
type AliConfig struct {
	AccessKey    string `json:"access_key"`
	SecretKey    string `json:"secret_key"`
	Bucket       string `json:"bucket"`
	Endpoint     string `json:"endpoint"`
	Region       string `json:"region"`
	ObjectPrefix string `json:"object_prefix"`
	UWBPrefix    string `json:"uwb_prefix"`
	HelpPrefix   string `json:"help_prefix"`
}

// oss 上传客户端
type aliOss struct {
	client       *oss.Client
	bucket       string
	endpoint     string
	objectPrefix string
	uwbPrefix    string
	helpPrefix   string
}

func NewAliOss(c AliConfig) Driver {
	// Create credentials provider with static credentials
	credProvider := credentials.NewStaticCredentialsProvider(c.AccessKey, c.SecretKey)

	// Build OSS config
	cfg := oss.LoadDefaultConfig().
		WithCredentialsProvider(credProvider).
		WithEndpoint(c.Endpoint)

	// Set region if provided
	if c.Region != "" {
		cfg = cfg.WithRegion(c.Region)
	}

	// Create OSS client
	client := oss.NewClient(cfg)

	return &aliOss{
		client:       client,
		bucket:       c.Bucket,
		endpoint:     c.Endpoint,
		objectPrefix: c.ObjectPrefix,
		uwbPrefix:    c.UWBPrefix,
		helpPrefix:   c.HelpPrefix,
	}
}

// 获取objectName的前缀
func (c *aliOss) GetObjectPrefix() string {
	return c.objectPrefix
}

// 获取UWB对象的前缀
func (c *aliOss) GetUWBPrefix() string {
	return c.uwbPrefix
}

func (c *aliOss) GetHelpPrefix() string {
	return c.helpPrefix
}

// Put 上传本地文件
func (c *aliOss) Put(objectName string, localFileName string) error {
	request := &oss.PutObjectRequest{
		Bucket: oss.Ptr(c.bucket),
		Key:    oss.Ptr(objectName),
	}

	_, err := c.client.PutObjectFromFile(context.Background(), request, localFileName)
	if err != nil {
		return errors.Wrapf(err, "put oss file fail")
	}
	return nil
}

// PutObj 上传io.Reader
func (c *aliOss) PutObj(objectName string, file io.Reader) error {
	request := &oss.PutObjectRequest{
		Bucket: oss.Ptr(c.bucket),
		Key:    oss.Ptr(objectName),
		Body:   file,
	}

	_, err := c.client.PutObject(context.Background(), request)
	if err != nil {
		return errors.Wrapf(err, "put oss file fail")
	}
	return nil
}

// Get 下载到本地文件
func (c *aliOss) Get(objectName, downloadedFileName string) error {
	request := &oss.GetObjectRequest{
		Bucket: oss.Ptr(c.bucket),
		Key:    oss.Ptr(objectName),
	}

	_, err := c.client.GetObjectToFile(context.Background(), request, downloadedFileName)
	if err != nil {
		return errors.Wrapf(err, "get oss file fail")
	}
	return nil
}

// Del 删除
func (c *aliOss) Del(objectName string) error {
	request := &oss.DeleteObjectRequest{
		Bucket: oss.Ptr(c.bucket),
		Key:    oss.Ptr(objectName),
	}

	_, err := c.client.DeleteObject(context.Background(), request)
	if err != nil {
		return errors.Wrapf(err, "del oss file fail")
	}
	return nil
}

// PresignedUrl 生成预签名URL
func (c *aliOss) PresignedUrl(objectName string, expires time.Duration) (string, error) {
	request := &oss.GetObjectRequest{
		Bucket: oss.Ptr(c.bucket),
		Key:    oss.Ptr(objectName),
	}

	result, err := c.client.Presign(context.Background(), request, func(po *oss.PresignOptions) {
		po.Expires = expires
	})
	if err != nil {
		return "", err
	}
	return result.URL, nil
}

// GetPublicUrl 获取公共读URL
func (c *aliOss) GetPublicUrl(objectName string) (string, error) {
	publicUrl := fmt.Sprintf("https://%s.%s/%s", c.bucket, c.endpoint, objectName)
	return publicUrl, nil
}

// ExtractObjectNameFromUrl 从URL提取对象名
func (c *aliOss) ExtractObjectNameFromUrl(urlStr string) (string, error) {
	return ExtractObjectNameFromUrl(urlStr, c.bucket)
}

// ReadObject 直接读取对象内容
func (c *aliOss) ReadObject(objectName string) ([]byte, error) {
	request := &oss.GetObjectRequest{
		Bucket: oss.Ptr(c.bucket),
		Key:    oss.Ptr(objectName),
	}

	result, err := c.client.GetObject(context.Background(), request)
	if err != nil {
		return nil, errors.Wrapf(err, "get oss object fail")
	}
	defer result.Body.Close()

	// Read all content from the object
	content, err := io.ReadAll(result.Body)
	if err != nil {
		return nil, errors.Wrapf(err, "read oss object content fail")
	}

	return content, nil
}

// GetObjectETag returns the ETag of an object from metadata (efficient, no download needed)
// For simple uploads, ETag is the MD5 hash of the object
// For multipart uploads, ETag contains "-" and is NOT a valid MD5 hash
func (c *aliOss) GetObjectETag(objectName string) (string, bool, error) {
	request := &oss.HeadObjectRequest{
		Bucket: oss.Ptr(c.bucket),
		Key:    oss.Ptr(objectName),
	}

	result, err := c.client.HeadObject(context.Background(), request)
	if err != nil {
		return "", false, errors.Wrapf(err, "head oss object fail")
	}

	// ETag is usually quoted, remove quotes
	etag := ""
	if result.ETag != nil {
		etag = strings.Trim(*result.ETag, "\"")
	}

	// Check if it's a multipart upload ETag (contains "-")
	// Multipart ETags are in format: md5hash-partcount (e.g., "abc123-5")
	isValidMD5 := etag != "" && !strings.Contains(etag, "-")

	return etag, isValidMD5, nil
}

// GetObjectMetaMD5 returns the user-defined MD5 from object metadata (x-oss-meta-md5)
func (c *aliOss) GetObjectMetaMD5(objectName string) (string, bool, error) {
	request := &oss.HeadObjectRequest{
		Bucket: oss.Ptr(c.bucket),
		Key:    oss.Ptr(objectName),
	}

	result, err := c.client.HeadObject(context.Background(), request)
	if err != nil {
		return "", false, errors.Wrapf(err, "head oss object fail")
	}

	if result.Metadata == nil {
		return "", false, nil
	}

	for k, v := range result.Metadata {
		if strings.EqualFold(k, "md5") || strings.EqualFold(k, "x-oss-meta-md5") {
			if v != "" {
				return v, true, nil
			}
		}
	}

	return "", false, nil
}

// ListObjects lists objects with the given prefix (and optional suffix filter))
func (c *aliOss) ListObjects(prefix string, suffix string) ([]string, error) {
	request := &oss.ListObjectsV2Request{
		Bucket: oss.Ptr(c.bucket),
		Prefix: oss.Ptr(prefix),
	}

	p := c.client.NewListObjectsV2Paginator(request)
	var objectNames []string
	for p.HasNext() {
		page, err := p.NextPage(context.TODO())
		if err == nil {
			for _, obj := range page.Contents {
				key := oss.ToString(obj.Key)
				if suffix == "" || strings.HasSuffix(key, suffix) {
					objectNames = append(objectNames, key)
				}
			}
		} else {
			return nil, errors.Wrapf(err, "list oss objects fail")
		}
	}

	return objectNames, nil
}

func (c *aliOss) GetSTSToken() (map[string]string, error) {
	// STS is not currently supported for the Aliyun OSS driver.
	return nil, errors.New("STS not supported for Aliyun OSS driver")
}

func (c *aliOss) GetObjectInfo(objectName string) (map[string]interface{}, error) {
	request := &oss.HeadObjectRequest{
		Bucket: oss.Ptr(c.bucket),
		Key:    oss.Ptr(objectName),
	}

	result, err := c.client.HeadObject(context.Background(), request)
	if err != nil {
		return nil, errors.Wrapf(err, "head oss object fail")
	}

	info := map[string]interface{}{
		"Size":         result.ContentLength,
		"LastModified": result.LastModified,
		"ETag":         result.ETag,
	}
	return info, nil
}
