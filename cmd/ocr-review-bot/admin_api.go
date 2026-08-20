package main

import (
	"context"
	"crypto/subtle"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/config"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/gitlab"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/viewer"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/publisher"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/review"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/store"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/workspace"
)

type adminAPI struct {
	store     *store.Store
	gl        *gitlab.Client
	cfg       config.Config
	projectMu sync.Mutex
	projects  map[int64]cachedProject
	auth      *authManager
}

type cachedProject struct {
	Value     gitlab.Project
	ExpiresAt time.Time
}
type adminContextKey string

const (
	adminRoleKey  adminContextKey = "admin-role"
	adminActorKey adminContextKey = "admin-actor"
)

type adminReview struct {
	ID                int64             `json:"id"`
	ProjectID         int64             `json:"project_id"`
	ProjectName       string            `json:"project_name,omitempty"`
	ProjectPath       string            `json:"project_path,omitempty"`
	ProjectWebURL     string            `json:"project_web_url,omitempty"`
	MRIID             int64             `json:"mr_iid"`
	Title             string            `json:"title"`
	WebURL            string            `json:"web_url"`
	SourceBranch      string            `json:"source_branch"`
	TargetBranch      string            `json:"target_branch"`
	HeadSHA           string            `json:"head_sha"`
	TargetSHA         string            `json:"target_sha"`
	BaseSHA           string            `json:"base_sha"`
	RuleSHA256        string            `json:"rule_sha256,omitempty"`
	State             string            `json:"state"`
	Stage             string            `json:"stage"`
	Priority          int               `json:"priority"`
	Attempt           int               `json:"attempt"`
	FailureReason     string            `json:"failure_reason,omitempty"`
	ProgressCompleted int               `json:"progress_completed"`
	ProgressTotal     int               `json:"progress_total"`
	QueuedAt          time.Time         `json:"queued_at"`
	StartedAt         *time.Time        `json:"started_at,omitempty"`
	FinishedAt        *time.Time        `json:"finished_at,omitempty"`
	InputTokens       int64             `json:"input_tokens"`
	OutputTokens      int64             `json:"output_tokens"`
	TotalTokens       int64             `json:"total_tokens"`
	Comments          int64             `json:"comments"`
	ToolCalls         int64             `json:"tool_calls"`
	LLMProvider       string            `json:"llm_provider,omitempty"`
	LLMModel          string            `json:"llm_model,omitempty"`
	SessionID         string            `json:"session_id,omitempty"`
	Coverage          adminCoverage     `json:"coverage"`
	Findings          adminFindingCount `json:"findings"`
	ReportURL         string            `json:"report_url,omitempty"`
}

type adminCoverage struct {
	Selected  int  `json:"selected"`
	Completed int  `json:"completed"`
	Reused    int  `json:"reused"`
	Failed    int  `json:"failed"`
	Waived    int  `json:"waived"`
	Complete  bool `json:"complete"`
}

type adminFindingCount struct {
	Total    int `json:"total"`
	Blocking int `json:"blocking"`
	New      int `json:"new"`
	Unfixed  int `json:"unfixed"`
	Fixed    int `json:"fixed"`
}

type adminFinding struct {
	Path           string `json:"path"`
	Content        string `json:"content"`
	SuggestionCode string `json:"suggestion_code,omitempty"`
	ExistingCode   string `json:"existing_code,omitempty"`
	StartLine      int    `json:"start_line"`
	EndLine        int    `json:"end_line"`
	Category       string `json:"category,omitempty"`
	Severity       string `json:"severity,omitempty"`
	Status         string `json:"status"`
}

type adminReviewDetail struct {
	Review      adminReview       `json:"review"`
	Rule        adminRule         `json:"rule"`
	Session     adminSession      `json:"session"`
	LLM         adminLLM          `json:"llm"`
	Coverage    adminCoverageData `json:"coverage"`
	Findings    []adminFinding    `json:"findings"`
	Publication adminPublication  `json:"publication"`
	Revisions   []adminReview     `json:"revisions"`
}

type adminRule struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	SHA256 string `json:"sha256,omitempty"`
}

type adminSession struct {
	ID          string `json:"id,omitempty"`
	Resumed     bool   `json:"resumed"`
	ResumedFrom string `json:"resumed_from,omitempty"`
}

type adminLLM struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

type adminCoverageData struct {
	adminCoverage
	SelectedFiles  []string `json:"selected_files,omitempty"`
	CompletedFiles []string `json:"completed_files,omitempty"`
	ReusedFiles    []string `json:"reused_files,omitempty"`
	FailedFiles    []string `json:"failed_files,omitempty"`
	WaivedFiles    []string `json:"waived_files,omitempty"`
	Affected       []string `json:"affected_files,omitempty"`
}

type adminPublication struct {
	State     string `json:"state"`
	Comments  int64  `json:"comments"`
	ReportURL string `json:"report_url,omitempty"`
}

type adminActionRequest struct {
	Reason        string `json:"reason"`
	ExpectedState string `json:"expected_state,omitempty"`
	Priority      int    `json:"priority,omitempty"`
}

func registerAdminRoutes(mux *http.ServeMux, st *store.Store, gl *gitlab.Client, cfg config.Config, auth *authManager) {
	api := &adminAPI{store: st, gl: gl, cfg: cfg, auth: auth, projects: make(map[int64]cachedProject)}
	mux.HandleFunc("/api/v1/admin/me", api.handleMe)
	mux.HandleFunc("/api/v1/admin/reviews", api.handleReviews)
	mux.HandleFunc("/api/v1/admin/reviews/", api.handleReview)
	mux.HandleFunc("/api/v1/admin/usage/summary", api.handleUsageSummary)
	mux.HandleFunc("/api/v1/admin/usage/trend", api.handleUsageTrend)
	mux.HandleFunc("/api/v1/admin/usage/projects", api.handleUsageProjects)
	mux.HandleFunc("/api/v1/admin/usage/models", api.handleUsageModels)
	mux.HandleFunc("/api/v1/admin/rules/problems", api.handleRuleProblems)
	mux.HandleFunc("/api/v1/admin/reconcile", api.handleReconcile)
	mux.HandleFunc("/api/v1/admin/system", api.handleSystem)
	mux.HandleFunc("/api/v1/admin/audit-events", api.handleAuditEvents)
	mux.HandleFunc("/api/v1/admin/events", api.handleEventStream)
}

func (a *adminAPI) handleMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if user, ok := currentAuthUser(r.Context()); ok {
		writeJSON(w, publicUser(user), nil)
		return
	}
	role := adminRole(r.Context())
	writeJSON(w, map[string]any{"name": adminActor(r.Context()), "roles": []string{role}, "permissions": adminPermissions(role)}, nil)
}

func (a *adminAPI) handleEventStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAdminError(w, http.StatusInternalServerError, "sse_not_supported", nil)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	events, unsubscribe := a.store.SubscribeEvents(0)
	defer unsubscribe()
	_, _ = fmt.Fprint(w, "event: connected\ndata: {}\n\n")
	flusher.Flush()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event, open := <-events:
			if !open {
				return
			}
			if a.auth != nil && a.auth.cfg.Auth.Enabled {
				job, jobErr := a.store.GetJob(r.Context(), event.ReviewJobID)
				if jobErr != nil || job == nil || !a.auth.requestCanAccessProject(r, job.TargetProjectID) {
					continue
				}
			}
			payload, err := json.Marshal(event)
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "event: review\ndata: %s\n\n", payload)
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

func (a *adminAPI) handleReviews(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	q := store.ReviewListQuery{
		Scope: r.URL.Query().Get("scope"), States: splitCSV(r.URL.Query().Get("state")), Stages: splitCSV(r.URL.Query().Get("stage")),
		ProjectID: parseInt64Query(r.URL.Query().Get("project_id")), MRIID: parseInt64Query(r.URL.Query().Get("mr_iid")),
		Page: parseIntDefault(r.URL.Query().Get("page"), 1), PageSize: parseIntDefault(r.URL.Query().Get("page_size"), 50), Sort: r.URL.Query().Get("sort"),
	}
	if q.ProjectID > 0 && !requireProjectAccess(w, r, a.auth, q.ProjectID) {
		return
	}
	page, err := a.store.ListReviews(r.Context(), q)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, "review_list_failed", err)
		return
	}
	items := make([]adminReview, 0, len(page.Items))
	for _, job := range page.Items {
		if a.auth != nil && !a.auth.requestCanAccessProject(r, job.TargetProjectID) {
			continue
		}
		summary := a.reviewSummary(job, nil)
		a.enrichProject(r.Context(), &summary, job.TargetProjectID)
		items = append(items, summary)
	}
	total, hasNext := page.Total, page.HasNext
	if a.auth != nil && a.auth.cfg.Auth.Enabled {
		total, hasNext = len(items), false
	}
	writeJSON(w, map[string]any{"items": items, "page": page.Page, "page_size": page.PageSize, "total": total, "has_next": hasNext}, nil)
}

func (a *adminAPI) handleReview(w http.ResponseWriter, r *http.Request) {
	suffix := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/reviews/")
	if suffix == "export" && r.Method == http.MethodGet {
		a.exportReviews(w, r)
		return
	}
	parts := strings.Split(suffix, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		writeAdminError(w, http.StatusBadRequest, "invalid_review_id", nil)
		return
	}
	if a.auth != nil && a.auth.cfg.Auth.Enabled {
		job, jobErr := a.store.GetJob(r.Context(), id)
		if jobErr != nil || job == nil {
			writeAdminError(w, http.StatusNotFound, "review_not_found", jobErr)
			return
		}
		if !requireProjectAccess(w, r, a.auth, job.TargetProjectID) {
			return
		}
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		a.reviewDetail(w, r, id)
		return
	}
	if len(parts) == 2 && r.Method == http.MethodGet {
		switch parts[1] {
		case "events":
			events, eventErr := a.store.ListEvents(r.Context(), id, parseIntDefault(r.URL.Query().Get("limit"), 100))
			writeJSON(w, events, eventErr)
		case "coverage", "findings":
			a.reviewDetailSubresource(w, r, id, parts[1])
		case "revisions":
			a.reviewRevisions(w, r, id)
		default:
			http.NotFound(w, r)
		}
		return
	}
	if len(parts) == 2 && r.Method == http.MethodPost {
		a.reviewAction(w, r, id, parts[1])
		return
	}
	methodNotAllowed(w)
}

func (a *adminAPI) exportReviews(w http.ResponseWriter, r *http.Request) {
	projectID := parseInt64Query(r.URL.Query().Get("project_id"))
	if projectID > 0 && !requireProjectAccess(w, r, a.auth, projectID) {
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="ocr-reviews.csv"`)
	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"id", "project_id", "mr_iid", "title", "head_sha", "target_sha", "state", "stage", "attempt", "total_tokens", "queued_at", "finished_at"})
	for pageNumber := 1; ; pageNumber++ {
		page, err := a.store.ListReviews(r.Context(), store.ReviewListQuery{Scope: r.URL.Query().Get("scope"), States: splitCSV(r.URL.Query().Get("state")), ProjectID: projectID, MRIID: parseInt64Query(r.URL.Query().Get("mr_iid")), Page: pageNumber, PageSize: 200, Sort: "updated_at.desc"})
		if err != nil {
			return
		}
		for _, job := range page.Items {
			if a.auth != nil && !a.auth.requestCanAccessProject(r, job.TargetProjectID) {
				continue
			}
			finished := ""
			if job.FinishedAt != nil {
				finished = job.FinishedAt.UTC().Format(time.RFC3339)
			}
			_ = writer.Write([]string{strconv.FormatInt(job.ID, 10), strconv.FormatInt(job.TargetProjectID, 10), strconv.FormatInt(job.MRIID, 10), job.Title, job.HeadSHA, job.TargetSHA, job.State, job.Stage, strconv.Itoa(job.Attempt), strconv.FormatInt(job.TotalTokens, 10), job.QueuedAt.UTC().Format(time.RFC3339), finished})
		}
		if !page.HasNext {
			break
		}
	}
	writer.Flush()
}

func (a *adminAPI) reviewDetail(w http.ResponseWriter, r *http.Request, id int64) {
	job, err := a.store.GetJob(r.Context(), id)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, "review_read_failed", err)
		return
	}
	if job == nil {
		writeAdminError(w, http.StatusNotFound, "review_not_found", nil)
		return
	}
	result, parsed := a.parseJobResult(job)
	detail := adminReviewDetail{
		Review:      a.reviewSummary(*job, &result),
		Rule:        adminRule{Path: a.cfg.Review.RulePath, Status: ruleStatus(*job)},
		Session:     adminSession{ID: job.SessionID, Resumed: job.SessionID != ""},
		LLM:         adminLLM{Provider: job.LLMProvider, Model: job.LLMModel},
		Findings:    []adminFinding{},
		Publication: adminPublication{State: publicationState(*job), Comments: job.Comments},
		Revisions:   []adminReview{},
	}
	a.enrichProject(r.Context(), &detail.Review, job.TargetProjectID)
	detail.Review.ReportURL = a.reportURL(*job)
	if stored, ok := a.storedFindings(r.Context(), job.ID); ok {
		detail.Findings = stored
	} else if parsed {
		detail.Findings = a.jobFindings(*job, result)
	}
	detail.Review.Findings = adminFindingCount{Total: len(detail.Findings)}
	blocking := make(map[string]struct{}, len(a.cfg.Review.BlockingSeverities))
	for _, severity := range a.cfg.Review.BlockingSeverities {
		blocking[strings.ToLower(strings.TrimSpace(severity))] = struct{}{}
	}
	for _, finding := range detail.Findings {
		if _, exists := blocking[strings.ToLower(strings.TrimSpace(finding.Severity))]; exists {
			detail.Review.Findings.Blocking++
		}
		switch finding.Status {
		case "new":
			detail.Review.Findings.New++
		case "unfixed":
			detail.Review.Findings.Unfixed++
		case "fixed":
			detail.Review.Findings.Fixed++
		}
	}
	if parsed {
		detail.Coverage = coverageData(result)
		detail.Publication.ReportURL = detail.Review.ReportURL
		if result.SessionID != "" {
			detail.Session.ID = result.SessionID
		}
	}
	writeJSON(w, detail, nil)
}

func (a *adminAPI) reviewDetailSubresource(w http.ResponseWriter, r *http.Request, id int64, kind string) {
	job, err := a.store.GetJob(r.Context(), id)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, "review_read_failed", err)
		return
	}
	if job == nil {
		writeAdminError(w, http.StatusNotFound, "review_not_found", nil)
		return
	}
	result, _ := a.parseJobResult(job)
	if kind == "coverage" {
		writeJSON(w, coverageData(result), nil)
		return
	}
	if findings, ok := a.storedFindings(r.Context(), job.ID); ok {
		writeJSON(w, findings, nil)
		return
	}
	writeJSON(w, a.jobFindings(*job, result), nil)
}

func (a *adminAPI) reviewRevisions(w http.ResponseWriter, r *http.Request, id int64) {
	job, err := a.store.GetJob(r.Context(), id)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, "review_read_failed", err)
		return
	}
	if job == nil {
		writeAdminError(w, http.StatusNotFound, "review_not_found", nil)
		return
	}
	revisions, err := a.store.ListReviewRevisions(r.Context(), job.ProjectID, job.MRIID)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, "revision_read_failed", err)
		return
	}
	result := make([]adminReview, 0, len(revisions))
	for _, revision := range revisions {
		result = append(result, a.reviewSummary(revision, nil))
	}
	writeJSON(w, result, nil)
}

func (a *adminAPI) reviewAction(w http.ResponseWriter, r *http.Request, id int64, action string) {
	if role := adminRole(r.Context()); role != "operator" && role != "admin" {
		writeAdminError(w, http.StatusForbidden, "admin_role_forbidden", nil)
		return
	}
	var request adminActionRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_action_body", err)
		return
	}
	request.Reason = strings.TrimSpace(request.Reason)
	if request.Reason == "" {
		writeAdminError(w, http.StatusBadRequest, "reason_required", nil)
		return
	}
	job, err := a.store.GetJob(r.Context(), id)
	if err != nil || job == nil {
		writeAdminError(w, http.StatusNotFound, "review_not_found", err)
		return
	}
	if request.ExpectedState != "" && request.ExpectedState != job.State {
		writeAdminError(w, http.StatusConflict, "review_state_conflict", nil)
		return
	}
	var actionErr error
	switch action {
	case "retry":
		actionErr = a.store.RetryReview(r.Context(), id, request.Reason)
	case "cancel":
		actionErr = a.store.CancelReview(r.Context(), id, request.Reason)
	case "priority":
		actionErr = a.store.SetPriority(r.Context(), id, request.Priority, request.Reason)
	default:
		http.NotFound(w, r)
		return
	}
	if actionErr != nil {
		writeAdminError(w, http.StatusConflict, "review_action_failed", actionErr)
		return
	}
	actor := adminActor(r.Context())
	_ = a.store.RecordAudit(r.Context(), actor, "review."+action, &id, request.Reason)
	updated, getErr := a.store.GetJob(r.Context(), id)
	if getErr != nil || updated == nil {
		writeAdminError(w, http.StatusInternalServerError, "review_read_failed", getErr)
		return
	}
	writeJSON(w, a.reviewSummary(*updated, nil), nil)
}

func (a *adminAPI) handleUsageSummary(w http.ResponseWriter, r *http.Request) {
	if adminRole(r.Context()) != "admin" {
		writeAdminError(w, http.StatusForbidden, "superadmin_required", nil)
		return
	}
	from, to := usageRange(r)
	value, err := a.store.UsageSummary(r.Context(), from, to)
	writeJSON(w, value, err)
}

func (a *adminAPI) handleUsageTrend(w http.ResponseWriter, r *http.Request) {
	if adminRole(r.Context()) != "admin" {
		writeAdminError(w, http.StatusForbidden, "superadmin_required", nil)
		return
	}
	from, to := usageRange(r)
	value, err := a.store.UsageTrend(r.Context(), from, to)
	writeJSON(w, value, err)
}
func (a *adminAPI) handleUsageProjects(w http.ResponseWriter, r *http.Request) {
	if adminRole(r.Context()) != "admin" {
		writeAdminError(w, http.StatusForbidden, "superadmin_required", nil)
		return
	}
	from, to := usageRange(r)
	value, err := a.store.UsageByProject(r.Context(), from, to, parseIntDefault(r.URL.Query().Get("limit"), 50))
	writeJSON(w, value, err)
}

func (a *adminAPI) handleUsageModels(w http.ResponseWriter, r *http.Request) {
	if adminRole(r.Context()) != "admin" {
		writeAdminError(w, http.StatusForbidden, "superadmin_required", nil)
		return
	}
	from, to := usageRange(r)
	value, err := a.store.UsageByModel(r.Context(), from, to, parseIntDefault(r.URL.Query().Get("limit"), 50))
	writeJSON(w, value, err)
}

func (a *adminAPI) handleRuleProblems(w http.ResponseWriter, r *http.Request) {
	page, err := a.store.ListReviews(r.Context(), store.ReviewListQuery{States: []string{store.StateRejectedRuleMissing, store.StateRejectedRuleInvalid}, Page: 1, PageSize: parseIntDefault(r.URL.Query().Get("limit"), 100), Sort: "updated_at.desc"})
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, "rule_problem_list_failed", err)
		return
	}
	items := make([]adminReview, 0, len(page.Items))
	for _, job := range page.Items {
		if a.auth != nil && !a.auth.requestCanAccessProject(r, job.TargetProjectID) {
			continue
		}
		items = append(items, a.reviewSummary(job, nil))
	}
	writeJSON(w, items, nil)
}

func (a *adminAPI) handleReconcile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if adminRole(r.Context()) != "admin" {
		writeAdminError(w, http.StatusForbidden, "admin_role_forbidden", nil)
		return
	}
	bot, err := a.gl.GetCurrentUser(r.Context())
	if err != nil {
		writeAdminError(w, http.StatusBadGateway, "gitlab_identity_failed", err)
		return
	}
	discoverReviews(r.Context(), a.gl, a.store, a.cfg, bot.ID)
	_ = a.store.RecordAudit(r.Context(), adminActor(r.Context()), "system.reconcile", nil, "manual discovery")
	writeJSON(w, map[string]string{"status": "completed"}, nil)
}

func (a *adminAPI) handleSystem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if adminRole(r.Context()) != "admin" {
		writeAdminError(w, http.StatusForbidden, "superadmin_required", nil)
		return
	}
	dashboard, err := a.store.Dashboard(r.Context())
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, "system_status_failed", err)
		return
	}
	writeJSON(w, map[string]any{
		"database":   map[string]string{"status": "ready"},
		"gitlab":     map[string]string{"base_url": a.cfg.GitLab.BaseURL, "status": "configured"},
		"llm":        map[string]string{"model": a.cfg.LLM.Model, "status": configuredStatus(a.cfg.LLM.URL, a.cfg.LLM.Model)},
		"code_graph": map[string]any{"enabled": a.cfg.CodeGraph.Enabled, "command": a.cfg.CodeGraph.Command, "status": configuredStatus(a.cfg.CodeGraph.Command)},
		"viewer":     map[string]string{"url": a.cfg.Review.ViewerURL, "status": configuredStatus(a.cfg.Review.ViewerURL)},
		"dashboard":  dashboard,
		"budgets": map[string]any{
			"daily": a.cfg.Review.DailyTokenBudget, "monthly": a.cfg.Review.MonthlyTokenBudget,
			"daily_used": dashboard.TodayTokens, "monthly_used": dashboard.MonthTokens,
			"daily_exceeded":   a.cfg.Review.DailyTokenBudget > 0 && dashboard.TodayTokens >= a.cfg.Review.DailyTokenBudget,
			"monthly_exceeded": a.cfg.Review.MonthlyTokenBudget > 0 && dashboard.MonthTokens >= a.cfg.Review.MonthlyTokenBudget,
		},
	}, nil)
}

func (a *adminAPI) handleAuditEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if adminRole(r.Context()) != "admin" {
		writeAdminError(w, http.StatusForbidden, "superadmin_required", nil)
		return
	}
	events, err := a.store.ListAuditEvents(r.Context(), parseIntDefault(r.URL.Query().Get("limit"), 100))
	writeJSON(w, events, err)
}

func (a *adminAPI) reviewSummary(job store.ReviewJob, result *review.Result) adminReview {
	summary := adminReview{
		ID: job.ID, ProjectID: job.TargetProjectID, MRIID: job.MRIID, Title: job.Title, WebURL: job.WebURL,
		SourceBranch: job.SourceBranch, TargetBranch: job.TargetBranch, HeadSHA: job.HeadSHA, TargetSHA: job.TargetSHA,
		BaseSHA: job.BaseSHA, RuleSHA256: job.RuleSHA256, State: job.State, Stage: job.Stage, Priority: job.Priority,
		Attempt: job.Attempt, FailureReason: job.FailureReason, ProgressCompleted: job.ProgressCompleted,
		ProgressTotal: job.ProgressTotal, QueuedAt: job.QueuedAt, StartedAt: job.StartedAt, FinishedAt: job.FinishedAt,
		InputTokens: job.InputTokens, OutputTokens: job.OutputTokens, TotalTokens: job.TotalTokens, Comments: job.Comments,
		ToolCalls: job.ToolCalls, LLMProvider: job.LLMProvider, LLMModel: job.LLMModel, SessionID: job.SessionID,
		Findings: adminFindingCount{Total: int(job.Comments)},
	}
	if result != nil {
		summary.Coverage = coverageData(*result).adminCoverage
		summary.Findings = findingCounts(*result, a.cfg.Review.BlockingSeverities)
	}
	return summary
}

func (a *adminAPI) parseJobResult(job *store.ReviewJob) (review.Result, bool) {
	if job == nil || job.ArtifactDir == "" {
		return review.Result{}, false
	}
	result, err := review.ParseResult(filepath.Join(job.ArtifactDir, "ocr-result.json"))
	return result, err == nil
}

func (a *adminAPI) enrichProject(ctx context.Context, target *adminReview, projectID int64) {
	if target == nil || a.gl == nil || projectID <= 0 {
		return
	}
	now := time.Now().UTC()
	a.projectMu.Lock()
	cached, ok := a.projects[projectID]
	a.projectMu.Unlock()
	if !ok || cached.ExpiresAt.Before(now) {
		project, err := a.gl.GetProject(ctx, projectID)
		if err != nil {
			return
		}
		cached = cachedProject{Value: project, ExpiresAt: now.Add(10 * time.Minute)}
		a.projectMu.Lock()
		a.projects[projectID] = cached
		a.projectMu.Unlock()
	}
	target.ProjectName = cached.Value.Name
	target.ProjectPath = cached.Value.PathWithNamespace
	target.ProjectWebURL = cached.Value.WebURL
}

func (a *adminAPI) jobFindings(job store.ReviewJob, result review.Result) []adminFinding {
	reportURL := a.reportURL(job)
	if reportURL != "" {
		if parsed, err := url.Parse(reportURL); err == nil {
			parts := strings.Split(strings.TrimPrefix(parsed.Path, "/r/"), "/")
			if len(parts) >= 2 {
				if root, rootErr := viewer.SessionsRoot(); rootErr == nil {
					if sessionView, loadErr := viewer.LoadSession(root, parts[0], job.SessionID); loadErr == nil {
						findings := make([]adminFinding, 0, len(sessionView.Comments))
						for _, comment := range sessionView.Comments {
							findings = append(findings, adminFinding{Path: comment.FilePath, Content: comment.Content, SuggestionCode: comment.SuggestionCode, ExistingCode: comment.ExistingCode, StartLine: comment.StartLine, EndLine: comment.EndLine, Category: comment.Category, Severity: comment.Severity, Status: string(comment.Status)})
						}
						return findings
					}
				}
			}
		}
	}
	findings := make([]adminFinding, 0, len(result.Comments))
	for _, comment := range result.Comments {
		findings = append(findings, adminFinding{Path: comment.Path, Content: comment.Content, SuggestionCode: comment.SuggestionCode, ExistingCode: comment.ExistingCode, StartLine: comment.StartLine, EndLine: comment.EndLine, Category: comment.Category, Severity: comment.Severity, Status: "current"})
	}
	return findings
}

func (a *adminAPI) storedFindings(ctx context.Context, jobID int64) ([]adminFinding, bool) {
	stored, err := a.store.ListFindings(ctx, jobID)
	if err != nil || len(stored) == 0 {
		return nil, false
	}
	findings := make([]adminFinding, 0, len(stored))
	for _, finding := range stored {
		findings = append(findings, adminFinding{Path: finding.Path, Content: finding.Content, SuggestionCode: finding.SuggestionCode, ExistingCode: finding.ExistingCode, StartLine: finding.StartLine, EndLine: finding.EndLine, Category: finding.Category, Severity: finding.Severity, Status: finding.Status})
	}
	return findings, true
}

func (a *adminAPI) reportURL(job store.ReviewJob) string {
	if job.SessionID == "" || a.cfg.Review.ViewerURL == "" || a.gl == nil {
		return ""
	}
	metadata := adminReview{}
	a.enrichProject(context.Background(), &metadata, job.TargetProjectID)
	if metadata.ProjectName == "" {
		return ""
	}
	project := gitlab.Project{ID: job.TargetProjectID, Name: metadata.ProjectName}
	absDataDir, err := filepath.Abs(a.cfg.DataDir)
	if err != nil {
		return ""
	}
	repoDir := filepath.Join(absDataDir, "workspaces", workspace.WorkloadName(project))
	return (&publisher.Publisher{ViewerURL: a.cfg.Review.ViewerURL}).ReportURL(job.SessionID, repoDir)
}

func coverageData(result review.Result) adminCoverageData {
	data := adminCoverageData{}
	if result.Manifest != nil {
		data.SelectedFiles = manifestPaths(result.Manifest.Coverage.Selected)
		data.CompletedFiles = manifestPaths(result.Manifest.Coverage.Completed)
		data.ReusedFiles = manifestPaths(result.Manifest.Coverage.Reused)
		data.FailedFiles = manifestPaths(result.Manifest.Coverage.Failed)
		data.WaivedFiles = manifestPaths(result.Manifest.Coverage.Waived)
	}
	data.Affected = append([]string(nil), result.AffectedFiles...)
	data.adminCoverage = adminCoverage{Selected: len(data.SelectedFiles), Completed: len(data.CompletedFiles), Reused: len(data.ReusedFiles), Failed: len(data.FailedFiles), Waived: len(data.WaivedFiles), Complete: !review.CoverageIncomplete(result)}
	return data
}
func manifestPaths(items []review.ManifestItem) []string {
	paths := make([]string, 0, len(items))
	for _, item := range items {
		if item.Path != "" {
			paths = append(paths, item.Path)
		}
	}
	return paths
}

func findingCounts(result review.Result, blocking []string) adminFindingCount {
	count := adminFindingCount{Total: len(result.Comments)}
	blockingSet := map[string]struct{}{}
	for _, severity := range blocking {
		blockingSet[strings.ToLower(strings.TrimSpace(severity))] = struct{}{}
	}
	for _, comment := range result.Comments {
		if _, ok := blockingSet[strings.ToLower(strings.TrimSpace(comment.Severity))]; ok {
			count.Blocking++
		}
	}
	return count
}

func ruleStatus(job store.ReviewJob) string {
	if job.RuleSHA256 != "" {
		return "valid"
	}
	if job.State == store.StateRejectedRuleInvalid {
		return "invalid"
	}
	return "missing_default_used"
}

func publicationState(job store.ReviewJob) string {
	switch job.State {
	case store.StatePublishing:
		return "publishing"
	case store.StateCompletedPass, store.StateCompletedFail:
		return "published"
	default:
		return "not_started"
	}
}

func usageRange(r *http.Request) (time.Time, time.Time) {
	now := time.Now().UTC()
	from := now.Add(-30 * 24 * time.Hour)
	to := now.Add(time.Second)
	if value := r.URL.Query().Get("from"); value != "" {
		if parsed, err := time.Parse("2006-01-02", value); err == nil {
			from = parsed.UTC()
		}
	}
	if value := r.URL.Query().Get("to"); value != "" {
		if parsed, err := time.Parse("2006-01-02", value); err == nil {
			to = parsed.UTC().Add(24 * time.Hour)
		}
	}
	return from, to
}

func writeAdminError(w http.ResponseWriter, status int, code string, err error) {
	message := map[string]string{
		"invalid_password":        "密码至少需要 10 位字符",
		"setup_failed":            "创建超管失败：账户名或邮箱可能已存在，或邮箱格式无效",
		"invalid_credentials":     "账户名、邮箱或密码错误",
		"authentication_required": "请先登录",
		"superadmin_required":     "只有超管可以执行此操作",
	}[code]
	if message == "" {
		message = code
	}
	if err != nil && os.Getenv("OCR_ADMIN_VERBOSE_ERRORS") == "1" {
		message = err.Error()
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func configuredStatus(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return "not_configured"
		}
	}
	return "configured"
}
func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func parseInt64Query(value string) int64 {
	parsed, _ := strconv.ParseInt(value, 10, 64)
	return parsed
}

func parseIntDefault(value string, fallback int) int {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func methodNotAllowed(w http.ResponseWriter) {
	w.WriteHeader(http.StatusMethodNotAllowed)
}

func withAdminSecurity(next http.Handler, adminToken, configuredRole string, auth *authManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protected := strings.HasPrefix(r.URL.Path, "/api/v1/admin/") || strings.HasPrefix(r.URL.Path, "/api/v1/reviews/")
		publicAuth := strings.HasPrefix(r.URL.Path, "/api/v1/auth/")
		if protected {
			requestID := r.Header.Get("X-Request-ID")
			if requestID == "" {
				requestID = time.Now().UTC().Format("20060102T150405.000000000Z")
			}
			w.Header().Set("X-Request-ID", requestID)
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			if auth != nil && auth.cfg.Auth.Enabled {
				if auth.initErr != nil {
					writeAdminError(w, http.StatusServiceUnavailable, "authentication_unavailable", auth.initErr)
					return
				}
				user, err := auth.authenticate(r)
				if err != nil {
					writeAdminError(w, http.StatusUnauthorized, "authentication_required", nil)
					return
				}
				if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
					writeAdminError(w, http.StatusForbidden, "csrf_header_required", nil)
					return
				}
				role := "operator"
				if user.Role == store.UserRoleSuperadmin {
					role = "admin"
				}
				ctx := context.WithValue(r.Context(), adminRoleKey, role)
				ctx = context.WithValue(ctx, adminActorKey, user.Username)
				ctx = context.WithValue(ctx, authUserKey, user)
				r = r.WithContext(ctx)
			} else {
				if adminToken != "" {
					provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
					if provided == "" {
						if cookie, err := r.Cookie("ocr_admin_token"); err == nil {
							provided = cookie.Value
						}
					}
					if subtle.ConstantTimeCompare([]byte(provided), []byte(adminToken)) != 1 {
						writeAdminError(w, http.StatusUnauthorized, "admin_auth_required", nil)
						return
					}
					if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("Authorization") == "" && r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
						writeAdminError(w, http.StatusForbidden, "csrf_header_required", nil)
						return
					}
				}
				role := normalizeAdminRole(configuredRole)
				actor := strings.TrimSpace(r.Header.Get("X-Authenticated-User"))
				if actor == "" {
					actor = "admin-token"
				}
				ctx := context.WithValue(r.Context(), adminRoleKey, role)
				ctx = context.WithValue(ctx, adminActorKey, actor)
				r = r.WithContext(ctx)
			}
		} else if publicAuth {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
		}
		next.ServeHTTP(w, r)
	})
}

func normalizeAdminRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "viewer", "operator", "admin", "auditor":
		return strings.ToLower(strings.TrimSpace(role))
	default:
		return "admin"
	}
}

func adminRole(ctx context.Context) string {
	if role, ok := ctx.Value(adminRoleKey).(string); ok && role != "" {
		return role
	}
	return "admin"
}

func adminActor(ctx context.Context) string {
	if actor, ok := ctx.Value(adminActorKey).(string); ok && actor != "" {
		return actor
	}
	return "admin-ui"
}

func adminPermissions(role string) []string {
	read := []string{"review.read", "quality.read", "usage.read", "system.read"}
	switch role {
	case "operator":
		return append(read, "review.retry", "review.cancel", "review.priority")
	case "admin":
		return append(read, "review.retry", "review.cancel", "review.priority", "user.manage", "config.manage", "audit.read", "system.reconcile")
	case "auditor":
		return []string{"review.read", "quality.read", "usage.read", "audit.read"}
	default:
		return read
	}
}
