package market

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DataError travels through gateway layers without losing upstream status or retry policy.
type DataError struct {
	Code              string `json:"code"`
	Message           string `json:"message"`
	UpstreamStatus    int    `json:"upstream_status,omitempty"`
	Retryable         bool   `json:"retryable"`
	RetryAfterSeconds int    `json:"retry_after_seconds,omitempty"`
	Cause             error  `json:"-"`
}

func (e *DataError) Error() string { return SafeErrorMessage(e.Message) }
func (e *DataError) Unwrap() error { return e.Cause }

var bearerSecret = regexp.MustCompile(`(?i)Bearer\s+[^\s"'<>]+`)
var namedSecret = regexp.MustCompile(`(?i)((?:api[_-]?key|token|authorization|secret|password)["']?\s*[:=]\s*["']?)[^&\s"'<>]+`)

func SafeErrorMessage(s string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "[REDACTED]")
		}
	}
	s = bearerSecret.ReplaceAllString(s, "Bearer [REDACTED]")
	s = namedSecret.ReplaceAllString(s, "${1}[REDACTED]")
	if len(s) > 4096 {
		s = s[:4096] + "…"
	}
	return s
}
func ErrorDetails(err error) *DataError {
	out := &DataError{Code: "data_error", Message: SafeErrorMessage(err.Error())}
	var detail *DataError
	if errors.As(err, &detail) {
		*out = *detail
		out.Cause = nil
		out.Message = SafeErrorMessage(err.Error())
		return out
	}
	if errors.Is(err, context.Canceled) {
		out.Code = "canceled"
		return out
	}
	var network net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &network) {
		out.Code = "network_error"
		out.Retryable = true
		out.RetryAfterSeconds = 30
	}
	return out
}
func ErrorPayload(err error) map[string]any {
	return map[string]any{"error": SafeErrorMessage(err.Error()), "error_details": ErrorDetails(err), "complete": false}
}
func WithDataWarning(payload map[string]any, err error) map[string]any {
	payload["warning"] = SafeErrorMessage(err.Error())
	payload["warning_details"] = ErrorDetails(err)
	payload["complete"] = false
	return payload
}
func WarningError(message string, detail *DataError) error {
	if detail == nil {
		return errors.New(SafeErrorMessage(message))
	}
	copy := *detail
	copy.Message = SafeErrorMessage(message)
	return &copy
}
func RetryAfter(value string, now time.Time) int {
	if seconds, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && seconds >= 0 {
		return seconds
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return int(math.Ceil(at.Sub(now).Seconds()))
	}
	return 0
}
func HTTPDataError(resp *http.Response, body []byte, secrets ...string) error {
	var envelope struct {
		Error   string     `json:"error"`
		Details *DataError `json:"error_details"`
	}
	_ = json.Unmarshal(body, &envelope)
	if envelope.Details != nil {
		copy := *envelope.Details
		copy.Message = SafeErrorMessage(envelope.Error, secrets...)
		if copy.Message == "" {
			copy.Message = SafeErrorMessage(envelope.Details.Message, secrets...)
		}
		return &copy
	}
	message := strings.TrimSpace(string(body))
	if envelope.Error != "" {
		message = envelope.Error
	}
	e := &DataError{Code: "upstream_http", Message: fmt.Sprintf("upstream status %d: %s", resp.StatusCode, SafeErrorMessage(message, secrets...)), UpstreamStatus: resp.StatusCode}
	if resp.StatusCode == 429 {
		e.Retryable = true
		e.RetryAfterSeconds = RetryAfter(resp.Header.Get("Retry-After"), time.Now())
		if e.RetryAfterSeconds == 0 {
			e.RetryAfterSeconds = 60
		}
	} else if resp.StatusCode >= 500 {
		e.Retryable = true
		e.RetryAfterSeconds = 30
	}
	return e
}

// DecodeHTTPJSON checks status first so HTML/plain-text failures stay diagnosable.
func DecodeHTTPJSON(resp *http.Response, v any) error {
	if resp.StatusCode/100 != 2 {
		// The envelope includes the message twice and JSON escaping can expand
		// each byte sixfold. Bound the envelope separately from its messages.
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if err != nil {
			return &DataError{Code: "upstream_read", Message: fmt.Sprintf("upstream status %d: %s", resp.StatusCode, SafeErrorMessage(err.Error())), UpstreamStatus: resp.StatusCode, Retryable: true, RetryAfterSeconds: 30}
		}
		return HTTPDataError(resp, body)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return &DataError{Code: "invalid_upstream_json", Message: fmt.Sprintf("upstream status %d returned invalid JSON: %s", resp.StatusCode, err), UpstreamStatus: resp.StatusCode, Retryable: true, RetryAfterSeconds: 30}
	}
	return nil
}
func SetRetryAfter(w http.ResponseWriter, err error) {
	if seconds := ErrorDetails(err).RetryAfterSeconds; seconds > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
	}
}
