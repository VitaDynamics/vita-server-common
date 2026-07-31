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

func ListContainsInt(list []int, value int, logCtx map[string]interface{}) bool {
	defer func() {
		if r := recover(); r != nil {
			logrus.WithFields(logCtx).Errorf("collectionutils ListContainsInt panic: %v", r)
		}
	}()
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func ListContainsInt64(list []int64, value int64, logCtx map[string]interface{}) bool {
	defer func() {
		if r := recover(); r != nil {
			logrus.WithFields(logCtx).Errorf("collectionutils ListContainsInt64 panic: %v", r)
		}
	}()
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func ListContainsFloat64(list []float64, value float64, logCtx map[string]interface{}) bool {
	defer func() {
		if r := recover(); r != nil {
			logrus.WithFields(logCtx).Errorf("collectionutils ListContainsFloat64 panic: %v", r)
		}
	}()
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
