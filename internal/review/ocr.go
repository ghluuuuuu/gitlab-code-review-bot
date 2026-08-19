package review

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

type RuleSet struct {
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
	Rules   []Rule   `json:"rules"`
}

type Rule struct {
	Path string `json:"path"`
	Rule string `json:"rule"`
}

type Comment struct {
	Path           string `json:"path"`
	Content        string `json:"content"`
	SuggestionCode string `json:"suggestion_code,omitempty"`
	ExistingCode   string `json:"existing_code,omitempty"`
	StartLine      int    `json:"start_line"`
	EndLine        int    `json:"end_line"`
	Category       string `json:"category,omitempty"`
	Severity       string `json:"severity,omitempty"`
}

type Summary struct {
	FilesReviewed    int64  `json:"files_reviewed"`
	Comments         int64  `json:"comments"`
	TotalTokens      int64  `json:"total_tokens"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64  `json:"cache_write_tokens,omitempty"`
	Elapsed          string `json:"elapsed"`
	BudgetExceeded   bool   `json:"budget_exceeded,omitempty"`
}

type Manifest struct {
	TerminalState string           `json:"terminal_state"`
	Coverage      ManifestCoverage `json:"coverage,omitempty"`
}

type ManifestCoverage struct {
	Selected  []ManifestItem `json:"selected,omitempty"`
	Completed []ManifestItem `json:"completed,omitempty"`
	Reused    []ManifestItem `json:"reused,omitempty"`
	Failed    []ManifestItem `json:"failed,omitempty"`
	Waived    []ManifestItem `json:"waived,omitempty"`
}

type ManifestItem struct {
	ItemID      string `json:"item_id,omitempty"`
	Path        string `json:"path,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

type LLMIdentity struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

type ToolCalls struct {
	Total  int64            `json:"total"`
	ByTool map[string]int64 `json:"by_tool,omitempty"`
}

type Result struct {
	Status         string          `json:"status"`
	LLM            *LLMIdentity    `json:"llm,omitempty"`
	Message        string          `json:"message,omitempty"`
	Summary        Summary         `json:"summary"`
	ToolCalls      *ToolCalls      `json:"tool_calls,omitempty"`
	Comments       []Comment       `json:"comments"`
	Warnings       json.RawMessage `json:"warnings,omitempty"`
	ProjectSummary string          `json:"project_summary,omitempty"`
	ChangeAnalysis string          `json:"change_analysis,omitempty"`
	SessionID      string          `json:"session_id,omitempty"`
	Manifest       *Manifest       `json:"manifest,omitempty"`
	AffectedFiles  []string        `json:"affected_files,omitempty"`
}

type LLMConfig struct {
	URL            string
	Token          string
	Model          string
	UseAnthropic   bool
	Language       string
	AuthHeader     string
	ExtraHeaders   string
	ExtraBody      string
	TimeoutSeconds int
}

func ValidateRuleData(data []byte) (RuleSet, string, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return RuleSet{}, "", errors.New("review rule is empty")
	}
	var rules RuleSet
	if err := json.Unmarshal(data, &rules); err != nil {
		return RuleSet{}, "", fmt.Errorf("decode review rule: %w", err)
	}
	if len(rules.Rules) == 0 {
		return RuleSet{}, "", errors.New("review rules must contain at least one rule")
	}
	for i, rule := range rules.Rules {
		if strings.TrimSpace(rule.Path) == "" || strings.TrimSpace(rule.Rule) == "" {
			return RuleSet{}, "", fmt.Errorf("review rule %d requires path and rule", i)
		}
	}
	hash := sha256.Sum256(data)
	return rules, hex.EncodeToString(hash[:]), nil
}

type LLMPreflightHTTPError struct {
	StatusCode int
	Body       string
}

func (e *LLMPreflightHTTPError) Error() string {
	return fmt.Sprintf("LLM responded %d: %s", e.StatusCode, e.Body)
}

// IsLLMConfigurationError identifies client-side failures that require token,
// endpoint, model, quota, or request configuration changes rather than a hot retry.
func IsLLMConfigurationError(err error) bool {
	var httpErr *LLMPreflightHTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	if httpErr.StatusCode < 400 || httpErr.StatusCode >= 500 {
		return false
	}
	switch httpErr.StatusCode {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests:
		return false
	default:
		return true
	}
}

func preflightLLM(ctx context.Context, llm LLMConfig) error {
	slog.Info("ocr llm preflight", "url", llm.URL, "model", llm.Model)
	preflightCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"ping"}],"max_tokens":1}`, llm.Model)
	req, err := http.NewRequestWithContext(preflightCtx, http.MethodPost, llm.URL, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if llm.AuthHeader != "" {
		req.Header.Set(llm.AuthHeader, llm.Token)
	} else if llm.Token != "" {
		req.Header.Set("Authorization", "Bearer "+llm.Token)
	}
	if llm.ExtraHeaders != "" {
		for _, pair := range strings.Split(llm.ExtraHeaders, ",") {
			if key, value, ok := strings.Cut(pair, ":"); ok {
				req.Header.Set(strings.TrimSpace(key), strings.TrimSpace(value))
			}
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", llm.URL, err)
	}
	defer resp.Body.Close()
	slog.Info("ocr llm preflight response", "url", llm.URL, "status", resp.StatusCode)
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &LLMPreflightHTTPError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(respBody))}
	}
	return nil
}

func ParseResult(path string) (Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return Result{}, errors.New("OCR result is empty; inspect ocr-stderr.log and live console output")
	}
	var result Result
	if err := json.Unmarshal(data, &result); err != nil {
		return Result{}, fmt.Errorf("decode OCR result: %w", err)
	}
	if result.Comments == nil {
		result.Comments = []Comment{}
	}
	return result, nil
}

func CoverageIncomplete(result Result) bool {
	if result.Manifest == nil {
		return false
	}
	return len(result.Manifest.Coverage.Failed) > 0 ||
		len(result.Manifest.Coverage.Selected) > len(result.Manifest.Coverage.Completed)+len(result.Manifest.Coverage.Reused)+len(result.Manifest.Coverage.Failed)
}
func IsBlocking(result Result, severities []string) bool {
	blocking := make(map[string]struct{}, len(severities))
	for _, severity := range severities {
		blocking[strings.ToLower(severity)] = struct{}{}
	}
	for _, comment := range result.Comments {
		if _, ok := blocking[strings.ToLower(comment.Severity)]; ok {
			return true
		}
	}
	return result.Status == "partial" || result.Status == "failed" || CoverageIncomplete(result)
}
