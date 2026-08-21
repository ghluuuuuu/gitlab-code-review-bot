package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/gitlab"
	ocrmcp "github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/mcp"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestNormalizeRepositoryURLIgnoresUserHostAndSuffix(t *testing.T) {
	values := []string{"alice@gitlab.example.com:group/service.git", "bob@gitlab.example.com:group/service.git", "https://gitlab.example.com/group/service.git", "group/service"}
	for _, value := range values {
		if got := normalizeRepositoryURL(value); got != "group/service" {
			t.Fatalf("normalizeRepositoryURL(%q) = %q", value, got)
		}
	}
	nested := "http://192.168.133.125/newlandedu/iotcloud/modern-farm-automation-platform/modern-farm-automation-platform-server.git"
	if got, want := normalizeRepositoryURL(nested), "newlandedu/iotcloud/modern-farm-automation-platform/modern-farm-automation-platform-server"; got != want {
		t.Fatalf("normalizeRepositoryURL(%q) = %q, want %q", nested, got, want)
	}
}

func TestQualityMCPResolvesGitRemoteAndReturnsSuggestions(t *testing.T) {
	gitlabServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v4/projects" {
			_, _ = w.Write([]byte(`[{"id":7,"name":"service","path_with_namespace":"group/service","http_url_to_repo":"https://gitlab.example.com/group/service.git","web_url":"https://gitlab.example.com/group/service"}]`))
			return
		}
		http.NotFound(w, r)
	}))
	defer gitlabServer.Close()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	user, err := st.CreateUser(ctx, store.CreateUserInput{Username: "root", Email: "root@example.com", PasswordHash: "hash", Role: store.UserRoleSuperadmin, Enabled: true, AuthSource: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Enqueue(ctx, store.EnqueueInput{ProjectID: 7, MRIID: 12, Title: "Fix issue", SourceProjectID: 7, SourceBranch: "feature", TargetProjectID: 7, TargetBranch: "main", HeadSHA: "abcdef123456", TargetSHA: "target-sha"}); err != nil {
		t.Fatal(err)
	}
	job, err := st.Claim(ctx, "worker", time.Minute)
	if err != nil || job == nil {
		t.Fatalf("claim job: %#v %v", job, err)
	}
	if err := st.RecordFinding(ctx, store.ReviewFinding{ReviewJobID: job.ID, Path: "src/main.go", StartLine: 12, EndLine: 12, Category: "correctness", Severity: "high", Content: "nil dereference", SuggestionCode: "if value == nil { return }"}); err != nil {
		t.Fatal(err)
	}
	for index := range 100 {
		if err := st.RecordFinding(ctx, store.ReviewFinding{ReviewJobID: job.ID, Path: "src/main.go", StartLine: 20 + index, EndLine: 20 + index, Category: "style", Severity: "low", Content: fmt.Sprintf("issue %d", index)}); err != nil {
			t.Fatal(err)
		}
	}
	auth := &authManager{store: st, gitlab: gitlab.New(gitlabServer.URL, "token", time.Second), cfg: authTestConfig(), permissions: map[string]permissionCacheEntry{}, identities: map[int64]gitLabIdentityCacheEntry{}}
	service := newQualityMCP(st, auth.gitlab, auth)
	toolContext := context.WithValue(ctx, authUserKey, user)
	catalog, _, err := service.getQualityReportCatalog(toolContext, nil, gitProjectInput{RepositoryURL: "alice@gitlab.example.com:group/service.git", Branch: "feature", CommitHash: "abcdef1"})
	if err != nil {
		t.Fatal(err)
	}
	catalogText := catalog.Content[0].(*mcp.TextContent).Text
	if strings.Contains(catalogText, "nil dereference") || strings.Contains(catalogText, "suggestion_code") || !strings.Contains(catalogText, "src/main.go") || !strings.Contains(catalogText, `"total":101`) {
		t.Fatalf("catalog leaked findings or missed summary: %s", catalogText)
	}
	const rawToken = "mcp-test-token"
	if err := st.SetMCPToken(ctx, user.ID, tokenHash(rawToken)); err != nil {
		t.Fatal(err)
	}
	mcpServer := httptest.NewServer(service)
	defer mcpServer.Close()
	client, err := ocrmcp.NewRemoteClient(ctx, "ocr-quality", mcpServer.URL, map[string]string{"Authorization": "Bearer " + rawToken}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if len(client.Tools()) != 2 {
		t.Fatalf("MCP tools = %d, want 2", len(client.Tools()))
	}
	catalogOutput, err := client.CallTool(ctx, "get_quality_report_catalog", map[string]any{"repository_url": "https://gitlab.example.com/group/service.git", "branch": "feature", "commit_hash": "abcdef123456", "limit": 1})
	if err != nil || strings.Contains(catalogOutput, "nil dereference") || !strings.Contains(catalogOutput, "src/main.go") {
		t.Fatalf("MCP catalog output = %q, err = %v", catalogOutput, err)
	}
	output, err := client.CallTool(ctx, "get_quality_file_report", map[string]any{"repository_url": "https://gitlab.example.com/group/service.git", "branch": "feature", "commit_hash": "abcdef123456", "path": "src/main.go", "limit": 1})
	if err != nil || !strings.Contains(output, "nil dereference") || !strings.Contains(output, `"has_more":true`) || !strings.Contains(output, "if value == nil") {
		t.Fatalf("MCP HTTP file output = %q, err = %v", output, err)
	}
}
