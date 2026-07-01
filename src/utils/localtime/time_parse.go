package localtime

import "time"

// ParseEventTime parses event timestamps into local time.
func ParseEventTime(ts string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		return t.In(time.Local), nil
	}
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t.In(time.Local), nil
	}

	layouts := []string{
		"2006-01-02 15:04:05.000",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.000",
		"2006-01-02T15:04:05",
	}
	var lastErr error
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, ts, time.Local); err == nil {
			return t.In(time.Local), nil
		} else {
			lastErr = err
		}
	}
	return time.Time{}, lastErr
}
