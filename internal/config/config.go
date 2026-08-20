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
	Auth         AuthConfig      `json:"auth"`
	SourcePath   string          `json:"-"`
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

type AuthConfig struct {
	Enabled        bool                 `json:"enabled"`
	SessionHours   int                  `json:"session_hours,omitempty"`
	BootstrapAdmin BootstrapAdminConfig `json:"bootstrap_admin,omitempty"`
	OIDC           OIDCConfig           `json:"oidc,omitempty"`
}

type BootstrapAdminConfig struct {
	Username string `json:"username,omitempty"`
	Email    string `json:"email,omitempty"`
	Password string `json:"password,omitempty"`
}

type OIDCConfig struct {
	Enabled      bool     `json:"enabled"`
	IssuerURL    string   `json:"issuer_url,omitempty"`
	ClientID     string   `json:"client_id,omitempty"`
	ClientSecret string   `json:"client_secret,omitempty"`
	RedirectURL  string   `json:"redirect_url,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
	AutoRegister bool     `json:"auto_register"`
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
			ViewerURL:          "http://localhost:8080",
		},
		Auth: AuthConfig{Enabled: false, SessionHours: 12, OIDC: OIDCConfig{AutoRegister: true}},
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
	authSpecified := false
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return cfg, err
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			return cfg, fmt.Errorf("decode config: %w", err)
		}
		_, authSpecified = raw["auth"]
		if err := json.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("decode config: %w", err)
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return cfg, err
		}
		cfg.SourcePath = absolute
	}
	applyEnvironment(&cfg)
	if path != "" && !authSpecified {
		cfg.Auth.Enabled = true
	}
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
	if cfg.Auth.SessionHours <= 0 {
		cfg.Auth.SessionHours = 12
	}
	if len(cfg.Auth.OIDC.Scopes) == 0 {
		cfg.Auth.OIDC.Scopes = []string{"openid", "profile", "email"}
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
	if err := Validate(cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func Validate(cfg Config) error {
	if cfg.DatabasePath == "" || cfg.GitLab.BaseURL == "" {
		return errors.New("database_path and gitlab.base_url are required")
	}
	if cfg.GitLab.Token == "" {
		return errors.New("gitlab.token is required")
	}
	if cfg.LLM.URL == "" || cfg.LLM.Model == "" {
		return errors.New("llm.url and llm.model are required")
	}
	if cfg.Auth.Enabled && cfg.Auth.OIDC.Enabled && (cfg.Auth.OIDC.IssuerURL == "" || cfg.Auth.OIDC.ClientID == "" || cfg.Auth.OIDC.ClientSecret == "") {
		return errors.New("OIDC issuer_url, client_id and client_secret are required when OIDC is enabled")
	}
	return nil
}

func ReadPersisted(path string) (Config, error) {
	var cfg Config
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	cfg.SourcePath = path
	return cfg, nil
}

func Save(cfg Config) error {
	if cfg.SourcePath == "" {
		return errors.New("configuration was not loaded from a file")
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	temporary := cfg.SourcePath + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, cfg.SourcePath)
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
	if value := os.Getenv("OCR_VIEWER_URL"); value != "" {
		cfg.Review.ViewerURL = value
	}
	if value := os.Getenv("OCR_AUTH_ENABLED"); value != "" {
		cfg.Auth.Enabled = value == "1" || value == "true"
	}
	if value := os.Getenv("OCR_BOOTSTRAP_ADMIN_USERNAME"); value != "" {
		cfg.Auth.BootstrapAdmin.Username = value
	}
	if value := os.Getenv("OCR_BOOTSTRAP_ADMIN_EMAIL"); value != "" {
		cfg.Auth.BootstrapAdmin.Email = value
	}
	if value := os.Getenv("OCR_BOOTSTRAP_ADMIN_PASSWORD"); value != "" {
		cfg.Auth.BootstrapAdmin.Password = value
	}
	if value := os.Getenv("OCR_OIDC_ISSUER_URL"); value != "" {
		cfg.Auth.OIDC.IssuerURL = value
		cfg.Auth.OIDC.Enabled = true
	}
	if value := os.Getenv("OCR_OIDC_CLIENT_ID"); value != "" {
		cfg.Auth.OIDC.ClientID = value
	}
	if value := os.Getenv("OCR_OIDC_CLIENT_SECRET"); value != "" {
		cfg.Auth.OIDC.ClientSecret = value
	}
	if value := os.Getenv("OCR_OIDC_REDIRECT_URL"); value != "" {
		cfg.Auth.OIDC.RedirectURL = value
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
