package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/config"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/gitlab"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/review"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/store"
)

func TestLoadQualityFilesIncludesDiffStatsAndAuthors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v4/projects/1/merge_requests/2/diffs":
			_ = json.NewEncoder(w).Encode([]gitlab.MergeRequestDiff{{OldPath: "src/a.go", NewPath: "src/a.go", Diff: "--- a/src/a.go\n+++ b/src/a.go\n@@ -1,2 +1,3 @@\n-old\n+new\n+added\n context"}})
		case "/api/v4/projects/1/merge_requests/2":
			_ = json.NewEncoder(w).Encode(gitlab.MergeRequest{Author: gitlab.User{ID: 7, Name: "Alice", Username: "alice", AvatarURL: "https://gitlab.example.com/avatar.png", WebURL: "https://gitlab.example.com/alice"}})
		case "/api/v4/projects/1/merge_requests/2/commits":
			_ = json.NewEncoder(w).Encode([]gitlab.Commit{{ID: "commit-1", AuthorName: "Alice", AuthorEmail: "alice@example.com"}})
		case "/api/v4/projects/1/repository/commits/commit-1/diff":
			_ = json.NewEncoder(w).Encode([]gitlab.MergeRequestDiff{{OldPath: "src/a.go", NewPath: "src/a.go"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	files, err := loadQualityFiles(context.Background(), gitlab.New(server.URL, "token", time.Second), 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "src/a.go" || files[0].Additions != 2 || files[0].Deletions != 1 {
		t.Fatalf("unexpected quality files: %#v", files)
	}
	if len(files[0].Authors) != 1 || files[0].Authors[0].Username != "alice" || files[0].Authors[0].AvatarURL == "" {
		t.Fatalf("file author was not resolved: %#v", files[0].Authors)
	}
}

func TestLoadQualityProjectsAndAllMergeRequests(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(dataDir, "quality.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, err = st.Enqueue(ctx, store.EnqueueInput{
		ProjectID: 1, MRIID: 2, SourceProjectID: 1, TargetProjectID: 1,
		SourceBranch: "feature", TargetBranch: "main", HeadSHA: "head", TargetSHA: "target",
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := st.Claim(ctx, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetLLMMetadata(ctx, job.ID, "ocr-bot", "model", "session-1"); err != nil {
		t.Fatal(err)
	}
	artifactDir := filepath.Join(dataDir, "artifacts", "1")
	if err := review.WriteResult(filepath.Join(artifactDir, "ocr-result.json"), review.Result{ChangeAnalysis: "### 涉及的功能模块\n设备模块\n\n### 运维配置更新\n更新 COMMAND_TIMEOUT", Comments: []review.Comment{
		{Path: "a.go", Category: "security", Severity: "high"}, {Path: "b.go", Category: "security", Severity: "medium"}, {Path: "c.go", Category: "performance", Severity: "low"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Finish(ctx, job.ID, store.StateCompletedFail, "", artifactDir, store.Usage{}); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v4/projects":
			_ = json.NewEncoder(w).Encode([]gitlab.Project{{ID: 1, Name: "service", PathWithNamespace: "group/service", WebURL: "https://gitlab.example.com/group/service"}})
		case "/api/v4/projects/1/languages":
			_, _ = w.Write([]byte(`{"Go":71.2,"Vue":28.8}`))
		case "/api/v4/projects/1":
			_ = json.NewEncoder(w).Encode(gitlab.Project{ID: 1, Name: "service", PathWithNamespace: "group/service", WebURL: "https://gitlab.example.com/group/service"})
		case "/api/v4/projects/1/merge_requests":
			_ = json.NewEncoder(w).Encode([]gitlab.MergeRequest{
				{ProjectID: 1, IID: 3, Title: "Unreviewed change", State: "opened", WebURL: "https://gitlab.example.com/mr/3", SourceBranch: "other", TargetBranch: "main", UpdatedAt: "2026-08-15T12:00:00Z"},
				{ProjectID: 1, IID: 2, Title: "Improve service", State: "merged", WebURL: "https://gitlab.example.com/mr/2", SourceBranch: "feature", TargetBranch: "main", UpdatedAt: "2026-08-14T12:00:00Z", Author: gitlab.User{ID: 7, Name: "Alice", Username: "alice", AvatarURL: "avatar", WebURL: "profile"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := config.Default()
	cfg.DataDir = dataDir
	cfg.Review.ViewerURL = "http://viewer"
	projects, err := loadQualityProjects(ctx, gitlab.New(server.URL, "token", time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].PathWithNamespace != "group/service" || projects[0].TechStack != "Go" {
		t.Fatalf("visible projects and primary technology must load independently of reviews: %#v", projects)
	}
	var mr qualityMR
	mrs, err := loadQualityMergeRequests(ctx, st, gitlab.New(server.URL, "token", time.Second), cfg, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(mrs) != 2 || mrs[0].MRIID != 3 || mrs[0].Reviewed || mrs[0].State != "opened" {
		t.Fatalf("all project merge requests must be returned: %#v", mrs)
	}
	mr = mrs[1]
	if !mr.Reviewed || mr.State != "merged" || mr.IssueCounts["security"] != 2 || mr.IssueCounts["performance"] != 1 || mr.FileIssueCounts["a.go"] != 1 || mr.FileIssueCounts["b.go"] != 1 || mr.FileIssueCounts["c.go"] != 1 || mr.FileBlockingCounts["a.go"] != 1 || mr.FileBlockingCounts["b.go"] != 0 || !strings.Contains(mr.ChangeAnalysis, "COMMAND_TIMEOUT") || mr.Author.Username != "alice" || mr.ReportURL == "" {
		t.Fatalf("reviewed MR quality metadata incomplete: %#v", mr)
	}
}

func TestLoadFixTrendTracksCurrentAndCumulativeFixedIssues(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(dataDir, "trend.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	reviews := [][]review.Comment{
		{{Path: "a.go", StartLine: 10, Category: "correctness", Content: "A"}, {Path: "b.go", StartLine: 20, Category: "security", Content: "B"}},
		{{Path: "b.go", StartLine: 20, Category: "security", Content: "B"}, {Path: "c.go", StartLine: 30, Category: "performance", Content: "C"}},
		{},
	}
	for i, comments := range reviews {
		head := fmt.Sprintf("head-%d", i+1)
		if _, err := st.Enqueue(ctx, store.EnqueueInput{ProjectID: 1, MRIID: 2, SourceProjectID: 1, TargetProjectID: 1, SourceBranch: "feature", TargetBranch: "main", HeadSHA: head, TargetSHA: "target"}); err != nil {
			t.Fatal(err)
		}
		job, err := st.Claim(ctx, "worker", time.Minute)
		if err != nil || job == nil {
			t.Fatalf("claim review %d: job=%#v err=%v", i, job, err)
		}
		artifactDir := filepath.Join(dataDir, "artifacts", fmt.Sprint(i+1))
		if err := review.WriteResult(filepath.Join(artifactDir, "ocr-result.json"), review.Result{Comments: comments}); err != nil {
			t.Fatal(err)
		}
		if err := st.Finish(ctx, job.ID, store.StateCompletedFail, "", artifactDir, store.Usage{Comments: int64(len(comments))}); err != nil {
			t.Fatal(err)
		}
	}
	points, err := loadFixTrend(ctx, st, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 3 || points[0].IssueCount != 2 || points[0].FixedCount != 0 || points[1].IssueCount != 2 || points[1].FixedCount != 1 || points[2].IssueCount != 0 || points[2].FixedCount != 3 {
		t.Fatalf("unexpected fix trend: %#v", points)
	}
}

func TestLoadQualityFileDetailIncludesCodeAndLineFindings(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "file-detail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Enqueue(ctx, store.EnqueueInput{ProjectID: 1, MRIID: 2, SourceProjectID: 3, TargetProjectID: 1, SourceBranch: "feature", TargetBranch: "main", HeadSHA: "head", TargetSHA: "target"}); err != nil {
		t.Fatal(err)
	}
	job, err := st.Claim(ctx, "worker", time.Minute)
	if err != nil || job == nil {
		t.Fatalf("claim review: %#v %v", job, err)
	}
	if err := st.RecordFinding(ctx, store.ReviewFinding{ReviewJobID: job.ID, Path: "src/main.go", StartLine: 2, EndLine: 3, Category: "correctness", Severity: "high", Content: "nil access", SuggestionCode: "if value == nil { return }"}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v4/projects/1/merge_requests/2":
			_ = json.NewEncoder(w).Encode(gitlab.MergeRequest{IID: 2, SHA: "head", SourceProjectID: 3, SourceBranch: "feature"})
		case "/api/v4/projects/3/repository/files/src/main.go":
			if r.URL.Query().Get("ref") != "head" {
				t.Fatalf("file ref = %q", r.URL.Query().Get("ref"))
			}
			_ = json.NewEncoder(w).Encode(gitlab.RepositoryFile{FilePath: "src/main.go", Encoding: "base64", Content: "cGFja2FnZSBtYWluCgpmdW5jIG1haW4oKSB7fQo="})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	detail, err := loadQualityFileDetail(ctx, st, gitlab.New(server.URL, "token", time.Second), 1, 2, "src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Path != "src/main.go" || detail.Ref != "head" || !strings.Contains(detail.Content, "func main") || len(detail.Findings) != 1 || detail.Findings[0].StartLine != 2 || detail.Findings[0].SuggestionCode == "" {
		t.Fatalf("file detail is incomplete: %#v", detail)
	}
}

func TestLoadQualityMergeRequestsUsesLiveFindingsBeforeCompletion(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(dataDir, "live-quality.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Enqueue(ctx, store.EnqueueInput{ProjectID: 1, MRIID: 2, SourceProjectID: 1, TargetProjectID: 1, SourceBranch: "feature", TargetBranch: "main", HeadSHA: "head", TargetSHA: "target", Title: "Live review"}); err != nil {
		t.Fatal(err)
	}
	job, err := st.Claim(ctx, "worker", time.Minute)
	if err != nil || job == nil {
		t.Fatalf("claim live review: %#v %v", job, err)
	}
	if err := st.RecordFinding(ctx, store.ReviewFinding{ReviewJobID: job.ID, Path: "internal/live.go", StartLine: 12, EndLine: 12, Category: "security", Severity: "critical", Content: "live issue"}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v4/projects/1":
			_ = json.NewEncoder(w).Encode(gitlab.Project{ID: 1, Name: "service", PathWithNamespace: "group/service"})
		case "/api/v4/projects/1/merge_requests":
			_ = json.NewEncoder(w).Encode([]gitlab.MergeRequest{{ProjectID: 1, IID: 2, Title: "Live review", State: "opened", SourceBranch: "feature", TargetBranch: "main"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := config.Default()
	cfg.DataDir = dataDir
	mrs, err := loadQualityMergeRequests(ctx, st, gitlab.New(server.URL, "token", time.Second), cfg, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(mrs) != 1 {
		t.Fatalf("unexpected live quality MRs: %#v", mrs)
	}
	mr := mrs[0]
	if !mr.Reviewed || mr.SeverityCounts["critical"] != 1 || mr.IssueCounts["security"] != 1 || mr.FileIssueCounts["internal/live.go"] != 1 || mr.FileBlockingCounts["internal/live.go"] != 1 || mr.FileIssueTypeCounts["internal/live.go"]["security"] != 1 {
		t.Fatalf("live finding was not reflected: %#v", mr)
	}
}

func TestLoadProjectBranchGraphIncludesAllBranchesAndMergeDirections(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "branch-graph.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Enqueue(ctx, store.EnqueueInput{ProjectID: 1, MRIID: 8, SourceProjectID: 1, TargetProjectID: 1, SourceBranch: "feature", TargetBranch: "main", HeadSHA: "head", TargetSHA: "target"}); err != nil {
		t.Fatal(err)
	}
	job, err := st.Claim(ctx, "worker", time.Minute)
	if err != nil || job == nil {
		t.Fatalf("claim branch graph job: %#v %v", job, err)
	}
	if err := st.SetProgress(ctx, job.ID, 2, 12); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v4/projects/1/repository/branches":
			_ = json.NewEncoder(w).Encode([]gitlab.Branch{{Name: "feature"}, {Name: "main"}, {Name: "release"}})
		case "/api/v4/projects/1/merge_requests":
			_ = json.NewEncoder(w).Encode([]gitlab.MergeRequest{{IID: 8, Title: "Feature merge", SourceBranch: "feature", TargetBranch: "main", State: "merged"}, {IID: 7, Title: "Release merge", SourceBranch: "release", TargetBranch: "main", State: "opened"}})
		case "/api/v4/projects/1/merge_requests/8/diffs":
			_ = json.NewEncoder(w).Encode([]gitlab.MergeRequestDiff{{NewPath: "a.go"}, {NewPath: "b.go"}})
		case "/api/v4/projects/1/merge_requests/7/diffs":
			_ = json.NewEncoder(w).Encode([]gitlab.MergeRequestDiff{{NewPath: "release.go"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	graph, err := loadProjectBranchGraph(ctx, st, gitlab.New(server.URL, "token", time.Second), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.Branches) != 3 || len(graph.Relations) != 4 || graph.Relations[0].Source != "feature" || graph.Relations[0].Target != "main" || graph.Relations[0].ChangedFiles != 2 {
		t.Fatalf("unexpected branch graph: %#v", graph)
	}
	forkPairs := map[string]bool{}
	for _, relation := range graph.Relations {
		if relation.Kind == "fork" {
			forkPairs[relation.Source+"->"+relation.Target] = true
		}
	}
	if !forkPairs["main->feature"] || !forkPairs["main->release"] {
		t.Fatalf("MR target branches must expose fork-source relations: %#v", graph.Relations)
	}
	for _, branch := range graph.Branches {
		if branch.Name == "main" && branch.ChangedFiles != 3 {
			t.Fatalf("target branch complexity = %d, want 3 from incoming diffs", branch.ChangedFiles)
		}
		if branch.Name == "release" && branch.OpenMergeRequests != 1 {
			t.Fatalf("release open merge requests = %d, want 1", branch.OpenMergeRequests)
		}
	}
}

func TestLoadProjectBranchGraphInfersDirectGitRelationsWithoutMRs(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "git-relations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	featureSHA, stagingSHA, mainSHA := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	branch := func(name, sha string) gitlab.Branch {
		result := gitlab.Branch{Name: name}
		result.Commit.ID = sha
		return result
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v4/projects/2/repository/branches":
			_ = json.NewEncoder(w).Encode([]gitlab.Branch{branch("feature", featureSHA), branch("staging", stagingSHA), branch("main", mainSHA)})
		case "/api/v4/projects/2/merge_requests":
			_ = json.NewEncoder(w).Encode([]gitlab.MergeRequest{})
		case "/api/v4/projects/2/repository/commits/" + featureSHA + "/refs":
			_ = json.NewEncoder(w).Encode([]gitlab.CommitRef{{Type: "branch", Name: "feature"}, {Type: "branch", Name: "staging"}, {Type: "branch", Name: "main"}})
		case "/api/v4/projects/2/repository/commits/" + stagingSHA + "/refs":
			_ = json.NewEncoder(w).Encode([]gitlab.CommitRef{{Type: "branch", Name: "staging"}, {Type: "branch", Name: "main"}})
		case "/api/v4/projects/2/repository/commits/" + mainSHA + "/refs":
			_ = json.NewEncoder(w).Encode([]gitlab.CommitRef{{Type: "branch", Name: "main"}})
		case "/api/v4/projects/2/repository/commits/" + featureSHA + "/sequence":
			_ = json.NewEncoder(w).Encode(gitlab.CommitSequence{Count: 10})
		case "/api/v4/projects/2/repository/commits/" + stagingSHA + "/sequence":
			_ = json.NewEncoder(w).Encode(gitlab.CommitSequence{Count: 20})
		case "/api/v4/projects/2/repository/commits/" + mainSHA + "/sequence":
			_ = json.NewEncoder(w).Encode(gitlab.CommitSequence{Count: 30})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	graph, err := loadProjectBranchGraph(ctx, st, gitlab.New(server.URL, "token", time.Second), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.Relations) != 2 || graph.Relations[0].Source != "feature" || graph.Relations[0].Target != "staging" || graph.Relations[1].Source != "staging" || graph.Relations[1].Target != "main" {
		t.Fatalf("unexpected direct Git relations: %#v", graph.Relations)
	}
	for _, relation := range graph.Relations {
		if relation.Kind != "git" || relation.MRIID != 0 {
			t.Fatalf("Git relation metadata is invalid: %#v", relation)
		}
	}
}

func TestInferGitBranchRelationsKeepsOnlyNearestContainingBranch(t *testing.T) {
	shas := map[string]string{
		"b": strings.Repeat("d", 40),
		"a": strings.Repeat("e", 40),
		"c": strings.Repeat("f", 40),
		"d": strings.Repeat("1", 40),
	}
	branches := make([]gitlab.Branch, 0, len(shas))
	for _, name := range []string{"b", "a", "c", "d"} {
		branch := gitlab.Branch{Name: name}
		branch.Commit.ID = shas[name]
		branches = append(branches, branch)
	}
	sequences := map[string]int64{"b": 10, "a": 20, "c": 30, "d": 40}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		for name, sha := range shas {
			if r.URL.Path == "/api/v4/projects/3/repository/commits/"+sha+"/sequence" {
				_ = json.NewEncoder(w).Encode(gitlab.CommitSequence{Count: sequences[name]})
				return
			}
			if r.URL.Path == "/api/v4/projects/3/repository/commits/"+sha+"/refs" {
				refs := []gitlab.CommitRef{{Type: "branch", Name: name}}
				if name == "b" {
					refs = append(refs, gitlab.CommitRef{Type: "branch", Name: "a"}, gitlab.CommitRef{Type: "branch", Name: "c"}, gitlab.CommitRef{Type: "branch", Name: "d"})
				}
				_ = json.NewEncoder(w).Encode(refs)
				return
			}
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	relations, err := inferGitBranchRelations(context.Background(), gitlab.New(server.URL, "token", time.Second), 3, branches)
	if err != nil {
		t.Fatal(err)
	}
	if len(relations) != 1 || relations[0].Source != "b" || relations[0].Target != "a" {
		t.Fatalf("nearest Git relation = %#v, want only b -> a", relations)
	}
}
