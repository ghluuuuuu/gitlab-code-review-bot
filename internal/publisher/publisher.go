package publisher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/gitlab"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/review"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/store"
)

type Publisher struct {
	GitLab    *gitlab.Client
	ViewerURL string
}

func (p *Publisher) PublishProgress(ctx context.Context, job *store.ReviewJob, progress review.Progress) error {
	if p.GitLab == nil {
		return fmt.Errorf("gitlab client is required")
	}
	marker := progressMarker(job)
	body := fmt.Sprintf("## 代码审查中\n\n- 审查提交：`%s`\n- 当前状态：%s\n", job.HeadSHA, progress.Message)
	if progress.Total > 0 {
		body += fmt.Sprintf("- 文件进度：%d / %d\n", progress.Completed, progress.Total)
	}
	inputTokens, outputTokens, totalTokens := progress.InputTokens, progress.OutputTokens, progress.TotalTokens
	if inputTokens == 0 && outputTokens == 0 && totalTokens == 0 {
		inputTokens, outputTokens, totalTokens = job.InputTokens, job.OutputTokens, job.TotalTokens
	}
	if totalTokens > 0 || inputTokens > 0 || outputTokens > 0 {
		body += fmt.Sprintf("- Token：%d（输入 %d / 输出 %d）\n", totalTokens, inputTokens, outputTokens)
	}
	if progress.Finding != nil {
		body += "\n### 最新发现\n\n" + renderFinding(*progress.Finding) + "\n"
	}
	return p.GitLab.UpsertSummaryNote(ctx, job.ProjectID, job.MRIID, marker, body)
}

func progressMarker(job *store.ReviewJob) string {
	return fmt.Sprintf("<!-- ocr-progress:%d:%d:%s -->", job.ProjectID, job.MRIID, job.HeadSHA)
}

func progressMarkerPrefix(job *store.ReviewJob) string {
	return fmt.Sprintf("<!-- ocr-progress:%d:%d:", job.ProjectID, job.MRIID)
}

func conclusionMarker(job *store.ReviewJob) string {
	return fmt.Sprintf("<!-- ocr-summary:%d:%d:%s -->", job.ProjectID, job.MRIID, job.HeadSHA)
}

func conclusionMarkerPrefix(job *store.ReviewJob) string {
	return fmt.Sprintf("<!-- ocr-summary:%d:%d", job.ProjectID, job.MRIID)
}

func (p *Publisher) publishConclusion(ctx context.Context, job *store.ReviewJob, body string) error {
	marker := conclusionMarker(job)
	if err := p.GitLab.DeleteNotesWithMarkers(ctx, job.ProjectID, job.MRIID, progressMarkerPrefix(job), conclusionMarkerPrefix(job)); err != nil {
		slog.Warn("delete completed review notes", "project", job.ProjectID, "mr", job.MRIID, "error", err)
	}
	return p.GitLab.CreateNote(ctx, job.ProjectID, job.MRIID, marker+"\n"+body)
}

func (p *Publisher) PublishLiveFinding(ctx context.Context, job *store.ReviewJob, comment review.Comment) error {
	if p.GitLab == nil {
		return fmt.Errorf("gitlab client is required")
	}
	marker := liveFindingMarker(job, comment)
	exists, existsErr := p.GitLab.NoteExists(ctx, job.ProjectID, job.MRIID, marker)
	if existsErr != nil {
		return existsErr
	}
	if exists {
		return nil
	}
	if comment.Path != "" && comment.StartLine > 0 {
		version, versionErr := p.GitLab.GetDiffVersionForHead(ctx, job.ProjectID, job.MRIID, job.HeadSHA)
		if versionErr == nil {
			if err := p.GitLab.CreateDiffDiscussion(ctx, job.ProjectID, job.MRIID, version, comment.Path, comment.StartLine, marker+"\n"+renderFinding(comment)); err == nil {
				return nil
			} else if !gitlab.IsInvalidDiffPosition(err) {
				slog.Warn("publish diff discussion failed; using regular note", "project", job.ProjectID, "mr", job.MRIID, "path", comment.Path, "line", comment.StartLine, "error", err)
			}
		} else {
			slog.Warn("current diff version unavailable; using regular note", "project", job.ProjectID, "mr", job.MRIID, "review_head", job.HeadSHA, "error", versionErr)
		}
	}
	return p.publishOrdinaryFinding(ctx, job, marker, comment)
}

func (p *Publisher) publishOrdinaryFinding(ctx context.Context, job *store.ReviewJob, marker string, comment review.Comment) error {
	body := marker + "\n## 代码审查问题\n\n" + renderFinding(comment) + "\n\n> 无法定位到有效的差异行，已作为普通评论发布。\n"
	return p.GitLab.CreateNote(ctx, job.ProjectID, job.MRIID, body)
}

func liveFindingMarker(job *store.ReviewJob, comment review.Comment) string {
	return fmt.Sprintf("<!-- ocr-live-finding:%d:%d:%s:%s:%d -->", job.ProjectID, job.MRIID, job.HeadSHA, comment.Path, comment.StartLine)
}

func (p *Publisher) resolveSupersededFindings(ctx context.Context, job *store.ReviewJob, result review.Result) error {
	reconsideredPaths := make(map[string]struct{}, len(result.AffectedFiles))
	if result.Manifest != nil {
		for _, item := range result.Manifest.Coverage.Completed {
			if item.Path != "" {
				reconsideredPaths[item.Path] = struct{}{}
			}
		}
	}
	for _, path := range result.AffectedFiles {
		if path != "" {
			reconsideredPaths[path] = struct{}{}
		}
	}
	if len(reconsideredPaths) == 0 {
		return nil
	}
	discussions, err := p.GitLab.ListDiscussions(ctx, job.ProjectID, job.MRIID)
	if err != nil {
		return err
	}
	markerPrefix := fmt.Sprintf("<!-- ocr-live-finding:%d:%d:", job.ProjectID, job.MRIID)
	currentMarkerPrefix := markerPrefix + job.HeadSHA + ":"
	var resolveErr error
	for _, discussion := range discussions {
		shouldResolve := false
		for _, note := range discussion.Notes {
			if !strings.Contains(note.Body, markerPrefix) || strings.Contains(note.Body, currentMarkerPrefix) || note.Position == nil {
				continue
			}
			path := note.Position.NewPath
			if path == "" {
				path = note.Position.OldPath
			}
			if _, reconsidered := reconsideredPaths[path]; reconsidered && note.Resolvable && !note.Resolved {
				shouldResolve = true
				break
			}
		}
		if shouldResolve {
			resolveErr = errors.Join(resolveErr, p.GitLab.ResolveDiscussion(ctx, job.ProjectID, job.MRIID, discussion.ID))
		}
	}
	return resolveErr
}

func (p *Publisher) Publish(ctx context.Context, job *store.ReviewJob, result review.Result, blocking bool, ruleMissing bool) error {
	if p.GitLab == nil {
		return fmt.Errorf("gitlab client is required")
	}
	resolutionErr := p.resolveSupersededFindings(ctx, job, result)
	var findingPublishErr error
	for _, comment := range result.Comments {
		if err := p.PublishLiveFinding(ctx, job, comment); err != nil {
			findingPublishErr = errors.Join(findingPublishErr, err)
		}
	}
	conclusion := "通过"
	if blocking {
		conclusion = "不通过"
	}
	body := fmt.Sprintf("## 代码审查结果%s\n\n- 审查提交：`%s`\n- 目标分支：`%s`\n- 覆盖状态：`%s`\n- 问题数量：%d\n- Token：%d（输入 %d / 输出 %d）\n", conclusion, job.HeadSHA, job.TargetBranch, result.Status, len(result.Comments), result.Summary.TotalTokens, result.Summary.InputTokens, result.Summary.OutputTokens)
	if ruleMissing {
		body += fmt.Sprintf("\n> ⚠️ 在目标分支 `%s` 的仓库根目录中未找到项目审查规则文件，本次已使用 OCR 内置默认规则。建议补充该文件，以便后续审查遵循项目规则。\n", job.TargetBranch)
	}
	if result.ProjectSummary != "" {
		body += "\n### 项目摘要\n\n" + result.ProjectSummary + "\n"
	}
	if result.ChangeAnalysis != "" {
		body += "\n## 本次变更影响分析\n\n" + result.ChangeAnalysis + "\n"
	}
	if reportURL := p.ReviewReportURL(job, result.SessionID, job.RepoDir); reportURL != "" {
		body += fmt.Sprintf("\n### 📋 审查报告\n\n[%s](%s)\n", reportURL, reportURL)
	}
	if findingPublishErr != nil {
		body += "\n> 部分问题评论发布失败，请查看服务日志；后续增量审查会再次尝试。\n"
	}
	if resolutionErr != nil {
		body += "\n> 旧审查评论主题自动解决失败，将在下次增量审查时重试：" + resolutionErr.Error() + "\n"
	}
	return p.publishConclusion(ctx, job, body)
}

func (p *Publisher) PublishRuleFailure(ctx context.Context, job *store.ReviewJob, reason string) error {
	body := fmt.Sprintf("## OpenCodeReview 审查未通过\n\n- 审查提交：`%s`\n- 目标分支：`%s`\n- 状态：规则门禁失败\n- 原因：%s\n\n本次未调用 LLM，也未执行代码审查。", job.HeadSHA, job.TargetBranch, reason)
	return p.publishConclusion(ctx, job, body)
}

func (p *Publisher) PublishFailure(ctx context.Context, job *store.ReviewJob, failureType, reason string) error {
	body := fmt.Sprintf("## OpenCodeReview 审查未通过\n\n- 审查提交：`%s`\n- 目标分支：`%s`\n- 状态：%s\n- 原因：%s\n\n请修复后重新 push，Bot 将自动重新审查。", job.HeadSHA, job.TargetBranch, failureType, reason)
	if reportURL := p.ReviewReportURL(job, job.SessionID, job.RepoDir); reportURL != "" {
		body += fmt.Sprintf("\n\n[查看审查报告](%s)\n", reportURL)
	}
	return p.publishConclusion(ctx, job, body)
}

func (p *Publisher) ReviewReportURL(job *store.ReviewJob, sessionID, repoDir string) string {
	if reportURL := p.QualityReportURL(job); reportURL != "" {
		return reportURL
	}
	return p.ReportURL(sessionID, repoDir)
}

func (p *Publisher) QualityReportURL(job *store.ReviewJob) string {
	if job == nil || strings.TrimSpace(p.ViewerURL) == "" || job.MRIID <= 0 {
		return ""
	}
	projectID := job.TargetProjectID
	if projectID <= 0 {
		projectID = job.ProjectID
	}
	if projectID <= 0 {
		return ""
	}
	viewerURL := strings.TrimRight(strings.TrimSpace(p.ViewerURL), "/")
	reportURL, err := url.Parse(viewerURL + "/quality")
	if err != nil || reportURL.Scheme == "" || reportURL.Host == "" {
		return ""
	}
	query := reportURL.Query()
	query.Set("project_id", strconv.FormatInt(projectID, 10))
	query.Set("mr_iid", strconv.FormatInt(job.MRIID, 10))
	reportURL.RawQuery = query.Encode()
	return reportURL.String()
}

func (p *Publisher) ReportURL(sessionID, repoDir string) string {
	if strings.TrimSpace(p.ViewerURL) == "" || sessionID == "" {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(p.ViewerURL), "/") + "/r/" + encodeRepoPath(repoDir) + "/" + sessionID
}

func encodeRepoPath(p string) string {
	if p == "" {
		return "empty"
	}
	vol := filepath.VolumeName(p)
	p = p[len(vol):]
	p = strings.TrimLeft(p, "/\\")
	p = strings.ReplaceAll(p, "/", "-")
	p = strings.ReplaceAll(p, "\\", "-")
	vol = strings.ReplaceAll(vol, ":", "_")
	result := vol + p
	if result == "" {
		return "empty"
	}
	return result
}

func renderFinding(comment review.Comment) string {
	badge := ""
	if comment.Category != "" || comment.Severity != "" {
		badge = fmt.Sprintf("[%s · %s] ", emptyAs(comment.Category, "other"), emptyAs(comment.Severity, "unknown"))
	}
	location := ""
	if comment.Path != "" && comment.StartLine > 0 {
		endLine := comment.EndLine
		if endLine < comment.StartLine {
			endLine = comment.StartLine
		}
		location = fmt.Sprintf("\n\n`%s:%d-%d`", comment.Path, comment.StartLine, endLine)
	} else if comment.Path != "" {
		location = fmt.Sprintf("\n\n`%s`", comment.Path)
	}
	return badge + comment.Content + location
}

func emptyAs(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
