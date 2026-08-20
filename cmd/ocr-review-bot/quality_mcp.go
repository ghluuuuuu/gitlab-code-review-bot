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

const maxMCPFindings = 200

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
}

type fileIssuesInput struct {
	RepositoryURL string `json:"repository_url"`
	Branch        string `json:"branch,omitempty"`
	CommitHash    string `json:"commit_hash,omitempty"`
	Path          string `json:"path"`
}

type qualityMCPResult struct {
	RepositoryURL   string         `json:"repository_url"`
	MRIID           int64          `json:"mr_iid"`
	Title           string         `json:"title"`
	SourceBranch    string         `json:"source_branch"`
	TargetBranch    string         `json:"target_branch"`
	HeadSHA         string         `json:"head_sha"`
	TargetSHA       string         `json:"target_sha"`
	State           string         `json:"state"`
	Findings        []mcpFinding   `json:"findings"`
	Summary         map[string]int `json:"summary"`
	OmittedFindings int            `json:"omitted_findings,omitempty"`
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
	service.server = mcp.NewServer(&mcp.Implementation{Name: "ocr-quality", Title: "OCR Code Quality", Version: "1.0.0"}, &mcp.ServerOptions{Instructions: "You are connected to a Git-aware code quality MCP service. Before calling any tool, run git remote get-url origin, git branch --show-current, and git rev-parse HEAD in the user's current workspace. For get_file_issues also run git ls-files --full-name -- <file> to obtain a repository-relative path. Pass the command outputs as repository_url, branch, commit_hash, and path. Never invent GitLab project IDs or repository paths; use the local Git command output. Use returned line ranges, issue descriptions, existing code, and suggestion_code to guide fixes."})
	mcp.AddTool(service.server, &mcp.Tool{
		Name:        "get_current_branch_issues",
		Description: "Get quality issues for the local Git repository and current branch. Before calling this tool, the coding agent MUST run `git remote get-url origin`, `git branch --show-current`, and `git rev-parse HEAD` in the user's workspace, then pass those outputs as repository_url, branch, and commit_hash. ",
	}, service.getCurrentBranchIssues)
	mcp.AddTool(service.server, &mcp.Tool{
		Name:        "get_file_issues",
		Description: "Get issue locations, descriptions, existing code, and repair suggestions for one repository-relative file at the current Git commit. Before calling this tool, the coding agent MUST run `git remote get-url origin`, `git branch --show-current`, `git rev-parse HEAD`, and determine the repository-relative path (for example with `git ls-files --full-name -- <file>`). Pass those outputs as repository_url, branch, commit_hash, and path. ",
	}, service.getFileIssues)
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

func (s *qualityMCP) getCurrentBranchIssues(ctx context.Context, _ *mcp.CallToolRequest, input gitProjectInput) (*mcp.CallToolResult, any, error) {
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
	return s.resultForJob(ctx, project, job, "")
}

func (s *qualityMCP) getFileIssues(ctx context.Context, _ *mcp.CallToolRequest, input fileIssuesInput) (*mcp.CallToolResult, any, error) {
	input.Path = strings.TrimSpace(strings.ReplaceAll(input.Path, "\\", "/"))
	if input.Path == "" || filepath.IsAbs(input.Path) || strings.HasPrefix(input.Path, "../") {
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
	return s.resultForJob(ctx, project, job, input.Path)
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
		if normalized == normalizeRepositoryURL(project.HTTPURLToRepository) || normalized == normalizeRepositoryURL(project.WebURL) || strings.HasSuffix(normalized, "/"+strings.ToLower(strings.TrimSuffix(project.PathWithNamespace, ".git"))) {
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
	if strings.Contains(value, "://") {
		parsed, err := url.Parse(value)
		if err == nil && parsed.Host != "" {
			return strings.ToLower(strings.TrimSuffix(parsed.Host+"/"+strings.Trim(parsed.Path, "/"), ".git"))
		}
	}
	if at := strings.LastIndex(value, "@"); at >= 0 {
		value = value[at+1:]
	}
	if colon := strings.Index(value, ":"); colon >= 0 && !strings.Contains(value[:colon], "/") {
		value = value[:colon] + "/" + value[colon+1:]
	}
	return strings.ToLower(strings.TrimSuffix(strings.Trim(value, "/"), ".git"))
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

func (s *qualityMCP) resultForJob(ctx context.Context, project gitlab.Project, job *store.ReviewJob, path string) (*mcp.CallToolResult, any, error) {
	all, err := s.findings(ctx, job)
	if err != nil {
		return nil, nil, err
	}
	filtered := make([]mcpFinding, 0, len(all))
	for _, finding := range all {
		if path == "" || finding.Path == path {
			filtered = append(filtered, finding)
		}
	}
	total := len(filtered)
	omitted := 0
	if len(filtered) > maxMCPFindings {
		omitted = len(filtered) - maxMCPFindings
		filtered = filtered[:maxMCPFindings]
	}
	result := qualityMCPResult{RepositoryURL: project.HTTPURLToRepository, MRIID: job.MRIID, Title: job.Title, SourceBranch: job.SourceBranch, TargetBranch: job.TargetBranch, HeadSHA: job.HeadSHA, TargetSHA: job.TargetSHA, State: job.State, Findings: filtered, Summary: summarizeMCPFindings(all, total), OmittedFindings: omitted}
	return textResult(result)
}

func summarizeMCPFindings(findings []mcpFinding, selectedTotal int) map[string]int {
	result := map[string]int{"total": selectedTotal, "blocking": 0}
	for _, finding := range findings {
		if finding.Severity == "critical" || finding.Severity == "high" {
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
