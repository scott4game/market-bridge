package market

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHTTPErrorStatusBeforeJSONAndRedaction(t *testing.T) {
	for _, status := range []int{401, 403, 429, 502, 504} {
		resp := &http.Response{StatusCode: status, Header: http.Header{"Retry-After": []string{"71"}}, Body: io.NopCloser(strings.NewReader(`error apiKey=VERYSECRET Authorization: Bearer ALSOSECRET`))}
		var result any
		err := DecodeHTTPJSON(resp, &result)
		if err == nil {
			t.Fatal("missing error")
		}
		d := ErrorDetails(err)
		if d.UpstreamStatus != status || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("%+v", d)
		}
		if status == 429 && d.RetryAfterSeconds != 71 {
			t.Fatal(d)
		}
		if (status == 401 || status == 403) && d.Retryable {
			t.Fatal(d)
		}
	}
}
func TestStructuredErrorSurvivesGatewayAndPartialWarning(t *testing.T) {
	original := &DataError{Code: "history_cooldown", Message: "wait", UpstreamStatus: 429, Retryable: true, RetryAfterSeconds: 123}
	raw, _ := json.Marshal(ErrorPayload(original))
	response := &http.Response{StatusCode: 502, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(raw)))}
	var v any
	err := DecodeHTTPJSON(response, &v)
	d := ErrorDetails(err)
	if d.Code != original.Code || d.UpstreamStatus != 429 || d.RetryAfterSeconds != 123 {
		t.Fatalf("%+v", d)
	}
	payload := WithDataWarning(map[string]any{"bars": []int{1}}, err)
	if payload["complete"] != false || payload["warning_details"] == nil {
		t.Fatal(payload)
	}
}
func TestInvalidJSONEmptyResponseAndNetworkClassification(t *testing.T) {
	for _, body := range []string{"", "not json"} {
		var out any
		err := DecodeHTTPJSON(&http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, &out)
		if err == nil || ErrorDetails(err).Code != "invalid_upstream_json" {
			t.Fatal(err)
		}
	}
	if ErrorDetails(context.Canceled).Retryable || !ErrorDetails(context.DeadlineExceeded).Retryable {
		t.Fatal("wrong retry policy")
	}
	if ErrorDetails(errors.New("invalid factor")).Retryable {
		t.Fatal("deterministic error retryable")
	}
	now := time.Now().UTC().Truncate(time.Second)
	if RetryAfter(now.Add(time.Minute).Format(http.TimeFormat), now) != 60 {
		t.Fatal("HTTP date retry-after")
	}
}

func TestLongStructuredErrorSurvivesGateway(t *testing.T) {
	for _, message := range []string{strings.Repeat("x", 2500), strings.Repeat("<", 4096)} {
		original := &DataError{Code: "history_cooldown", Message: message, UpstreamStatus: 429, Retryable: true, RetryAfterSeconds: 77}
		raw, err := json.Marshal(ErrorPayload(original))
		if err != nil {
			t.Fatal(err)
		}
		response := &http.Response{StatusCode: 502, Header: http.Header{"Retry-After": []string{"77"}}, Body: io.NopCloser(strings.NewReader(string(raw)))}
		var out any
		detail := ErrorDetails(DecodeHTTPJSON(response, &out))
		if detail.Code != original.Code || detail.UpstreamStatus != 429 || !detail.Retryable || detail.RetryAfterSeconds != 77 || detail.Message != message {
			t.Fatalf("metadata or message lost for %d-byte envelope: %+v", len(raw), detail)
		}
	}
}
