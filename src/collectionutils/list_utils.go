package collectionutils

import (
	"strings"

	"github.com/sirupsen/logrus"
)

func ListContains(list []string, value string, logCtx map[string]interface{}) bool {
	defer func() {
		if r := recover(); r != nil {
			logrus.WithFields(logCtx).Errorf("collectionutils ListContains panic: %v", r)
		}
	}()
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if len(list) == 0 {
		return false
	}
	for _, item := range list {
		if strings.TrimSpace(item) == value {
			return true
		}
	}

	return false
}
