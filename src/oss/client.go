package oss

import (
	"sync"

	"github.com/sirupsen/logrus"
	// "vita-app/internal/model/global"
)

var (
	// Separate singleton instances for both providers
	minioClient  Driver
	aliOssClient Driver
	minioOnce    sync.Once
	aliOssOnce   sync.Once
)

type GenerateOssClientParams struct {
	Provider     string `yaml:"provider"` // "minio" or "tos" or "oss"
	Endpoint     string `yaml:"endpoint"`
	ObjectPrefix string `yaml:"objectPrefix"`
	UWBPrefix    string `yaml:"uwbPrefix"`  // For UWB-specific files
	HelpPrefix   string `yaml:"helpPrefix"` // For help.json files
	ServerIp     string `yaml:"serverIp"`   // for minio
	AccessKey    string `yaml:"accessKey"`
	SecretKey    string `yaml:"secretKey"`
	BucketName   string `yaml:"bucketName"`
	UseSSL       bool   `yaml:"useSSL"`
	Region       string `yaml:"region"` // For TOS
}

// GetMinioClient returns the singleton MinIO client instance
func GetMinioClient(params *GenerateOssClientParams) Driver {
	minioOnce.Do(func() {
		config := MinioConfig{
			Endpoint:        params.Endpoint,
			ServerIp:        params.ServerIp,
			AccessKey:       params.AccessKey,
			SecretAccessKey: params.SecretKey,
			UseSSL:          params.UseSSL,
			BucketName:      params.BucketName,
		}

		minioClient = NewMinio(config)
		if minioClient == nil {
			logrus.Fatal("Failed to initialize MinIO client")
		}
	})

	return minioClient
}

// GetAliOssClient returns the singleton Aliyun OSS client instance
func GetAliOssClient(params *GenerateOssClientParams) Driver {
	aliOssOnce.Do(func() {
		config := AliConfig{
			AccessKey:    params.AccessKey,
			SecretKey:    params.SecretKey,
			Bucket:       params.BucketName,
			Endpoint:     params.Endpoint,
			Region:       params.Region,
			ObjectPrefix: params.ObjectPrefix,
			UWBPrefix:    params.UWBPrefix,
			HelpPrefix:   params.HelpPrefix,
		}

		aliOssClient = NewAliOss(config)
		if aliOssClient == nil {
			logrus.Fatal("Failed to initialize Aliyun OSS client")
		}
	})

	return aliOssClient
}

// InitializeMinioClient manually initializes the MinIO client
func InitializeMinioClient(config MinioConfig) Driver {
	minioOnce.Do(func() {
		minioClient = NewMinio(config)
		if minioClient == nil {
			logrus.Fatal("Failed to initialize MinIO client with provided config")
		}
	})

	return minioClient
}

// InitializeAliOssClient manually initializes the Aliyun OSS client
func InitializeAliOssClient(config AliConfig) Driver {
	aliOssOnce.Do(func() {
		aliOssClient = NewAliOss(config)
		if aliOssClient == nil {
			logrus.Fatal("Failed to initialize Aliyun OSS client with provided config")
		}
	})

	return aliOssClient
}

// ResetMinioClient resets the MinIO singleton (useful for testing)
func ResetMinioClient() {
	minioClient = nil
	minioOnce = sync.Once{}
}

// ResetAliOssClient resets the Aliyun OSS singleton (useful for testing)
func ResetAliOssClient() {
	aliOssClient = nil
	aliOssOnce = sync.Once{}
}

// GetOssClient returns the appropriate OSS client based on configuration
func GetOssClient(params *GenerateOssClientParams) Driver {
	provider := params.Provider
	switch provider {
	case "minio":
		return GetMinioClient(params)
	case "aliyun", "ali":
		return GetAliOssClient(params)
	default:
		// Default to MinIO for backward compatibility
		logrus.Warn("No OSS provider specified, defaulting to MinIO")
		return GetMinioClient(params)
	}
}
