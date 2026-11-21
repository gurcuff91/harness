package providers

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestKeyRejectionReason(t *testing.T) {
	for body, want := range map[string]string{
		`{"error":{"type":"authentication_error","code":"api_key_expired","message":"This API key has expired."}}`: "api_key_expired",
		`{"error":{"type":"authentication_error","message":"bad"}}`:                                                "authentication_error",
		`{"error":"Invalid API Key."}`: "Invalid API Key.",
		`{"message":"unauthorized"}`:   "unauthorized",
		`not json`:                     "",
		`{}`:                           "",
	} {
		if got := keyRejectionReason([]byte(body)); got != want {
			t.Errorf("keyRejectionReason(%s) = %q, want %q", body, got, want)
		}
	}
}

func TestRejectedKey(t *testing.T) {
	resp := func(code int, body string) *http.Response {
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body))}
	}
	if err := rejectedKey(resp(200, "")); err != nil {
		t.Errorf("200: %v", err)
	}
	for _, code := range []int{400, 402, 429, 500} {
		if err := rejectedKey(resp(code, `{"error":"x"}`)); err != nil {
			t.Errorf("%d is not a rejected key: %v", code, err)
		}
	}
	if err := rejectedKey(resp(401, `{"error":{"code":"api_key_expired"}}`)); err == nil || err.Error() != "invalid API key: api_key_expired" {
		t.Errorf("401: %v", err)
	}
	if err := rejectedKey(resp(403, `error code: 1010`)); err == nil || err.Error() != "invalid API key (HTTP 403)" {
		t.Errorf("403 without a body reason: %v", err)
	}
}
