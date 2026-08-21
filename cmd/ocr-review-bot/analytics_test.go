package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/gitlab"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/store"
)

func TestLoadAdminAnalyticsAggregatesProjectsContributorsAndQuality(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "analytics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	jobs := []struct {
		projectID int64
		mrIID     int64
		state     string
		finding   store.ReviewFinding
	}{
		{projectID: 1, mrIID: 11, state: store.StateCompletedPass, finding: store.ReviewFinding{Path: "a.go", Severity: "critical", Category: "security", Content: "unsafe"}},
		{projectID: 2, mrIID: 22, state: store.StateCompletedFail, finding: store.ReviewFinding{Path: "b.go", Severity: "medium", Category: "correctness", Content: "wrong"}},
	}
	for index, value := range jobs {
		if _, err := st.Enqueue(ctx, store.EnqueueInput{ProjectID: value.projectID, TargetProjectID: value.projectID, SourceProjectID: value.projectID, MRIID: value.mrIID, SourceBranch: "feature", TargetBranch: "main", HeadSHA: value.finding.Path, TargetSHA: "target"}); err != nil {
			t.Fatal(err)
		}
		job, err := st.Claim(ctx, "analytics-worker", time.Minute)
		if err != nil || job == nil {
			t.Fatalf("claim job %d: %#v %v", index, job, err)
		}
		value.finding.ReviewJobID = job.ID
		if err := st.RecordFinding(ctx, value.finding); err != nil {
			t.Fatal(err)
		}
		if err := st.Finish(ctx, job.ID, value.state, "", "", store.Usage{Comments: 1}); err != nil {
			t.Fatal(err)
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v4/projects":
			_ = json.NewEncoder(w).Encode([]gitlab.Project{{ID: 1, Name: "One", PathWithNamespace: "group/one", WebURL: "https://gitlab/one"}, {ID: 2, Name: "Two", PathWithNamespace: "group/two", WebURL: "https://gitlab/two"}})
		case "/api/v4/projects/1/repository/commits":
			assertAnalyticsCommitQuery(t, r)
			_ = json.NewEncoder(w).Encode([]gitlab.Commit{{ID: "1", AuthorName: "Alice", AuthorEmail: "alice@example.com", CommittedDate: "2026-08-20T08:00:00Z", Stats: gitlab.CommitStats{Additions: 10, Deletions: 2, Total: 12}}, {ID: "2", AuthorName: "Bob", AuthorEmail: "bob@example.com", CommittedDate: "2026-08-20T09:00:00Z", Stats: gitlab.CommitStats{Additions: 5, Deletions: 1, Total: 6}}})
		case "/api/v4/projects/2/repository/commits":
			assertAnalyticsCommitQuery(t, r)
			_ = json.NewEncoder(w).Encode([]gitlab.Commit{{ID: "3", AuthorName: "Alice", AuthorEmail: "ALICE@example.com", CommittedDate: "2026-08-21T08:00:00Z", Stats: gitlab.CommitStats{Additions: 20, Deletions: 3, Total: 23}}})
		case "/api/v4/users":
			query := strings.ToLower(r.URL.Query().Get("search"))
			switch {
			case strings.Contains(query, "alice"):
				_ = json.NewEncoder(w).Encode([]gitlab.User{{ID: 101, Username: "alice", Name: "Alice", PublicEmail: "alice@example.com", WebURL: "https://gitlab/alice"}})
			case strings.Contains(query, "bob"):
				_ = json.NewEncoder(w).Encode([]gitlab.User{{ID: 102, Username: "bob", Name: "Bob", PublicEmail: "bob@example.com", WebURL: "https://gitlab/bob"}})
			default:
				_ = json.NewEncoder(w).Encode([]gitlab.User{})
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	from, to := time.Now().UTC().Add(-24*time.Hour), time.Now().UTC().Add(24*time.Hour)
	report, err := loadAdminAnalytics(ctx, st, gitlab.New(server.URL, "token", time.Second), from, to, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.ProjectCount != 2 || report.Summary.UpdatedProjects != 2 || report.Summary.CommitCount != 3 || report.Summary.ContributorCount != 2 {
		t.Fatalf("activity summary = %#v", report.Summary)
	}
	if report.Summary.ReviewCount != 2 || report.Summary.PassedReviews != 1 || report.Summary.FailedReviews != 1 || report.Summary.FindingCount != 2 || report.Summary.BlockingFindings != 1 || report.Quality.PassRate != 50 {
		t.Fatalf("quality summary = %#v quality=%#v", report.Summary, report.Quality)
	}
	if report.Quality.SeverityCounts["critical"] != 1 || report.Quality.CategoryCounts["correctness"] != 1 {
		t.Fatalf("quality dimensions = %#v %#v", report.Quality.SeverityCounts, report.Quality.CategoryCounts)
	}
	if len(report.Contributors) != 2 || report.Contributors[0].UserID != 101 || report.Contributors[0].IdentitySource != "gitlab_user" || report.Contributors[0].Name != "Alice" || report.Contributors[0].ChangedLines != 35 || report.Contributors[0].AddedLines != 30 || report.Contributors[0].DeletedLines != 5 || report.Contributors[0].ProjectCount != 2 {
		t.Fatalf("contributors = %#v", report.Contributors)
	}
}

func TestResolveContributorIdentitiesMergesAliasesByGitLabUserID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/users" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]gitlab.User{{ID: 734, Username: "guohao", Name: "郭豪 陆", PublicEmail: "734976028@qq.com", WebURL: "https://gitlab/guohao"}})
	}))
	defer server.Close()
	rows := []projectCommitAnalytics{{commits: []gitlab.Commit{
		{ID: "1", AuthorName: "郭豪 陆", AuthorEmail: "734976028@qq.com"},
		{ID: "2", AuthorName: "guohao", AuthorEmail: "12345+guohao@users.noreply.github.com"},
		{ID: "3", AuthorName: "郭豪 陆", AuthorEmail: "\x7f734976028@qq.com"},
	}}}
	identities := resolveContributorIdentities(context.Background(), gitlab.New(server.URL, "token", time.Second), rows)
	keys := make(map[string]struct{})
	for _, commit := range rows[0].commits {
		identity := identities[commitIdentityKey(commit)]
		if identity.userID != 734 || identity.key != "user:734" {
			t.Fatalf("identity for %#v = %#v", commit, identity)
		}
		keys[identity.key] = struct{}{}
	}
	if len(keys) != 1 {
		t.Fatalf("aliases resolved to %d contributors", len(keys))
	}
}

func TestAnalyticsProjectGroupsFilterByNamespace(t *testing.T) {
	projects := []gitlab.Project{{ID: 1, PathWithNamespace: "newlandedu/iotcloud/a"}, {ID: 2, PathWithNamespace: "newlandedu/iotcloud/b"}, {ID: 3, PathWithNamespace: "newlandedu/component-services/c"}}
	groups := analyticsProjectGroups(projects)
	if len(groups) != 3 || groups[0].Path != "newlandedu" || groups[1].Path != "newlandedu/component-services" || groups[2].ProjectCount != 2 {
		t.Fatalf("groups = %#v", groups)
	}
	filtered := filterAnalyticsProjects(projects, []string{"newlandedu/iotcloud"})
	if len(filtered) != 2 || filtered[0].ID != 1 || filtered[1].ID != 2 {
		t.Fatalf("filtered projects = %#v", filtered)
	}
	if len(filterAnalyticsProjects(projects, nil)) != 3 {
		t.Fatal("empty selection should include all projects")
	}
}

func TestNormalizeIdentityEmailMergesHiddenWhitespaceAndControls(t *testing.T) {
	left := normalizeIdentityEmail("734976028@qq.com")
	right := normalizeIdentityEmail("\u200b734976028@qq.com\u200d")
	if left != right || contributorFallbackKey("one", left) != contributorFallbackKey("two", right) {
		t.Fatalf("normalized aliases differ: %q vs %q", left, right)
	}
}

func TestContributorFallbackKeepsDifferentAnonymousNamesSeparate(t *testing.T) {
	if contributorFallbackKey("陆郭豪", "") == contributorFallbackKey("郭豪 陆", "") {
		t.Fatal("anonymous names without an email must remain separate")
	}
	if contributorFallbackKey("尧锋 郭", "same@example.com") != contributorFallbackKey("郭尧锋", "SAME@example.com") {
		t.Fatal("same email aliases must merge")
	}
}

func TestCommitIdentityKeyMergesDifferentNamesWithSameEmail(t *testing.T) {
	left := gitlab.Commit{AuthorName: "尧锋 郭", AuthorEmail: "same@example.com"}
	right := gitlab.Commit{AuthorName: "郭尧锋", AuthorEmail: "SAME@example.com"}
	if commitIdentityKey(left) != commitIdentityKey(right) {
		t.Fatalf("same-email aliases split: %q vs %q", commitIdentityKey(left), commitIdentityKey(right))
	}
}

func TestResolveContributorDoesNotTrustUnmatchedUniqueSearchResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/users" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]gitlab.User{{ID: 25, Username: "yangkq", Name: "杨克强"}})
	}))
	defer server.Close()
	identity := resolveContributorIdentity(context.Background(), gitlab.New(server.URL, "token", time.Second), gitlab.Commit{AuthorName: "unknown", AuthorEmail: "734976028@qq.com"})
	if identity.userID != 0 || identity.source != "commit" {
		t.Fatalf("unmatched search result was trusted: %#v", identity)
	}
}

func TestAdminAnalyticsRequiresSuperadminAndValidRange(t *testing.T) {
	api := &adminAPI{}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/analytics", nil)
	request = request.WithContext(context.WithValue(request.Context(), adminRoleKey, "viewer"))
	response := httptest.NewRecorder()
	api.handleAnalytics(response, request)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "superadmin_required") {
		t.Fatalf("viewer response = %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/analytics?from=2026-08-21&to=2026-08-20", nil)
	request = request.WithContext(context.WithValue(request.Context(), adminRoleKey, "admin"))
	response = httptest.NewRecorder()
	api.handleAnalytics(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_analytics_range") {
		t.Fatalf("invalid range response = %d %s", response.Code, response.Body.String())
	}
}

func assertAnalyticsCommitQuery(t *testing.T, r *http.Request) {
	t.Helper()
	query := r.URL.Query()
	if query.Get("all") != "true" || query.Get("per_page") != "100" || query.Get("with_stats") != "true" || query.Get("since") == "" || query.Get("until") == "" {
		t.Fatalf("commit analytics query = %s", r.URL.RawQuery)
	}
}
