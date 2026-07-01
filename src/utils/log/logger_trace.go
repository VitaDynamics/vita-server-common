package log

import (
	"fmt"
	"strings"

	"github.com/sirupsen/logrus"
)

type VbotLogger struct {
	base *logrus.Logger
}

type TraceFirstTextFormatter struct {
	Base     *logrus.TextFormatter
	TraceKey string
}

func (f *TraceFirstTextFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	if f == nil || f.Base == nil {
		return (&logrus.TextFormatter{}).Format(entry)
	}

	traceKey := f.TraceKey
	if traceKey == "" {
		traceKey = "trace_id"
	}

	traceValue, hasTrace := entry.Data[traceKey]
	if !hasTrace || traceValue == nil {
		return f.Base.Format(entry)
	}

	cloned := *entry
	cloned.Data = make(logrus.Fields, len(entry.Data)-1)
	for key, value := range entry.Data {
		if key == traceKey {
			continue
		}
		cloned.Data[key] = value
	}
	cloned.Message = fmt.Sprintf("%s=%v %s", traceKey, traceValue, entry.Message)

	return f.Base.Format(&cloned)
}

func NewVbotLogger(base *logrus.Logger) *VbotLogger {
	if base == nil {
		base = logrus.New()
	}
	return &VbotLogger{base: base}
}

// WithLogCtx attaches key/value fields to the log entry.
func (logger *VbotLogger) WithLogCtx(logCtx map[string]any) *logrus.Entry {
	if logger == nil {
		return logrus.NewEntry(logrus.New())
	}
	if logger.base != nil {
		entry := logrus.NewEntry(logger.base)
		if len(logCtx) == 0 {
			return entry
		}
		for key, value := range logCtx {
			if strings.TrimSpace(key) == "" || value == nil {
				continue
			}
			entry = entry.WithField(key, value)
		}
		return entry
	}
	return logrus.NewEntry(logrus.New())
}
