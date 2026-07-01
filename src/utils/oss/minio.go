package oss

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/pkg/errors"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// oss 上传配置
type MinioConfig struct {
	Endpoint        string
	ServerIp        string
	AccessKey       string
	SecretAccessKey string
	UseSSL          bool
	BucketName      string
}

// NewMinioClient 创建并返回一个新的MinioClient实例
func (m *MinioConfig) CreateClient() *minio.Client {
	// 初始化客户端
	client, err := minio.New(m.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(m.AccessKey, m.SecretAccessKey, ""),
		Secure: m.UseSSL,
	})
	if err != nil {
		return nil
	}

	return client
}

type MinioStorage struct {
	client     *minio.Client
	bucketName string
	ServerIp   string
}

func NewMinio(c MinioConfig) Driver {
	client := c.CreateClient()
	if client == nil {
		return nil
	}
	return &MinioStorage{
		client:     client,
		bucketName: c.BucketName,
		ServerIp:   c.ServerIp,
	}
}

// 获取objectName的前缀, minio不需要objectPrefix
func (m *MinioStorage) GetObjectPrefix() string {
	return ""
}

// 获取UWB对象的前缀
func (c *MinioStorage) GetUWBPrefix() string {
	return ""
}

func (c *MinioStorage) GetHelpPrefix() string {
	return ""
}

// contentType 需要上传的文件类型 例如application/zip
func (m *MinioStorage) Put(objectName, localFileName string) error {
	// 打开文件
	f, err := os.Open(localFileName)
	if err != nil {
		return err
	}
	defer f.Close()

	// 上传文件
	_, err = m.client.FPutObject(context.Background(), m.bucketName, objectName, localFileName, minio.PutObjectOptions{})
	return err
}

func (m *MinioStorage) PutObj(objectName string, file io.Reader) error {
	_, err := m.client.PutObject(context.Background(), m.bucketName, objectName, file, -1, minio.PutObjectOptions{})
	if err != nil {
		log.Fatalln(err)
		return err
	}
	return nil
}

func (m *MinioStorage) Get(objectName, downloadedFileName string) error {
	return m.client.FGetObject(context.Background(), m.bucketName, objectName, downloadedFileName, minio.GetObjectOptions{})
}

func (m *MinioStorage) Del(objectName string) error {
	return m.client.RemoveObject(context.Background(), m.bucketName, objectName, minio.RemoveObjectOptions{})
}

func (m *MinioStorage) PresignedUrl(objectName string, expires time.Duration) (string, error) {
	presignedURL, err := m.client.PresignedGetObject(context.Background(), m.bucketName, objectName, expires, nil)
	if err != nil {
		return "", err
	}
	return presignedURL.String(), nil
}

func (m *MinioStorage) GetPublicUrl(objectName string) (string, error) {
	// Get the endpoint from the client
	endpoint := m.client.EndpointURL()

	// Construct public URL: http(s)://endpoint/bucketname/objectname
	// This assumes the bucket has public read permissions
	publicURL := fmt.Sprintf("%s://%s/%s/%s",
		endpoint.Scheme,
		m.ServerIp,
		m.bucketName,
		strings.TrimPrefix(objectName, "/"))

	return publicURL, nil
}

// ExtractObjectNameFromUrl extracts the object name from a URL
func (m *MinioStorage) ExtractObjectNameFromUrl(urlStr string) (string, error) {
	return ExtractObjectNameFromUrl(urlStr, m.bucketName)
}

// ReadObject reads object content directly using MinIO SDK
func (m *MinioStorage) ReadObject(objectName string) ([]byte, error) {
	object, err := m.client.GetObject(context.Background(), m.bucketName, objectName, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer object.Close()

	// Read all content from the object
	content, err := io.ReadAll(object)
	if err != nil {
		return nil, err
	}

	return content, nil
}

// GetObjectETag returns the ETag of an object from metadata (efficient, no download needed)
// For simple uploads, ETag is the MD5 hash of the object
// For multipart uploads, ETag contains "-" and is NOT a valid MD5 hash
func (m *MinioStorage) GetObjectETag(objectName string) (string, bool, error) {
	stat, err := m.client.StatObject(context.Background(), m.bucketName, objectName, minio.StatObjectOptions{})
	if err != nil {
		return "", false, err
	}

	// ETag is usually quoted, remove quotes
	etag := strings.Trim(stat.ETag, "\"")

	// Check if it's a multipart upload ETag (contains "-")
	// Multipart ETags are in format: md5hash-partcount (e.g., "abc123-5")
	isValidMD5 := !strings.Contains(etag, "-")

	return etag, isValidMD5, nil
}

// GetObjectMetaMD5 returns the user-defined MD5 from object metadata (x-oss-meta-md5)
func (m *MinioStorage) GetObjectMetaMD5(objectName string) (string, bool, error) {
	stat, err := m.client.StatObject(context.Background(), m.bucketName, objectName, minio.StatObjectOptions{})
	if err != nil {
		return "", false, err
	}

	// Prefer user metadata (x-amz-meta-*)
	if stat.UserMetadata != nil {
		if v, ok := stat.UserMetadata["md5"]; ok && v != "" {
			return v, true, nil
		}
		if v, ok := stat.UserMetadata["x-oss-meta-md5"]; ok && v != "" {
			return v, true, nil
		}
	}

	// Fallback to raw metadata headers
	if stat.Metadata != nil {
		if v := stat.Metadata.Get("X-Amz-Meta-Md5"); v != "" {
			return v, true, nil
		}
		if v := stat.Metadata.Get("X-Oss-Meta-Md5"); v != "" {
			return v, true, nil
		}
	}

	return "", false, nil
}

// ListObjects lists objects with the given prefix and optional suffix filter
func (m *MinioStorage) ListObjects(prefix string, suffix string) ([]string, error) {
	objectNames := []string{}
	ctx := context.Background()
	objectCh := m.client.ListObjects(ctx, m.bucketName, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	})
	for object := range objectCh {
		if object.Err != nil {
			return nil, object.Err
		}
		if suffix == "" || strings.HasSuffix(object.Key, suffix) {
			objectNames = append(objectNames, object.Key)
		}
	}
	return objectNames, nil
}

func (m *MinioStorage) GetSTSToken() (map[string]string, error) {
	// STS is not currently supported for the MinIO driver.
	return nil, errors.New("STS not supported for MinIO driver")
}

func (m *MinioStorage) GetObjectInfo(objectName string) (map[string]interface{}, error) {
	return nil, errors.New("GetObjectInfo not implemented")
}
