package oss

import (
	"io"
	"time"
)

// Driver oss驱动接口定义
type Driver interface {

	//获取objectName的前缀
	GetObjectPrefix() string
	// 获取UWB对象的前缀
	GetUWBPrefix() string
	// 获取Help对象的前缀
	GetHelpPrefix() string
	//上传
	Put(objectName, localFileName string) error

	PutObj(objectName string, file io.Reader) error
	//下载
	Get(objectName, downloadedFileName string) error
	//删除
	Del(objectName string) error
	//预签名URL
	PresignedUrl(objectName string, expires time.Duration) (string, error)
	//获取公共读URL
	GetPublicUrl(objectName string) (string, error)

	//从URL提取对象名
	ExtractObjectNameFromUrl(urlStr string) (string, error)

	//直接读取对象内容
	ReadObject(objectName string) ([]byte, error)

	// GetObjectETag returns the ETag (MD5 hash for simple uploads) of an object
	// This is more efficient than downloading the entire file to calculate MD5
	// Note: For multipart uploads, ETag is NOT the MD5 hash (contains "-" suffix)
	// Returns the ETag string and a boolean indicating if it's a valid MD5 hash
	GetObjectETag(objectName string) (etag string, isValidMD5 bool, err error)

	// GetObjectMetaMD5 returns the user-defined MD5 from object metadata (x-oss-meta-md5)
	// Returns the MD5 string and a boolean indicating if it exists
	GetObjectMetaMD5(objectName string) (md5 string, ok bool, err error)

	// ListObjects lists objects with the given prefix (and optional suffix filter)
	ListObjects(prefix string, suffix string) ([]string, error)

	// GetSTSToken returns temporary security credentials
	GetSTSToken() (map[string]string, error)

	// Get Object info (size, last modified time, etc.)
	GetObjectInfo(objectName string) (map[string]interface{}, error)
}
