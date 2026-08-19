package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/agent"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/config/rules"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/config/template"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/config/toolsconfig"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/gitcmd"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/llm"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/llmloop"
	ocrmcp "github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/mcp"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/model"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/session"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/tool"
)

const OCRVersion = "v1.9.2"

type ProgressKind string

const (
	ProgressStarted  ProgressKind = "started"
	ProgressFinding  ProgressKind = "finding"
	ProgressFinished ProgressKind = "finished"
)

type Progress struct {
	Kind         ProgressKind
	Message      string
	Finding      *Comment
	SessionID    string
	Completed    int
	Total        int
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
}

type ProgressFunc func(context.Context, Progress) error

type TokenUsage struct {
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
}

type UsageFunc func(context.Context, TokenUsage)

type MCPClientOptions struct {
	Client       *ocrmcp.Client
	AllowedTools []string
}

type ComponentOptions struct {
	Repo        string
	From        string
	To          string
	RulePath    string
	Background  string
	Concurrency int
	ResumeIDs   []string
	SessionID   string
	LLMConfig   LLMConfig
	OnProgress  ProgressFunc
	OnUsage     UsageFunc
	MCPClients  []MCPClientOptions
}

func RunComponent(ctx context.Context, opts ComponentOptions) (Result, error) {
	if opts.Repo == "" || opts.From == "" || opts.To == "" {
		return Result{}, errors.New("repo, from, and to are required")
	}
	tpl, err := template.LoadDefault()
	if err != nil {
		return Result{}, fmt.Errorf("load OCR template: %w", err)
	}
	tpl.ApplyLanguage(opts.LLMConfig.Language)
	if err := tpl.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate OCR template: %w", err)
	}
	resolver, fileFilter, err := rules.NewResolver(opts.Repo, opts.RulePath)
	if err != nil {
		return Result{}, fmt.Errorf("load OCR rules: %w", err)
	}
	entries, err := toolsconfig.Load("")
	if err != nil {
		return Result{}, fmt.Errorf("load OCR tools: %w", err)
	}
	endpoint, err := componentEndpoint(opts.LLMConfig)
	if err != nil {
		return Result{}, err
	}
	if err := preflightLLM(ctx, opts.LLMConfig); err != nil {
		return Result{}, fmt.Errorf("LLM preflight failed: %w", err)
	}
	llm.InitEmbeddedLoader()

	var progressMu sync.Mutex
	var progressErr error
	collector := tool.NewCommentCollectorWithCallback(func(cm model.LlmComment) {
		progressMu.Lock()
		blocked := progressErr != nil
		progressMu.Unlock()
		if blocked || opts.OnProgress == nil {
			return
		}
		finding := commentFromModel(cm)
		err := opts.OnProgress(ctx, Progress{Kind: ProgressFinding, Message: "已生成并定位审查意见", Finding: &finding})
		if err != nil {
			progressMu.Lock()
			progressErr = errors.Join(progressErr, err)
			progressMu.Unlock()
		}
	})
	gitRunner := gitcmd.New(0)
	fileReader := &tool.FileReader{RepoDir: opts.Repo, Mode: tool.ModeRange, Ref: opts.To, Runner: gitRunner}
	registry := tool.NewRegistry()
	registry.Register(tool.NewFileRead(fileReader))
	registry.Register(tool.NewFileFind(fileReader))
	registry.Register(tool.NewFileReadDiff(tool.DiffMap{}))
	registry.Register(tool.NewCodeSearch(fileReader))
	registry.Register(&tool.CodeCommentProvider{Collector: collector})
	var mcpClients []*ocrmcp.Client
	for _, configured := range opts.MCPClients {
		if configured.Client == nil {
			continue
		}
		ocrmcp.RegisterAll(registry, configured.Client, configured.AllowedTools)
		mcpClients = append(mcpClients, configured.Client)
	}
	mcpToolDefs := ocrmcp.CollectToolDefs(mcpClients, registry)
	planToolDefs := append(agent.BuildToolDefs(entries, true), mcpToolDefs...)
	mainToolDefs := append(agent.BuildToolDefs(entries, false), mcpToolDefs...)

	var resumeStates []*session.ResumeState
	for _, resumeID := range opts.ResumeIDs {
		if resumeID == "" {
			continue
		}
		state, loadErr := session.LoadResumeState(opts.Repo, resumeID)
		if loadErr == nil {
			resumeStates = append(resumeStates, state)
		}
	}
	resume := session.MergeResumeStates(resumeStates...)

	background := opts.Background
	onAgentProgress := func(event agent.ProgressEvent) {
		progressMu.Lock()
		blocked := progressErr != nil
		progressMu.Unlock()
		if blocked || opts.OnProgress == nil {
			return
		}
		message := "正在分析 `" + event.Path + "`"
		if event.Kind == "file_completed" {
			message = "已完成 `" + event.Path + "`"
		} else if event.Kind == "file_failed" {
			message = "分析失败 `" + event.Path + "`，将保留已有结果用于恢复"
		}
		err := opts.OnProgress(ctx, Progress{Kind: ProgressStarted, Message: message, Completed: int(event.Completed), Total: int(event.Total), InputTokens: event.InputTokens, OutputTokens: event.OutputTokens, TotalTokens: event.TotalTokens})
		if err != nil {
			progressMu.Lock()
			progressErr = errors.Join(progressErr, err)
			progressMu.Unlock()
		}
	}
	resumeFrom := ""
	if resume != nil {
		resumeFrom = resume.SessionID
		if resumeFrom == opts.SessionID {
			for _, candidate := range resumeStates {
				if candidate != nil && candidate.SessionID != "" && candidate.SessionID != opts.SessionID {
					resumeFrom = candidate.SessionID
					break
				}
			}
			if resumeFrom == opts.SessionID {
				resumeFrom = ""
			}
		}
	}
	sessionHistory := session.New(opts.Repo, "", endpoint.Model, session.SessionOptions{
		SessionID: opts.SessionID, ReviewMode: session.ReviewModeRange, DiffFrom: opts.From, DiffTo: opts.To,
		ResumedFrom: resumeFrom, Operation: session.OperationReview,
	})
	if opts.OnProgress != nil {
		if err := opts.OnProgress(ctx, Progress{Kind: ProgressStarted, Message: "OpenCodeReview 源码组件已启动", SessionID: sessionHistory.SessionID}); err != nil {
			progressMu.Lock()
			progressErr = errors.Join(progressErr, err)
			progressMu.Unlock()
		}
	}

	ag := agent.New(agent.Args{
		RepoDir:          opts.Repo,
		From:             opts.From,
		To:               opts.To,
		ReviewMode:       session.ReviewModeRange,
		Template:         *tpl,
		SystemRule:       resolver,
		FileFilter:       fileFilter,
		LLMClient:        llm.NewLLMClient(endpoint),
		Tools:            registry,
		PlanToolDefs:     planToolDefs,
		MainToolDefs:     mainToolDefs,
		CommentCollector: collector,
		OnProgress:       onAgentProgress,
		OnUsage: func(usage llmloop.UsageSnapshot) {
			if opts.OnUsage != nil {
				opts.OnUsage(ctx, TokenUsage{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, TotalTokens: usage.TotalTokens})
			}
		},
		CommentWorkerPool:     agent.NewCommentWorkerPool(opts.Concurrency),
		MaxConcurrency:        opts.Concurrency,
		ConcurrentTaskTimeout: 0,
		Model:                 endpoint.Model,
		Provider:              endpoint.Provider,
		Background:            background,
		GitRunner:             gitRunner,
		Session:               sessionHistory,
		Resume:                resume,
		RuntimeConfig: agent.RuntimeConfig{
			Protocol:     endpoint.Protocol,
			Language:     opts.LLMConfig.Language,
			Timeout:      endpoint.Timeout,
			EndpointHost: endpointHost(endpoint.URL),
		},
	})

	started := time.Now()
	comments, runErr := ag.Run(ctx)
	result := resultFromAgent(ag, comments, endpoint, time.Since(started))
	progressMu.Lock()
	publishErr := progressErr
	progressMu.Unlock()
	if publishErr != nil {
		runErr = errors.Join(runErr, fmt.Errorf("publish live OCR progress: %w", publishErr))
	}
	if opts.OnProgress != nil && publishErr == nil {
		completed, total := 0, 0
		if result.Manifest != nil {
			completed = len(result.Manifest.Coverage.Completed) + len(result.Manifest.Coverage.Reused)
			total = len(result.Manifest.Coverage.Selected)
		}
		publishErr = opts.OnProgress(ctx, Progress{
			Kind: ProgressFinished, Message: "OpenCodeReview 源码组件执行完成",
			Completed: completed, Total: total,
		})
		runErr = errors.Join(runErr, publishErr)
	}
	return result, runErr
}

func componentEndpoint(cfg LLMConfig) (llm.ResolvedEndpoint, error) {
	protocol := llm.ProtocolOpenAIChatCompletions
	if cfg.UseAnthropic {
		protocol = llm.ProtocolAnthropic
	}
	if err := llm.ValidateProtocol(protocol); err != nil {
		return llm.ResolvedEndpoint{}, err
	}
	extraHeaders, err := parseStringMap(cfg.ExtraHeaders)
	if err != nil {
		return llm.ResolvedEndpoint{}, fmt.Errorf("decode llm.extra_headers: %w", err)
	}
	extraBody, err := parseAnyMap(cfg.ExtraBody)
	if err != nil {
		return llm.ResolvedEndpoint{}, fmt.Errorf("decode llm.extra_body: %w", err)
	}
	return llm.ResolvedEndpoint{
		URL: cfg.URL, Token: cfg.Token, Model: cfg.Model, Provider: "ocr-bot",
		Protocol: protocol, AuthHeader: cfg.AuthHeader, ExtraHeaders: extraHeaders,
		ExtraBody: extraBody, Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second,
	}, nil
}
func endpointHost(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Host
}

func parseStringMap(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	if strings.HasPrefix(strings.TrimSpace(raw), "{") {
		var result map[string]string
		return result, json.Unmarshal([]byte(raw), &result)
	}
	result := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		key, value, ok := strings.Cut(pair, ":")
		if !ok {
			return nil, fmt.Errorf("expected key:value, got %q", pair)
		}
		result[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return result, nil
}

func parseAnyMap(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var result map[string]any
	return result, json.Unmarshal([]byte(raw), &result)
}

func resultFromAgent(ag *agent.Agent, comments []model.LlmComment, endpoint llm.ResolvedEndpoint, elapsed time.Duration) Result {
	result := Result{
		Status: "complete", LLM: &LLMIdentity{Provider: endpoint.Provider, Model: endpoint.Model},
		Comments: make([]Comment, 0, len(comments)), SessionID: ag.SessionID(), ChangeAnalysis: ag.ChangeAnalysis(),
		Summary: Summary{FilesReviewed: ag.FilesReviewed(), Comments: int64(len(comments)),
			TotalTokens: ag.TotalTokensUsed(), InputTokens: ag.TotalInputTokens(), OutputTokens: ag.TotalOutputTokens(),
			CacheReadTokens: ag.TotalCacheReadTokens(), CacheWriteTokens: ag.TotalCacheWriteTokens(), Elapsed: elapsed.Round(time.Millisecond).String()},
	}
	for _, cm := range comments {
		result.Comments = append(result.Comments, commentFromModel(cm))
	}
	calls := ag.ToolCalls()
	if len(calls) > 0 {
		result.ToolCalls = &ToolCalls{ByTool: calls}
		for _, count := range calls {
			result.ToolCalls.Total += count
		}
	}
	if manifest := ag.RunManifest(); manifest != nil {
		result.Manifest = manifestFromOCR(manifest)
		result.Status = string(manifest.TerminalState)
	}
	return result
}

func commentFromModel(cm model.LlmComment) Comment {
	return Comment{Path: cm.Path, Content: cm.Content, SuggestionCode: cm.SuggestionCode,
		ExistingCode: cm.ExistingCode, StartLine: cm.StartLine, EndLine: cm.EndLine,
		Category: cm.Category, Severity: cm.Severity}
}

func manifestFromOCR(source *session.RunManifest) *Manifest {
	if source == nil {
		return nil
	}
	result := &Manifest{TerminalState: string(source.TerminalState)}
	copyItems := func(items []session.CoverageItem) []ManifestItem {
		out := make([]ManifestItem, 0, len(items))
		for _, item := range items {
			out = append(out, ManifestItem{ItemID: item.ItemID, Path: item.Path, Fingerprint: item.Fingerprint})
		}
		return out
	}
	result.Coverage.Selected = copyItems(source.Coverage.Selected)
	result.Coverage.Completed = copyItems(source.Coverage.Completed)
	result.Coverage.Reused = copyItems(source.Coverage.Reused)
	result.Coverage.Failed = copyItems(source.Coverage.Failed)
	result.Coverage.Waived = copyItems(source.Coverage.Waived)
	return result
}

func WriteResult(path string, result Result) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
