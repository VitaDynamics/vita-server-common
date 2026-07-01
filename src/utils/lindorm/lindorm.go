package lindorm

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/VitaDynamics/vita-server-common/src/utils/localtime"

	"github.com/sirupsen/logrus"
)

const (
	defaultPrecision = "ms"
)

type Client struct {
	Endpoint     string
	Database     string
	Username     string
	Password     string
	SchemaPolicy string
	HttpClient   *http.Client
}

func Query(client *Client, sql string, logCtx map[string]any) ([]map[string]any, error) {
	start := time.Now()
	endpoint := buildQueryURL(client)
	body, status, err := client.doRequest(http.MethodPost, endpoint, sql)
	elapsed := time.Since(start)

	logrus.WithFields(logrus.Fields(logCtx)).Infof("lindorm query time coast: sql=%s elapsed=%dms", sql, elapsed.Milliseconds())
	if err != nil {
		logrus.WithFields(logrus.Fields(logCtx)).Errorf("lindorm query failed: sql=%s elapsed=%dms err=%v", sql, elapsed.Milliseconds(), err)
		return nil, err
	}
	if status >= 300 {
		err := fmt.Errorf("lindorm query failed: status=%d body=%s", status, string(body))
		logrus.WithFields(logrus.Fields(logCtx)).Errorf("lindorm query failed: sql=%s elapsed=%dms status=%d", sql, elapsed.Milliseconds(), status)
		return nil, err
	}

	result, err := parseSQLResponse(body)
	if err != nil {
		logrus.WithFields(logrus.Fields(logCtx)).Errorf("lindorm parse response failed: sql=%s elapsed=%dms err=%v", sql, elapsed.Milliseconds(), err)
		return nil, err
	}

	logrus.WithFields(logrus.Fields(logCtx)).Debugf("lindorm query success: sql=%s elapsed=%dms rows=%d", sql, elapsed.Milliseconds(), len(result))
	return result, nil
}

func QueryOne(client *Client, sql string, logCtx map[string]any) (map[string]any, error) {
	start := time.Now()
	rows, err := Query(client, sql, logCtx)
	elapsed := time.Since(start)

	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		logrus.WithFields(logrus.Fields(logCtx)).Debugf("lindorm query one: sql=%s elapsed=%dms rows=0", sql, elapsed.Milliseconds())
		return nil, nil
	}

	logrus.WithFields(logrus.Fields(logCtx)).Debugf("lindorm query one success: sql=%s elapsed=%dms", sql, elapsed.Milliseconds())
	return rows[0], nil
}

func Exec(client *Client, sql string, logCtx map[string]any) error {
	start := time.Now()
	endpoint := buildQueryURL(client)
	body, status, err := client.doRequest(http.MethodPost, endpoint, sql)
	elapsed := time.Since(start)

	logrus.WithFields(logrus.Fields(logCtx)).Infof("lindorm exec time coast: sql=%s elapsed=%dms", sql, elapsed.Milliseconds())

	if err != nil {
		logrus.WithFields(logrus.Fields(logCtx)).Errorf("lindorm exec failed: sql=%s elapsed=%dms err=%v", sql, elapsed.Milliseconds(), err)
		return err
	}
	if status >= 300 {
		err := fmt.Errorf("lindorm exec failed: status=%d body=%s", status, string(body))
		logrus.WithFields(logrus.Fields(logCtx)).Errorf("lindorm exec failed: sql=%s elapsed=%dms status=%d", sql, elapsed.Milliseconds(), status)
		return err
	}

	logrus.WithFields(logrus.Fields(logCtx)).Debugf("lindorm exec success: sql=%s elapsed=%dms", sql, elapsed.Milliseconds())
	return nil
}

func buildWriteURL(client *Client) string {
	base := strings.TrimRight(client.Endpoint, "/")
	v := url.Values{}
	v.Set("precision", defaultPrecision)
	v.Set("db", client.Database)
	if client.SchemaPolicy != "" {
		v.Set("schema_policy", client.SchemaPolicy)
	}
	return fmt.Sprintf("%s/api/v2/write?%s", base, v.Encode())
}

func buildQueryURL(client *Client) string {
	base := strings.TrimRight(client.Endpoint, "/")
	v := url.Values{}
	v.Set("db", client.Database)
	return fmt.Sprintf("%s/api/v2/sql?%s", base, v.Encode())
}

func (client *Client) doRequest(method, endpoint, payload string) ([]byte, int, error) {
	req, err := http.NewRequest(method, endpoint, bytes.NewBufferString(payload))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	if client.Username != "" || client.Password != "" {
		auth := base64.StdEncoding.EncodeToString([]byte(client.Username + ":" + client.Password))
		req.Header.Set("Authorization", "Basic "+auth)
	}
	resp, err := client.HttpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

func ParseTimestampMillis(value any) (int64, bool) {
	switch v := value.(type) {
	case int64:
		return normalizeTimestampMillis(v)
	case int:
		return normalizeTimestampMillis(int64(v))
	case float64:
		return normalizeTimestampMillis(int64(v))
	case float32:
		return normalizeTimestampMillis(int64(v))
	case string:
		v = strings.TrimSpace(v)
		if v == "" {
			return 0, false
		}
		if parsed, err := strconv.ParseInt(v, 10, 64); err == nil {
			return normalizeTimestampMillis(parsed)
		}
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return t.UnixMilli(), true
		}
		if t, err := localtime.ParseEventTime(v); err == nil {
			return t.UnixMilli(), true
		}
	}
	return 0, false
}

func normalizeTimestampMillis(ts int64) (int64, bool) {
	if ts <= 0 {
		return 0, false
	}
	// Detect epoch units by explicit digit-width ranges. This avoids treating
	// older millisecond timestamps, such as 1999-01-01, as seconds.
	switch {
	case ts >= 100_000_000_000_000_000 && ts < math.MaxInt64:
		return ts / 1_000_000, true
	case ts >= 100_000_000_000_000 && ts <= 9_999_999_999_999_999:
		return ts / 1_000, true
	case ts >= 100_000_000_000 && ts <= 9_999_999_999_999:
		return ts, true
	case ts >= 100_000_000 && ts <= 9_999_999_999:
		return ts * 1_000, true
	default:
		return 0, false
	}
}

func formatLineProtocol(measurement string, tags map[string]string, fields map[string]any, tsMillis int64) (string, error) {
	if measurement == "" {
		return "", errors.New("measurement is required")
	}
	fieldParts := make([]string, 0, len(fields))
	for k, v := range fields {
		part, ok := formatField(k, v)
		if ok {
			fieldParts = append(fieldParts, part)
		}
	}
	if len(fieldParts) == 0 {
		return "", errors.New("no fields to write")
	}
	sort.Strings(fieldParts)

	tagParts := make([]string, 0, len(tags))
	for k, v := range tags {
		if v == "" {
			continue
		}
		tagParts = append(tagParts, fmt.Sprintf("%s=%s", escapeTagOrKey(k), escapeTagOrKey(v)))
	}
	sort.Strings(tagParts)

	var b strings.Builder
	b.WriteString(escapeTagOrKey(measurement))
	if len(tagParts) > 0 {
		b.WriteString(",")
		b.WriteString(strings.Join(tagParts, ","))
	}
	b.WriteString(" ")
	b.WriteString(strings.Join(fieldParts, ","))
	b.WriteString(" ")
	b.WriteString(strconv.FormatInt(tsMillis, 10))
	return b.String(), nil
}

func formatField(key string, value any) (string, bool) {
	if key == "" || value == nil {
		return "", false
	}
	escapedKey := escapeTagOrKey(key)
	switch v := value.(type) {
	case int:
		return fmt.Sprintf("%s=%di", escapedKey, v), true
	case int8:
		return fmt.Sprintf("%s=%di", escapedKey, v), true
	case int16:
		return fmt.Sprintf("%s=%di", escapedKey, v), true
	case int32:
		return fmt.Sprintf("%s=%di", escapedKey, v), true
	case int64:
		return fmt.Sprintf("%s=%di", escapedKey, v), true
	case uint:
		return fmt.Sprintf("%s=%di", escapedKey, v), true
	case uint8:
		return fmt.Sprintf("%s=%di", escapedKey, v), true
	case uint16:
		return fmt.Sprintf("%s=%di", escapedKey, v), true
	case uint32:
		return fmt.Sprintf("%s=%di", escapedKey, v), true
	case uint64:
		return fmt.Sprintf("%s=%di", escapedKey, v), true
	case float32:
		return fmt.Sprintf("%s=%v", escapedKey, v), true
	case float64:
		return fmt.Sprintf("%s=%v", escapedKey, v), true
	case bool:
		return fmt.Sprintf("%s=%t", escapedKey, v), true
	case time.Time:
		return fmt.Sprintf("%s=\"%s\"", escapedKey, escapeString(v.Format(time.RFC3339))), true
	case string:
		return fmt.Sprintf("%s=\"%s\"", escapedKey, escapeString(v)), true
	default:
		if data, err := json.Marshal(v); err == nil {
			return fmt.Sprintf("%s=\"%s\"", escapedKey, escapeString(string(data))), true
		}
	}
	return "", false
}

func escapeTagOrKey(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, ",", "\\,")
	value = strings.ReplaceAll(value, " ", "\\ ")
	value = strings.ReplaceAll(value, "=", "\\=")
	return value
}

// escapeString sanitises a string so it can safely be placed inside a
// double-quoted field value in InfluxDB / Lindorm line protocol.
//
// Inside quoted strings the protocol only recognises two escape sequences:
//   - \\  → literal backslash
//   - \"  → literal double-quote
//
// Every other control character (newline, tab, NUL, …) is NOT a valid
// escape and will cause "Unable to parse line".  We handle everything in
// a single pass so there is no ambiguity about intermediate states.
func escapeString(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		switch {
		case r == '\\':
			b.WriteString("\\\\")
		case r == '"':
			b.WriteString("\\\"")
		case r < 0x20 || r == 0x7F:
			// Replace control characters (CR, LF, TAB, NUL, …) with a space.
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

type sqlResult struct {
	Columns []string        `json:"columns"`
	Rows    [][]any         `json:"rows"`
	Values  [][]any         `json:"values"`
	Series  []sqlSeriesData `json:"series"`
}

type sqlSeriesData struct {
	Columns []string `json:"columns"`
	Values  [][]any  `json:"values"`
}

type sqlResponse struct {
	Results []sqlResult `json:"results"`
	Columns []string    `json:"columns"`
	Rows    [][]any     `json:"rows"`
}

func parseSQLResponse(body []byte) ([]map[string]any, error) {
	var resp sqlResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if len(resp.Columns) > 0 && len(resp.Rows) > 0 {
		return rowsToMaps(resp.Columns, resp.Rows), nil
	}
	if len(resp.Results) == 0 {
		return nil, nil
	}
	for _, result := range resp.Results {
		if len(result.Columns) > 0 && len(result.Rows) > 0 {
			return rowsToMaps(result.Columns, result.Rows), nil
		}
		if len(result.Columns) > 0 && len(result.Values) > 0 {
			return rowsToMaps(result.Columns, result.Values), nil
		}
		if len(result.Series) > 0 {
			series := result.Series[0]
			if len(series.Columns) > 0 && len(series.Values) > 0 {
				return rowsToMaps(series.Columns, series.Values), nil
			}
		}
	}
	return nil, nil
}

func parseSQLColumns(body []byte) ([]string, error) {
	var resp sqlResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if len(resp.Columns) > 0 {
		return resp.Columns, nil
	}
	for _, result := range resp.Results {
		if len(result.Columns) > 0 {
			return result.Columns, nil
		}
		if len(result.Series) > 0 && len(result.Series[0].Columns) > 0 {
			return result.Series[0].Columns, nil
		}
	}
	return []string{}, nil
}

func rowsToMaps(columns []string, rows [][]any) []map[string]any {
	result := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		entry := make(map[string]any, len(columns))
		for i, col := range columns {
			if i < len(row) {
				entry[col] = row[i]
			}
		}
		result = append(result, entry)
	}
	return result
}
