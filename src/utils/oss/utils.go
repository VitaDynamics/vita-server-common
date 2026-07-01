package oss

import (
	"fmt"
	"net/url"
	"strings"
)

// ExtractObjectNameFromUrl extracts the object name from a URL
// This is a shared utility function used by all driver implementations
// For example: "http://192.168.31.199:9000/vita-tmp/ota-releases/vita-01/v1.0.0/manifest.yaml"
// Returns: "ota-releases/vita-01/v1.0.0/manifest.yaml"
func ExtractObjectNameFromUrl(urlStr, bucketName string) (string, error) {
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return "", fmt.Errorf("failed to parse URL: %w", err)
	}

	// Remove leading slash from path
	objectPath := strings.TrimPrefix(parsedURL.Path, "/")

	// If the path starts with bucket name, remove it
	if bucketName != "" && strings.HasPrefix(objectPath, bucketName+"/") {
		objectPath = strings.TrimPrefix(objectPath, bucketName+"/")
	}

	if objectPath == "" {
		return "", fmt.Errorf("no object name found in URL: %s", urlStr)
	}

	return objectPath, nil
}
