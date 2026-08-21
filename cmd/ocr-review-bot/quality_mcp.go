package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/gitlab"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/review"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxMCPFileSummaries    = 200
	defaultMCPFileLimit    = 100
	maxMCPFileFindings     = 100
	defaultMCPFindingLimit = 50
)

type qualityMCP struct {
	store  *store.Store
	gitlab *gitlab.Client
	auth   *authManager
	server *mcp.Server
	http   http.Handler
}

type gitProjectInput struct {
	RepositoryURL string `json:"repository_url"`
	Branch        string `json:"branch,omitempty"`
	CommitHash    string `json:"commit_hash,omitempty"`
	Offset        int    `json:"offset,omitempty"`
	Limit         int    `json:"limit,omitempty"`
}

type fileQualityReportInput struct {
	RepositoryURL string `json:"repository_url"`
	Branch        string `json:"branch,omitempty"`
	CommitHash    string `json:"commit_hash,omitempty"`
	Path          string `json:"path"`
	Offset        int    `json:"offset,omitempty"`
	Limit         int    `json:"limit,omitempty"`
}

type qualityReportMetadata struct {
	RepositoryURL string `json:"repository_url"`
	ProjectID     int64  `json:"project_id"`
	ReviewJobID   int64  `json:"review_job_id"`
	MRIID         int64  `json:"mr_iid"`
	Title         string `json:"title"`
	SourceBranch  string `json:"source_branch"`
	TargetBranch  string `json:"target_branch"`
	HeadSHA       string `json:"head_sha"`
	TargetSHA     string `json:"target_sha"`
	State         string `json:"state"`
	Stage         string `json:"stage"`
}

type qualityReportCatalog struct {
	qualityReportMetadata
	Summary        map[string]int   `json:"summary"`
	SeverityCounts map[string]int   `json:"severity_counts"`
	CategoryCounts map[string]int   `json:"category_counts"`
	Files          []mcpFileSummary `json:"files"`
	Offset         int              `json:"offset"`
	Limit          int              `json:"limit"`
	OmittedFiles   int              `json:"omitted_files,omitempty"`
	HasMore        bool             `json:"has_more"`
	NextOffset     int              `json:"next_offset,omitempty"`
}

type mcpFileSummary struct {
	Path           string         `json:"path"`
	FindingCount   int            `json:"finding_count"`
	BlockingCount  int            `json:"blocking_count"`
	SeverityCounts map[string]int `json:"severity_counts"`
	CategoryCounts map[string]int `json:"category_counts"`
}

type qualityFileReport struct {
	qualityReportMetadata
	Path            string         `json:"path"`
	Summary         map[string]int `json:"summary"`
	SeverityCounts  map[string]int `json:"severity_counts"`
	CategoryCounts  map[string]int `json:"category_counts"`
	Findings        []mcpFinding   `json:"findings"`
	Offset          int            `json:"offset"`
	Limit           int            `json:"limit"`
	OmittedFindings int            `json:"omitted_findings,omitempty"`
	HasMore         bool           `json:"has_more"`
	NextOffset      int            `json:"next_offset,omitempty"`
}

type mcpFinding struct {
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

func newQualityMCP(st *store.Store, gl *gitlab.Client, auth *authManager) *qualityMCP {
	service := &qualityMCP{store: st, gitlab: gl, auth: auth}
	service.server = mcp.NewServer(&mcp.Implementation{Name: "ocr-quality", Title: "OCR Code Quality", Version: "1.0.0"}, &mcp.ServerOptions{Instructions: "You are connected to a Git-aware code quality MCP service. Before calling any tool, run git remote get-url origin, git branch --show-current, and git rev-parse HEAD in the user's current workspace. First call get_quality_report_catalog to obtain the current reviewed revision's status and the bounded list of affected files. Only call get_quality_file_report for a repository-relative file selected from that catalog. Both tools accept repository_url, branch, and commit_hash from those Git commands; the file tool also requires git ls-files --full-name -- <file>. Project matching compares only the GitLab namespace path; never invent a GitLab project ID. Catalog and file reports are paginated and never return all issue details in one response."})
	mcp.AddTool(service.server, &mcp.Tool{
		Name:        "get_quality_report_catalog",
		Description: "Progressive first step: get the current reviewed revision's quality status, aggregate counts, and a bounded catalog of affected files. It intentionally does not return issue descriptions or code. Run git remote get-url origin, git branch --show-current, and git rev-parse HEAD first, then pass repository_url, branch, and commit_hash. Use offset and limit to page through files; limit is at most 200. Do not call a file report until this catalog identifies the target path.",
	}, service.getQualityReportCatalog)
	mcp.AddTool(service.server, &mcp.Tool{
		Name:        "get_quality_file_report",
		Description: "Progressive second step: get bounded issue details for one repository-relative file listed by get_quality_report_catalog. Run git remote get-url origin, git branch --show-current, git rev-parse HEAD, and git ls-files --full-name -- <file> first. Pass repository_url, branch, commit_hash, and path. Use offset and limit to page findings; limit is at most 100. Never use this tool to request a whole-project report.",
	}, service.getQualityFileReport)
	service.http = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return service.server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	return service
}

func (s *qualityMCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		http.Error(w, "MCP authentication is unavailable", http.StatusServiceUnavailable)
		return
	}
	user, err := s.auth.authenticateMCP(r)
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="ocr-quality-mcp"`)
		http.Error(w, "MCP bearer token required", http.StatusUnauthorized)
		return
	}
	ctx := context.WithValue(r.Context(), authUserKey, user)
	s.http.ServeHTTP(w, r.WithContext(ctx))
}

func (s *qualityMCP) getQualityReportCatalog(ctx context.Context, _ *mcp.CallToolRequest, input gitProjectInput) (*mcp.CallToolResult, any, error) {
	project, user, err := s.resolveProject(ctx, input.RepositoryURL)
	if err != nil {
		return nil, nil, err
	}
	if !s.auth.canAccessProject(ctx, user, project.ID) {
		return nil, nil, errors.New("the authenticated user has no access to this GitLab project")
	}
	job, err := s.latestJob(ctx, project.ID, input.Branch, input.CommitHash)
	if err != nil {
		return nil, nil, err
	}
	return s.catalogForJob(ctx, project, job, input.Offset, input.Limit)
}

func (s *qualityMCP) getQualityFileReport(ctx context.Context, _ *mcp.CallToolRequest, input fileQualityReportInput) (*mcp.CallToolResult, any, error) {
	input.Path = strings.TrimSpace(strings.ReplaceAll(input.Path, "\\", "/"))
	input.Path = filepath.ToSlash(filepath.Clean(input.Path))
	if input.Path == "." || input.Path == ".." || input.Path == "" || filepath.IsAbs(input.Path) || strings.HasPrefix(input.Path, "../") {
		return nil, nil, errors.New("path must be a repository-relative file path")
	}
	project, user, err := s.resolveProject(ctx, input.RepositoryURL)
	if err != nil {
		return nil, nil, err
	}
	if !s.auth.canAccessProject(ctx, user, project.ID) {
		return nil, nil, errors.New("the authenticated user has no access to this GitLab project")
	}
	job, err := s.latestJob(ctx, project.ID, input.Branch, input.CommitHash)
	if err != nil {
		return nil, nil, err
	}
	return s.fileReportForJob(ctx, project, job, input.Path, input.Offset, input.Limit)
}

func (s *qualityMCP) resolveProject(ctx context.Context, repositoryURL string) (gitlab.Project, store.AppUser, error) {
	user, ok := currentAuthUser(ctx)
	if !ok {
		return gitlab.Project{}, store.AppUser{}, errors.New("MCP user authentication is missing")
	}
	normalized := normalizeRepositoryURL(repositoryURL)
	if normalized == "" {
		return gitlab.Project{}, user, errors.New("repository_url is required; run `git remote get-url origin`")
	}
	projects, err := s.gitlab.ListProjects(ctx)
	if err != nil {
		return gitlab.Project{}, user, fmt.Errorf("list GitLab projects: %w", err)
	}
	for _, project := range projects {
		if normalized == normalizeRepositoryURL(project.PathWithNamespace) || normalized == normalizeRepositoryURL(project.HTTPURLToRepository) || normalized == normalizeRepositoryURL(project.WebURL) {
			return project, user, nil
		}
	}
	return gitlab.Project{}, user, errors.New("no GitLab project matches repository_url; verify `git remote get-url origin`")
}

func normalizeRepositoryURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Scheme != "" && parsed.Path != "" {
		value = parsed.Path
	} else {
		if at := strings.LastIndex(value, "@"); at >= 0 {
			value = value[at+1:]
		}
		if colon := strings.Index(value, ":"); colon >= 0 && !strings.Contains(value[:colon], "/") {
			value = value[colon+1:]
		}
	}
	return strings.ToLower(strings.TrimSuffix(strings.Trim(strings.TrimSpace(value), "/"), ".git"))
}

func (s *qualityMCP) latestJob(ctx context.Context, projectID int64, branch, commitHash string) (*store.ReviewJob, error) {
	branch = strings.TrimSpace(branch)
	commitHash = strings.TrimSpace(commitHash)
	jobs, err := s.store.ListAllReviews(ctx)
	if err != nil {
		return nil, err
	}
	var latest *store.ReviewJob
	for index := range jobs {
		job := &jobs[index]
		if job.TargetProjectID != projectID {
			continue
		}
		if branch != "" && job.SourceBranch != branch && job.TargetBranch != branch {
			continue
		}
		if commitHash != "" && !sameGitHash(job.HeadSHA, commitHash) && !sameGitHash(job.TargetSHA, commitHash) {
			continue
		}
		if latest == nil || job.QueuedAt.After(latest.QueuedAt) {
			latest = job
		}
	}
	if latest == nil {
		return nil, fmt.Errorf("no reviewed revision matches branch %q and commit %q: %w", branch, commitHash, sql.ErrNoRows)
	}
	return latest, nil
}

func sameGitHash(left, right string) bool {
	left, right = strings.ToLower(strings.TrimSpace(left)), strings.ToLower(strings.TrimSpace(right))
	return left != "" && right != "" && (left == right || (len(left) >= 7 && strings.HasPrefix(right, left)) || (len(right) >= 7 && strings.HasPrefix(left, right)))
}

func (s *qualityMCP) findings(ctx context.Context, job *store.ReviewJob) ([]mcpFinding, error) {
	stored, err := s.store.ListFindings(ctx, job.ID)
	if err != nil {
		return nil, err
	}
	result := make([]mcpFinding, 0, len(stored))
	for _, finding := range stored {
		result = append(result, mcpFinding{Path: finding.Path, Content: finding.Content, SuggestionCode: finding.SuggestionCode, ExistingCode: finding.ExistingCode, StartLine: finding.StartLine, EndLine: finding.EndLine, Category: finding.Category, Severity: finding.Severity, Status: finding.Status})
	}
	if len(result) == 0 && job.ArtifactDir != "" {
		if parsed, parseErr := review.ParseResult(filepath.Join(job.ArtifactDir, "ocr-result.json")); parseErr == nil {
			for _, finding := range parsed.Comments {
				result = append(result, mcpFinding{Path: finding.Path, Content: finding.Content, SuggestionCode: finding.SuggestionCode, ExistingCode: finding.ExistingCode, StartLine: finding.StartLine, EndLine: finding.EndLine, Category: finding.Category, Severity: finding.Severity, Status: "current"})
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Path != result[j].Path {
			return result[i].Path < result[j].Path
		}
		return result[i].StartLine < result[j].StartLine
	})
	return result, nil
}

func reportMetadata(project gitlab.Project, job *store.ReviewJob) qualityReportMetadata {
	return qualityReportMetadata{
		RepositoryURL: project.HTTPURLToRepository,
		ProjectID:     project.ID,
		ReviewJobID:   job.ID,
		MRIID:         job.MRIID,
		Title:         job.Title,
		SourceBranch:  job.SourceBranch,
		TargetBranch:  job.TargetBranch,
		HeadSHA:       job.HeadSHA,
		TargetSHA:     job.TargetSHA,
		State:         job.State,
		Stage:         job.Stage,
	}
}

func (s *qualityMCP) catalogForJob(ctx context.Context, project gitlab.Project, job *store.ReviewJob, offset, limit int) (*mcp.CallToolResult, any, error) {
	all, err := s.findings(ctx, job)
	if err != nil {
		return nil, nil, err
	}
	severityCounts, categoryCounts := findingDimensions(all)
	byPath := make(map[string]*mcpFileSummary)
	ensureFile := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		if _, exists := byPath[path]; !exists {
			byPath[path] = &mcpFileSummary{Path: path, SeverityCounts: make(map[string]int), CategoryCounts: make(map[string]int)}
		}
	}
	for _, path := range job.Files {
		ensureFile(path)
	}
	for _, finding := range all {
		ensureFile(finding.Path)
		file := byPath[finding.Path]
		file.FindingCount++
		severity := strings.ToLower(strings.TrimSpace(finding.Severity))
		category := strings.ToLower(strings.TrimSpace(finding.Category))
		if severity == "" {
			severity = "unknown"
		}
		if category == "" {
			category = "other"
		}
		file.SeverityCounts[severity]++
		file.CategoryCounts[category]++
		if severity == "critical" || severity == "high" {
			file.BlockingCount++
		}
	}
	files := make([]mcpFileSummary, 0, len(byPath))
	for _, file := range byPath {
		files = append(files, *file)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	start, end, omitted, hasMore, nextOffset, err := mcpPage(offset, limit, defaultMCPFileLimit, maxMCPFileSummaries, len(files))
	if err != nil {
		return nil, nil, err
	}
	result := qualityReportCatalog{
		qualityReportMetadata: reportMetadata(project, job),
		Summary:               summarizeMCPFindings(all, len(all)),
		SeverityCounts:        severityCounts,
		CategoryCounts:        categoryCounts,
		Files:                 files[start:end],
		Offset:                start,
		Limit:                 end - start,
		OmittedFiles:          omitted,
		HasMore:               hasMore,
		NextOffset:            nextOffset,
	}
	return textResult(result)
}

func (s *qualityMCP) fileReportForJob(ctx context.Context, project gitlab.Project, job *store.ReviewJob, path string, offset, limit int) (*mcp.CallToolResult, any, error) {
	all, err := s.findings(ctx, job)
	if err != nil {
		return nil, nil, err
	}
	selected := make([]mcpFinding, 0)
	for _, finding := range all {
		if finding.Path == path {
			selected = append(selected, finding)
		}
	}
	severityCounts, categoryCounts := findingDimensions(selected)
	start, end, omitted, hasMore, nextOffset, err := mcpPage(offset, limit, defaultMCPFindingLimit, maxMCPFileFindings, len(selected))
	if err != nil {
		return nil, nil, err
	}
	result := qualityFileReport{
		qualityReportMetadata: reportMetadata(project, job),
		Path:                  path,
		Summary:               summarizeMCPFindings(selected, len(selected)),
		SeverityCounts:        severityCounts,
		CategoryCounts:        categoryCounts,
		Findings:              selected[start:end],
		Offset:                start,
		Limit:                 end - start,
		OmittedFindings:       omitted,
		HasMore:               hasMore,
		NextOffset:            nextOffset,
	}
	return textResult(result)
}

func findingDimensions(findings []mcpFinding) (map[string]int, map[string]int) {
	severityCounts := make(map[string]int)
	categoryCounts := make(map[string]int)
	for _, finding := range findings {
		severity := strings.ToLower(strings.TrimSpace(finding.Severity))
		if severity == "" {
			severity = "unknown"
		}
		category := strings.ToLower(strings.TrimSpace(finding.Category))
		if category == "" {
			category = "other"
		}
		severityCounts[severity]++
		categoryCounts[category]++
	}
	return severityCounts, categoryCounts
}

func mcpPage(offset, limit, defaultLimit, maxLimit, total int) (int, int, int, bool, int, error) {
	if offset < 0 || limit < 0 {
		return 0, 0, 0, false, 0, errors.New("offset and limit must not be negative")
	}
	if limit == 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		return 0, 0, 0, false, 0, fmt.Errorf("limit must not exceed %d", maxLimit)
	}
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	hasMore := end < total
	nextOffset := 0
	if hasMore {
		nextOffset = end
	}
	return offset, end, total - end, hasMore, nextOffset, nil
}

func summarizeMCPFindings(findings []mcpFinding, selectedTotal int) map[string]int {
	result := map[string]int{"total": selectedTotal, "blocking": 0}
	for _, finding := range findings {
		severity := strings.ToLower(strings.TrimSpace(finding.Severity))
		if severity == "critical" || severity == "high" {
			result["blocking"]++
		}
	}
	return result
}

func textResult(value any) (*mcp.CallToolResult, any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, nil, fmt.Errorf("encode MCP result: %w", err)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, value, nil
}
