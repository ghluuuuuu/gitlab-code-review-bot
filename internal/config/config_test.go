package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDoesNotRequireConfiguredBotUserID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{
		"database_path":"data/test.db",
		"gitlab":{"base_url":"https://gitlab.example.com","token":"token","poll_seconds":30},
		"review":{"concurrency":1,"timeout_minutes":1},
		"llm":{"url":"https://llm.example.com/v1/chat/completions","model":"model"},
		"server":{"addr":":8080"}
	}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("config without bot_user_id was rejected: %v", err)
	}
	if cfg.Review.FileConcurrency != 4 {
		t.Fatalf("default file_concurrency = %d, want 4", cfg.Review.FileConcurrency)
	}
	if cfg.Review.ViewerURL != "http://localhost:8080" {
		t.Fatalf("default viewer_url = %q", cfg.Review.ViewerURL)
	}
	if !cfg.CodeGraph.Enabled || cfg.CodeGraph.Command != "code-review-graph" || cfg.CodeGraph.DataDir != filepath.Join("data", "code-graphs") {
		t.Fatalf("default code graph config = %+v", cfg.CodeGraph)
	}
}

func TestApplyEnvironmentOverridesViewerURL(t *testing.T) {
	t.Setenv("OCR_VIEWER_URL", "https://reviews.example.com")
	cfg := Default()
	applyEnvironment(&cfg)
	if cfg.Review.ViewerURL != "https://reviews.example.com" {
		t.Fatalf("environment viewer_url = %q", cfg.Review.ViewerURL)
	}
}

func TestLoadConfiguredFileConcurrency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{
		"database_path":"data/test.db",
		"gitlab":{"base_url":"https://gitlab.example.com","token":"token"},
		"review":{"concurrency":2,"file_concurrency":6,"timeout_minutes":1},
		"code_graph":{"enabled":false,"command":"custom-code-review-graph","timeout_minutes":3},
		"llm":{"url":"https://llm.example.com/v1/chat/completions","model":"model"}
	}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Review.Concurrency != 2 || cfg.Review.FileConcurrency != 6 {
		t.Fatalf("review concurrency = %d, file concurrency = %d", cfg.Review.Concurrency, cfg.Review.FileConcurrency)
	}
	if cfg.CodeGraph.Enabled || cfg.CodeGraph.Command != "custom-code-review-graph" || cfg.CodeGraph.Timeout != 3*time.Minute {
		t.Fatalf("configured code graph config = %+v", cfg.CodeGraph)
	}
}
