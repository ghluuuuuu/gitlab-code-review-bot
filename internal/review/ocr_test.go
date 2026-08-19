package review

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateRuleData(t *testing.T) {
	_, hash, err := ValidateRuleData([]byte(`{"rules":[{"path":"**/*.go","rule":"check errors"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if hash == "" {
		t.Fatal("expected rule hash")
	}
}

func TestValidateRuleDataRejectsEmptyRules(t *testing.T) {
	if _, _, err := ValidateRuleData([]byte(`{"rules":[]}`)); err == nil {
		t.Fatal("expected empty rules to fail")
	}
}

func TestIsBlocking(t *testing.T) {
	result := Result{Status: "complete", Comments: []Comment{{Severity: "high"}}}
	if !IsBlocking(result, []string{"critical", "high"}) {
		t.Fatal("expected high severity to block")
	}
}

func TestParseResultRejectsEmptyOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ocr-result.json")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ParseResult(path)
	if err == nil || !strings.Contains(err.Error(), "OCR result is empty") {
		t.Fatalf("expected actionable empty result error, got %v", err)
	}
}

func TestPreflightLLMClassifiesAuthenticationFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer invalid-token" {
			t.Fatalf("authorization header = %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid token"}}`))
	}))
	defer server.Close()
	err := preflightLLM(context.Background(), LLMConfig{URL: server.URL, Token: "invalid-token", Model: "model"})
	if err == nil || !IsLLMConfigurationError(err) || !strings.Contains(err.Error(), "Invalid token") {
		t.Fatalf("unexpected preflight error: %v", err)
	}
}

func TestLLMRateLimitRemainsRetryable(t *testing.T) {
	err := &LLMPreflightHTTPError{StatusCode: http.StatusTooManyRequests, Body: "busy"}
	if IsLLMConfigurationError(err) {
		t.Fatal("429 must remain a transient retryable error")
	}
}
