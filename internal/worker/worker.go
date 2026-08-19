package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/codegraph"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/config"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/gitlab"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/publisher"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/review"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/store"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/workspace"
)

type Worker struct {
	ID           string
	Config       config.Config
	BotUserID    int64
	Store        *store.Store
	GitLab       *gitlab.Client
	Workspace    *workspace.Manager
	Publisher    *publisher.Publisher
	ProjectLocks *codegraph.ProjectLocks
}

func (w *Worker) Run(ctx context.Context) {
	idle := time.NewTicker(time.Second)
	defer idle.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-idle.C:
			job, err := w.Store.Claim(ctx, w.ID, w.Config.Review.Timeout+5*time.Minute)
			if err != nil {
				slog.Error("claim review job", "worker", w.ID, "error", err)
				continue
			}
			if job == nil {
				continue
			}
			w.process(ctx, job)
		}
	}
}

func (w *Worker) process(parent context.Context, job *store.ReviewJob) {
	timeoutCtx, timeoutCancel := context.WithTimeout(parent, w.Config.Review.Timeout)
	defer timeoutCancel()
	ctx, cancel := context.WithCancelCause(timeoutCtx)
	defer cancel(nil)
	watchDone := make(chan struct{})
	defer close(watchDone)
	go w.cancelWhenSuperseded(ctx, cancel, watchDone, job)
	slog.Info("review job started", "worker", w.ID, "job", job.ID, "project", job.ProjectID, "mr", job.MRIID, "head_sha", job.HeadSHA, "source_branch", job.SourceBranch, "target_branch", job.TargetBranch)
	if err := w.ensureCurrent(ctx, job); err != nil {
		slog.Warn("review job stale before start", "worker", w.ID, "job", job.ID, "error", err)
		_ = w.Store.MarkStale(context.Background(), job.ID, err.Error())
		return
	}
	slog.Info("review stage starting", "worker", w.ID, "job", job.ID, "stage", "rule_preflight")
	targetBranch, err := w.GitLab.GetBranch(ctx, job.TargetProjectID, job.TargetBranch)
	if err != nil {
		w.finishInfra(ctx, job, fmt.Errorf("read target branch: %w", err))
		return
	}
	if targetBranch.Commit.ID != job.TargetSHA {
		_ = w.Store.MarkStale(context.Background(), job.ID, "target branch revision changed before review")
		return
	}
	var ruleData []byte
	var ruleFile gitlab.RepositoryFile
	ruleMissing := false
	ruleData, ruleFile, err = w.GitLab.GetRepositoryFile(ctx, job.TargetProjectID, w.Config.Review.RulePath, targetBranch.Commit.ID)
	if err != nil {
		if gitlab.IsNotFound(err) {
			slog.Info("review rule not found, continuing with OCR defaults", "job", job.ID, "rule_path", w.Config.Review.RulePath)
			ruleMissing = true
		} else {
			w.finishRuleFailure(ctx, job, store.StateRejectedRuleInvalid, "目标分支评审规则读取失败："+err.Error())
			return
		}
	}
	var ruleSHA string
	var validatedRule string
	if !ruleMissing {
		_, ruleSHA, err = review.ValidateRuleData(ruleData)
		if err != nil {
			w.finishRuleFailure(ctx, job, store.StateRejectedRuleInvalid, "评审规则无效："+err.Error())
			return
		}
	}
	resumeIDs := make([]string, 0)
	seenResumeIDs := make(map[string]struct{})
	addResumeID := func(sessionID string) {
		if sessionID == "" {
			return
		}
		if _, exists := seenResumeIDs[sessionID]; exists {
			return
		}
		seenResumeIDs[sessionID] = struct{}{}
		resumeIDs = append(resumeIDs, sessionID)
	}
	if job.RuleSHA256 == ruleSHA {
		addResumeID(job.SessionID)
	}
	historicalSessions, err := w.Store.ListReusableSessions(ctx, job.TargetProjectID, ruleSHA, job.ID)
	if err != nil {
		w.finishInfra(ctx, job, fmt.Errorf("list reusable review sessions: %w", err))
		return
	}
	for _, sessionID := range historicalSessions {
		addResumeID(sessionID)
	}
	if w.ProjectLocks != nil {
		releaseProject, lockErr := w.ProjectLocks.Acquire(ctx, job.TargetProjectID)
		if lockErr != nil {
			w.finishInfra(ctx, job, fmt.Errorf("acquire project review lock: %w", lockErr))
			return
		}
		defer releaseProject()
	}
	if err := w.Store.SetStage(ctx, job.ID, "git_prepare"); err != nil {
		w.finishInfra(ctx, job, err)
		return
	}
	slog.Info("review stage starting", "worker", w.ID, "job", job.ID, "stage", "git_prepare")
	prepared, err := w.Workspace.Prepare(ctx, job)
	if err != nil {
		w.finishInfra(ctx, job, fmt.Errorf("prepare workspace: %w", err))
		return
	}
	slog.Info("review workspace prepared", "worker", w.ID, "job", job.ID, "repo", prepared.RepoDir, "artifacts", prepared.ArtifactDir, "target_sha", prepared.TargetSHA, "base_sha", prepared.BaseSHA)
	job.RepoDir = prepared.RepoDir
	defer func() {
		if err := w.Workspace.Cleanup(prepared); err != nil {
			slog.Warn("cleanup workspace", "job", job.ID, "error", err)
		}
	}()
	if ruleMissing {
		validatedRule = ""
	} else {
		validatedRule = filepath.Join(prepared.ArtifactDir, "validated-rule.json")
		if err := os.WriteFile(validatedRule, ruleData, 0o444); err != nil {
			w.finishInfra(ctx, job, err)
			return
		}
	}
	if prepared.TargetSHA != targetBranch.Commit.ID {
		_ = w.Workspace.Cleanup(prepared)
		_ = w.Store.MarkStale(context.Background(), job.ID, "target branch changed during rule preflight")
		return
	}
	if err := w.Store.SetGitMetadata(ctx, job.ID, prepared.TargetSHA, prepared.BaseSHA, ruleSHA); err != nil {
		w.finishInfra(ctx, job, err)
		return
	}
	job.TargetSHA, job.BaseSHA, job.RuleSHA256 = prepared.TargetSHA, prepared.BaseSHA, ruleSHA
	background := filepath.Join(prepared.ArtifactDir, "mr-context.md")
	if err := os.WriteFile(background, []byte(fmt.Sprintf("# Merge Request\n\n- 项目：%d\n- MR：!%d\n- 标题：%s\n- Head SHA：%s\n- 目标分支：%s\n- Code Graph Base：%s\n- 规则 Blob：%s\n", job.ProjectID, job.MRIID, job.Title, job.HeadSHA, job.TargetBranch, prepared.TargetRef, ruleFile.BlobID)), 0o600); err != nil {
		w.finishInfra(ctx, job, err)
		return
	}
	var mcpClients []review.MCPClientOptions
	var affectedFiles []string
	if w.Config.CodeGraph.Enabled {
		if err := w.Store.SetStage(ctx, job.ID, "code_graph"); err != nil {
			w.finishInfra(ctx, job, err)
			return
		}
		graphDataDir := filepath.Join(w.Config.CodeGraph.DataDir, fmt.Sprintf("project-%d", job.TargetProjectID))
		slog.Info("review stage starting", "worker", w.ID, "job", job.ID, "stage", "code_graph", "data_dir", graphDataDir)
		graphSession, graphErr := codegraph.Build(ctx, codegraph.Options{
			Command: w.Config.CodeGraph.Command,
			RepoDir: prepared.RepoDir, DataDir: graphDataDir, Base: prepared.TargetRef,
			Timeout: w.Config.CodeGraph.Timeout, Version: review.OCRVersion,
		})
		if graphErr != nil {
			w.finishInfra(ctx, job, fmt.Errorf("prepare code review graph: %w", graphErr))
			return
		}
		defer func() {
			if err := graphSession.Close(); err != nil {
				slog.Warn("close code review graph MCP client", "job", job.ID, "error", err)
			}
		}()
		affectedFiles = append(affectedFiles, graphSession.AffectedFiles...)
		mcpClients = append(mcpClients, review.MCPClientOptions{Client: graphSession.Client, AllowedTools: codegraph.ReviewTools()})
	}
	if err := w.Store.SetStage(ctx, job.ID, "ocr_review"); err != nil {
		w.finishInfra(ctx, job, err)
		return
	}
	outputPath := filepath.Join(prepared.ArtifactDir, "ocr-result.json")
	backgroundData, readBackgroundErr := os.ReadFile(background)
	if readBackgroundErr != nil {
		w.finishInfra(ctx, job, readBackgroundErr)
		return
	}
	slog.Info("review stage starting", "worker", w.ID, "job", job.ID, "stage", "ocr_review", "component_version", review.OCRVersion, "resume_sessions", len(resumeIDs), "file_concurrency", w.Config.Review.FileConcurrency)
	var usagePublishMu sync.Mutex
	var lastUsagePublish time.Time
	result, runErr := review.RunComponent(ctx, review.ComponentOptions{
		Repo: prepared.RepoDir, From: prepared.TargetRef, To: job.HeadSHA,
		RulePath: validatedRule, Background: string(backgroundData), Concurrency: w.Config.Review.FileConcurrency,
		ResumeIDs: resumeIDs, SessionID: job.SessionID,
		MCPClients: mcpClients,
		LLMConfig: review.LLMConfig{
			URL: w.Config.LLM.URL, Token: w.Config.LLM.Token, Model: w.Config.LLM.Model,
			UseAnthropic: w.Config.LLM.UseAnthropic, Language: w.Config.LLM.Language,
			AuthHeader: w.Config.LLM.AuthHeader, ExtraHeaders: w.Config.LLM.ExtraHeaders,
			ExtraBody: w.Config.LLM.ExtraBody, TimeoutSeconds: w.Config.LLM.TimeoutSeconds,
		},
		OnProgress: func(progressCtx context.Context, progress review.Progress) error {
			if progress.SessionID != "" {
				job.SessionID = progress.SessionID
				if err := w.Store.SetLLMMetadata(context.Background(), job.ID, "ocr-bot", w.Config.LLM.Model, progress.SessionID); err != nil {
					return err
				}
			}
			if progress.Total > 0 {
				job.ProgressCompleted, job.ProgressTotal = progress.Completed, progress.Total
				if err := w.Store.SetProgress(context.Background(), job.ID, progress.Completed, progress.Total); err != nil {
					return err
				}
			}
			if progress.TotalTokens > 0 || progress.InputTokens > 0 || progress.OutputTokens > 0 {
				job.InputTokens, job.OutputTokens, job.TotalTokens = progress.InputTokens, progress.OutputTokens, progress.TotalTokens
				if err := w.Store.SetUsage(context.Background(), job.ID, progress.InputTokens, progress.OutputTokens, progress.TotalTokens); err != nil {
					return err
				}
			}
			logMessage := strings.TrimSpace(progress.Message)
			if progress.Finding != nil {
				logMessage = fmt.Sprintf("发现 %s/%s 问题：%s:%d", strings.TrimSpace(progress.Finding.Severity), strings.TrimSpace(progress.Finding.Category), progress.Finding.Path, progress.Finding.StartLine)
			}
			if logMessage != "" {
				if eventErr := w.Store.RecordEvent(context.Background(), store.ReviewEvent{ReviewJobID: job.ID, EventType: "analysis_log", Stage: "ocr_review", SafeMessage: logMessage, Completed: progress.Completed, Total: progress.Total, InputTokens: progress.InputTokens, OutputTokens: progress.OutputTokens, TotalTokens: progress.TotalTokens}); eventErr != nil {
					slog.Warn("persist review analysis log failed", "worker", w.ID, "job", job.ID, "error", eventErr)
				}
			}
			if err := w.ensureCurrent(progressCtx, job); err != nil {
				return err
			}
			if progress.Finding != nil {
				finding := *progress.Finding
				if findingErr := w.Store.RecordFinding(context.Background(), store.ReviewFinding{ReviewJobID: job.ID, Path: finding.Path, Content: finding.Content, SuggestionCode: finding.SuggestionCode, ExistingCode: finding.ExistingCode, StartLine: finding.StartLine, EndLine: finding.EndLine, Category: finding.Category, Severity: finding.Severity, Status: "current"}); findingErr != nil {
					slog.Warn("persist live OCR finding failed", "worker", w.ID, "job", job.ID, "path", finding.Path, "error", findingErr)
				}
				if publishErr := w.Publisher.PublishLiveFinding(progressCtx, job, *progress.Finding); publishErr != nil {
					slog.Warn("publish live OCR finding failed; continuing review", "worker", w.ID, "job", job.ID, "path", progress.Finding.Path, "start_line", progress.Finding.StartLine, "error", publishErr)
				}
				return nil
			}
			if publishErr := w.Publisher.PublishProgress(progressCtx, job, progress); publishErr != nil {
				slog.Warn("publish live OCR progress failed; continuing review", "worker", w.ID, "job", job.ID, "error", publishErr)
			}
			return nil
		},
		OnUsage: func(usageCtx context.Context, usage review.TokenUsage) {
			if usage.TotalTokens <= 0 && usage.InputTokens <= 0 && usage.OutputTokens <= 0 {
				return
			}
			if err := w.Store.SetUsage(context.Background(), job.ID, usage.InputTokens, usage.OutputTokens, usage.TotalTokens); err != nil {
				slog.Warn("persist live OCR token usage failed", "worker", w.ID, "job", job.ID, "error", err)
				return
			}
			usagePublishMu.Lock()
			shouldPublish := lastUsagePublish.IsZero() || time.Since(lastUsagePublish) >= 5*time.Second
			if shouldPublish {
				lastUsagePublish = time.Now()
			}
			usagePublishMu.Unlock()
			if !shouldPublish {
				return
			}
			if err := w.ensureCurrent(usageCtx, job); err != nil {
				slog.Warn("skip live OCR token publication", "worker", w.ID, "job", job.ID, "error", err)
				return
			}
			jobSnapshot, err := w.Store.GetJob(context.Background(), job.ID)
			if err != nil || jobSnapshot == nil {
				slog.Warn("read live OCR token usage failed", "worker", w.ID, "job", job.ID, "error", err)
				return
			}
			progress := review.Progress{Kind: review.ProgressStarted, Message: "正在执行模型审查", Completed: jobSnapshot.ProgressCompleted, Total: jobSnapshot.ProgressTotal, InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, TotalTokens: usage.TotalTokens}
			if err := w.Publisher.PublishProgress(usageCtx, jobSnapshot, progress); err != nil {
				slog.Warn("publish live OCR token usage failed; continuing review", "worker", w.ID, "job", job.ID, "error", err)
			}
		},
	})
	result.AffectedFiles = affectedFiles
	if runErr == nil {
		persistedFindings := make([]store.ReviewFinding, 0, len(result.Comments))
		for _, finding := range result.Comments {
			persistedFindings = append(persistedFindings, store.ReviewFinding{Path: finding.Path, Content: finding.Content, SuggestionCode: finding.SuggestionCode, ExistingCode: finding.ExistingCode, StartLine: finding.StartLine, EndLine: finding.EndLine, Category: finding.Category, Severity: finding.Severity, Status: "current"})
		}
		if findingErr := w.Store.ReplaceFindings(context.Background(), job.ID, persistedFindings); findingErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("persist OCR findings: %w", findingErr))
		}
	}
	if result.SessionID != "" {
		job.SessionID = result.SessionID
		_ = w.Store.SetLLMMetadata(context.Background(), job.ID, "ocr-bot", w.Config.LLM.Model, result.SessionID)
	}
	if writeErr := review.WriteResult(outputPath, result); writeErr != nil {
		runErr = errors.Join(runErr, fmt.Errorf("write OCR result artifact: %w", writeErr))
	}
	slog.Info("ocr review returned", "worker", w.ID, "job", job.ID, "status", result.Status, "comments", len(result.Comments), "error", runErr)
	if w.jobSuperseded(job) {
		slog.Info("review job stopped because database marked it stale", "worker", w.ID, "job", job.ID, "head_sha", job.HeadSHA)
		return
	}
	if cause := context.Cause(ctx); errors.Is(cause, errSuperseded) {
		slog.Info("review job stopped because a newer head exists", "worker", w.ID, "job", job.ID, "head_sha", job.HeadSHA)
		return
	}
	if err := w.ensureCurrent(ctx, job); err != nil {
		_ = w.Store.MarkStale(context.Background(), job.ID, err.Error())
		return
	}
	if err := w.ensureTargetCurrent(ctx, job); err != nil {
		_ = w.Store.MarkStale(context.Background(), job.ID, err.Error())
		return
	}
	usage := store.Usage{InputTokens: result.Summary.InputTokens, OutputTokens: result.Summary.OutputTokens, TotalTokens: result.Summary.TotalTokens, Comments: int64(len(result.Comments))}
	if result.ToolCalls != nil {
		usage.ToolCalls = result.ToolCalls.Total
	}
	if result.LLM != nil {
		_ = w.Store.SetLLMMetadata(ctx, job.ID, result.LLM.Provider, result.LLM.Model, result.SessionID)
	}
	job.SessionID = result.SessionID
	if runErr != nil {
		reason := truncate(runErr.Error(), 1000)
		delay, exhausted := reviewRetryPolicy(runErr, job.Attempt)
		if exhausted {
			w.finishInfra(ctx, job, fmt.Errorf("review failed after %d attempts: %w", job.Attempt, runErr))
			return
		}
		retryAt := time.Now().UTC().Add(delay)
		if err := w.Store.RetryAfter(context.Background(), job.ID, reason, prepared.ArtifactDir, usage, retryAt); err != nil {
			slog.Error("queue OCR resume", "job", job.ID, "error", err)
		}
		message := fmt.Sprintf("审查中断，已保留会话和已有发现，将在 %s 后自动恢复", delay.Round(time.Second))
		if review.IsLLMConfigurationError(runErr) {
			message = fmt.Sprintf("LLM 鉴权或配置失败，将在 %s 后重试；请检查实际加载的 Token、模型、接口地址和账户状态", delay.Round(time.Second))
		}
		_ = w.Publisher.PublishProgress(ctx, job, review.Progress{Kind: review.ProgressStarted, Message: message})
		return
	}
	blocking := review.IsBlocking(result, w.Config.Review.BlockingSeverities)
	if err := w.Store.SetStage(ctx, job.ID, "publishing"); err != nil {
		w.finishInfra(ctx, job, err)
		return
	}
	slog.Info("review stage starting", "worker", w.ID, "job", job.ID, "stage", "publishing", "blocking", blocking, "comments", len(result.Comments))
	if err := w.Publisher.Publish(ctx, job, result, blocking, ruleMissing); err != nil {
		w.finishInfra(ctx, job, fmt.Errorf("publish review: %w", err))
		return
	}
	state := store.StateCompletedPass
	if blocking {
		state = store.StateCompletedFail
	}
	viewerURL := w.Publisher.ReportURL(result.SessionID, prepared.RepoDir)
	if err := w.Store.Finish(context.Background(), job.ID, state, "", prepared.ArtifactDir, usage); err != nil {
		slog.Error("finish review job", "job", job.ID, "error", err)
	}
	slog.Info("review job finished", "worker", w.ID, "job", job.ID, "state", state, "comments", len(result.Comments), "total_tokens", usage.TotalTokens, "report_url", viewerURL)
}

var errSuperseded = errors.New("review job superseded by a newer head")

const maxTransientReviewAttempts = 5

func reviewRetryPolicy(err error, attempt int) (time.Duration, bool) {
	if review.IsLLMConfigurationError(err) {
		return 10 * time.Minute, false
	}
	if attempt >= maxTransientReviewAttempts {
		return 0, true
	}
	if attempt < 1 {
		attempt = 1
	}
	delay := 15 * time.Second
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= 5*time.Minute {
			return 5 * time.Minute, false
		}
	}
	return delay, false
}

func (w *Worker) cancelWhenSuperseded(ctx context.Context, cancel context.CancelCauseFunc, done <-chan struct{}, job *store.ReviewJob) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			active, err := w.Store.IsActive(context.Background(), job.ID, job.HeadSHA)
			if err != nil {
				slog.Warn("check review job activity", "worker", w.ID, "job", job.ID, "error", err)
				continue
			}
			if !active {
				cancel(errSuperseded)
				return
			}
		}
	}
}

func (w *Worker) jobSuperseded(job *store.ReviewJob) bool {
	active, err := w.Store.IsActive(context.Background(), job.ID, job.HeadSHA)
	if err != nil {
		slog.Warn("check review job activity", "worker", w.ID, "job", job.ID, "error", err)
		return false
	}
	return !active
}

func (w *Worker) ensureCurrent(ctx context.Context, job *store.ReviewJob) error {
	mr, err := w.GitLab.GetMergeRequest(ctx, job.ProjectID, job.MRIID)
	if err != nil {
		return err
	}
	if mr.State != "opened" || mr.SHA != job.HeadSHA {
		return errors.New("merge request head is stale")
	}
	for _, reviewer := range mr.Reviewers {
		if reviewer.ID == w.BotUserID {
			return nil
		}
	}
	return errors.New("ocr-review-bot is no longer a reviewer")
}

func (w *Worker) ensureTargetCurrent(ctx context.Context, job *store.ReviewJob) error {
	branch, err := w.GitLab.GetBranch(ctx, job.TargetProjectID, job.TargetBranch)
	if err != nil {
		return err
	}
	if branch.Commit.ID != job.TargetSHA {
		return errors.New("target branch revision is stale")
	}
	return nil
}

func (w *Worker) finishRuleFailure(ctx context.Context, job *store.ReviewJob, state, reason string) {
	if errors.Is(context.Cause(ctx), errSuperseded) || w.jobSuperseded(job) {
		slog.Info("skip rule failure for superseded job", "worker", w.ID, "job", job.ID)
		return
	}
	_ = w.Publisher.PublishRuleFailure(ctx, job, reason)
	if err := w.Store.Finish(context.Background(), job.ID, state, reason, "", store.Usage{}); err != nil {
		slog.Error("finish rule failure", "job", job.ID, "error", err)
	}
}

func (w *Worker) finishInfra(ctx context.Context, job *store.ReviewJob, err error) {
	if errors.Is(context.Cause(ctx), errSuperseded) || w.jobSuperseded(job) {
		slog.Info("skip infrastructure failure for superseded job", "worker", w.ID, "job", job.ID)
		return
	}
	reason := truncate(err.Error(), 1000)
	_ = w.Publisher.PublishFailure(ctx, job, "基础设施失败", reason)
	if finishErr := w.Store.Finish(context.Background(), job.ID, store.StateFailedInfra, reason, "", store.Usage{}); finishErr != nil {
		slog.Error("finish infrastructure failure", "job", job.ID, "error", finishErr)
	}
}

func truncate(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	return value[:max]
}
