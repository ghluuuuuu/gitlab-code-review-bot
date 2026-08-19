package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Config struct {
	DatabasePath string          `json:"database_path"`
	DataDir      string          `json:"data_dir"`
	GitLab       GitLabConfig    `json:"gitlab"`
	Review       ReviewConfig    `json:"review"`
	CodeGraph    CodeGraphConfig `json:"code_graph"`
	LLM          LLMConfig       `json:"llm"`
	Server       ServerConfig    `json:"server"`
}

type GitLabConfig struct {
	BaseURL     string        `json:"base_url"`
	Token       string        `json:"token"`
	PollSeconds int           `json:"poll_seconds"`
	HTTPTimeout time.Duration `json:"-"`
}

type ReviewConfig struct {
	RulePath           string        `json:"rule_path"`
	Concurrency        int           `json:"concurrency"`
	FileConcurrency    int           `json:"file_concurrency"`
	Timeout            time.Duration `json:"-"`
	TimeoutMinutes     int           `json:"timeout_minutes"`
	BlockingSeverities []string      `json:"blocking_severities"`
	ViewerURL          string        `json:"viewer_url,omitempty"`
	DailyTokenBudget   int64         `json:"daily_token_budget,omitempty"`
	MonthlyTokenBudget int64         `json:"monthly_token_budget,omitempty"`
}

type CodeGraphConfig struct {
	Enabled        bool          `json:"enabled"`
	Command        string        `json:"command"`
	DataDir        string        `json:"data_dir,omitempty"`
	Timeout        time.Duration `json:"-"`
	TimeoutMinutes int           `json:"timeout_minutes"`
}

type LLMConfig struct {
	URL            string `json:"url"`
	Token          string `json:"token"`
	Model          string `json:"model"`
	UseAnthropic   bool   `json:"use_anthropic"`
	Language       string `json:"language"`
	AuthHeader     string `json:"auth_header,omitempty"`
	ExtraHeaders   string `json:"extra_headers,omitempty"`
	ExtraBody      string `json:"extra_body,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

type ServerConfig struct {
	Addr       string `json:"addr"`
	AdminToken string `json:"-"`
	AdminRole  string `json:"-"`
}

func Default() Config {
	return Config{
		DatabasePath: filepath.FromSlash("data/ocr-bot.db"),
		DataDir:      "data",
		GitLab: GitLabConfig{
			BaseURL:     "https://gitlab.example.com",
			PollSeconds: 30,
			HTTPTimeout: 30 * time.Second,
		},
		Review: ReviewConfig{
			RulePath:           ".opencodereview/rule.json",
			Concurrency:        2,
			FileConcurrency:    4,
			TimeoutMinutes:     30,
			Timeout:            30 * time.Minute,
			BlockingSeverities: []string{"critical", "high"},
			ViewerURL:          "http://localhost:5483",
		},
		CodeGraph: CodeGraphConfig{
			Enabled:        true,
			Command:        "code-review-graph",
			TimeoutMinutes: 10,
			Timeout:        10 * time.Minute,
		},
		LLM: LLMConfig{
			UseAnthropic: false,
			Language:     "中文",
		},
		Server: ServerConfig{Addr: ":8080"},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		path = os.Getenv("OCR_BOT_CONFIG")
	}
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return cfg, err
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("decode config: %w", err)
		}
	}
	applyEnvironment(&cfg)
	if cfg.DataDir == "" {
		cfg.DataDir = filepath.Dir(cfg.DatabasePath)
	}
	if cfg.GitLab.PollSeconds <= 0 {
		cfg.GitLab.PollSeconds = 30
	}
	if cfg.Review.Concurrency <= 0 {
		cfg.Review.Concurrency = 2
	}
	if cfg.Review.FileConcurrency <= 0 {
		cfg.Review.FileConcurrency = 4
	}
	if cfg.Review.TimeoutMinutes <= 0 {
		cfg.Review.TimeoutMinutes = 30
	}
	cfg.Review.Timeout = time.Duration(cfg.Review.TimeoutMinutes) * time.Minute
	if cfg.CodeGraph.Command == "" {
		cfg.CodeGraph.Command = "code-review-graph"
	}
	if cfg.CodeGraph.DataDir == "" {
		cfg.CodeGraph.DataDir = filepath.Join(cfg.DataDir, "code-graphs")
	}
	if cfg.CodeGraph.TimeoutMinutes <= 0 {
		cfg.CodeGraph.TimeoutMinutes = 10
	}
	cfg.CodeGraph.Timeout = time.Duration(cfg.CodeGraph.TimeoutMinutes) * time.Minute
	cfg.GitLab.HTTPTimeout = 30 * time.Second
	if cfg.DatabasePath == "" || cfg.GitLab.BaseURL == "" {
		return cfg, errors.New("database_path and gitlab.base_url are required")
	}
	if cfg.GitLab.Token == "" {
		return cfg, errors.New("gitlab.token is required")
	}
	if cfg.LLM.URL == "" || cfg.LLM.Model == "" {
		return cfg, errors.New("llm.url and llm.model are required")
	}
	return cfg, nil
}

func applyEnvironment(cfg *Config) {
	if value := os.Getenv("GITLAB_TOKEN"); value != "" {
		cfg.GitLab.Token = value
	}
	if value := os.Getenv("GITLAB_BASE_URL"); value != "" {
		cfg.GitLab.BaseURL = value
	}
	if value := os.Getenv("OCR_BOT_ADDR"); value != "" {
		cfg.Server.Addr = value
	}
	if value := os.Getenv("OCR_ADMIN_TOKEN"); value != "" {
		cfg.Server.AdminToken = value
	}
	if value := os.Getenv("OCR_ADMIN_ROLE"); value != "" {
		cfg.Server.AdminRole = value
	}
	if value := os.Getenv("OCR_LLM_URL"); value != "" {
		cfg.LLM.URL = value
	}
	if value := os.Getenv("OCR_LLM_TOKEN"); value != "" {
		cfg.LLM.Token = value
	}
	if value := os.Getenv("OCR_LLM_MODEL"); value != "" {
		cfg.LLM.Model = value
	}
}
