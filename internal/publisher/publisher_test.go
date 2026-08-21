package publisher

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/gitlab"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/review"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/store"
)

func TestSelectPublishedFindingsKeepsTwentyMostSevere(t *testing.T) {
	comments := make([]review.Comment, 0, 25)
	for index := range 25 {
		severity := "low"
		if index == 20 {
			severity = "critical"
		}
		if index == 21 {
			severity = "high"
		}
		comments = append(comments, review.Comment{Path: fmt.Sprintf("file-%02d.go", index), Severity: severity, Content: fmt.Sprintf("issue-%02d", index)})
	}
	selected, omitted := selectPublishedFindings(comments)
	if len(selected) != maxPublishedFindings || len(omitted) != 5 {
		t.Fatalf("selected=%d omitted=%d, want 20 and 5", len(selected), len(omitted))
	}
	if selected[0].Severity != "critical" || selected[1].Severity != "high" {
		t.Fatalf("highest severities were not retained first: %#v", selected[:2])
	}
	for _, comment := range omitted {
		if comment.Content == "issue-20" || comment.Content == "issue-21" {
			t.Fatalf("highest severity was omitted: %#v", comment)
		}
	}
}

func TestResetBotFindingCommentsDeletesExistingMRNotesAndDiscussions(t *testing.T) {
	var deletedNotes, deletedDiscussions int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/discussions"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[{"id":"bot-topic","notes":[{"body":"<!-- ocr-live-finding:105:7:old-head:a.go:10 -->"}]},{"id":"human-topic","notes":[{"body":"human"}]}]`)
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/discussions/bot-topic"):
			deletedDiscussions++
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/notes"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[{"id":11,"body":"<!-- ocr-live-finding:105:7:old-head:b.go:20 -->"},{"id":12,"body":"human"}]`)
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/notes/11"):
			deletedNotes++
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	p := &Publisher{GitLab: gitlab.New(server.URL, "token", time.Second)}
	job := &store.ReviewJob{ProjectID: 105, MRIID: 7, HeadSHA: "new-head"}
	if err := p.resetBotFindingComments(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if deletedNotes != 1 || deletedDiscussions != 1 {
		t.Fatalf("deleted notes=%d discussions=%d, want 1 each", deletedNotes, deletedDiscussions)
	}
}

func TestPublishLimitsGitLabFindingNotesAndExplainsReport(t *testing.T) {
	var findingPosts int
	var conclusionBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/notes"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[]`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/discussions"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[]`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/notes"):
			body := readFormBody(t, r).Get("body")
			if strings.Contains(body, "<!-- ocr-live-finding:") {
				findingPosts++
			} else {
				conclusionBody = body
			}
			w.WriteHeader(http.StatusCreated)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	comments := make([]review.Comment, 0, 25)
	for index := range 25 {
		comments = append(comments, review.Comment{Content: fmt.Sprintf("issue-%d", index), Severity: "low"})
	}
	p := &Publisher{GitLab: gitlab.New(server.URL, "token", time.Second), ViewerURL: "https://reviews.example.com"}
	job := &store.ReviewJob{ProjectID: 105, TargetProjectID: 105, MRIID: 7, HeadSHA: "head", TargetBranch: "main"}
	if err := p.Publish(context.Background(), job, review.Result{Status: "complete", Comments: comments, SessionID: "session"}, false, false); err != nil {
		t.Fatal(err)
	}
	if findingPosts != maxPublishedFindings {
		t.Fatalf("published finding notes = %d, want %d", findingPosts, maxPublishedFindings)
	}
	if !strings.Contains(conclusionBody, "其余 5 个问题未发布为评论") || !strings.Contains(conclusionBody, "审查报告") || !strings.Contains(conclusionBody, "https://reviews.example.com/quality?mr_iid=7&project_id=105") {
		t.Fatalf("conclusion did not explain omitted findings: %q", conclusionBody)
	}
}

func TestPublishLiveFindingUsesRegularNoteWhenGitLabRejectsLineCode(t *testing.T) {
	var discussionRequests, noteRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/notes"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[]`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/versions"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[{"base_commit_sha":"base","start_commit_sha":"start","head_commit_sha":"head"}]`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/discussions"):
			discussionRequests++
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"message":"400 Bad request - Note {:line_code=>[\"must be a valid line code\"]}"}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/notes"):
			noteRequests++
			body := readFormBody(t, r)
			if !strings.Contains(body.Get("body"), "已作为普通评论发布") {
				t.Fatalf("fallback note did not explain regular-note publication: %q", body.Get("body"))
			}
			w.WriteHeader(http.StatusCreated)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	p := &Publisher{GitLab: gitlab.New(server.URL, "token", time.Second)}
	job := &store.ReviewJob{ProjectID: 105, MRIID: 7, HeadSHA: "head"}
	err := p.PublishLiveFinding(context.Background(), job, review.Comment{Path: "main.go", StartLine: 99, EndLine: 99, Content: "issue"})
	if err != nil {
		t.Fatalf("PublishLiveFinding returned fallback error: %v", err)
	}
	if discussionRequests != 1 || noteRequests != 1 {
		t.Fatalf("requests: discussions=%d notes=%d, want 1 each", discussionRequests, noteRequests)
	}
}

func TestPublishLiveFindingUsesRegularNoteForNonPositionDiscussionError(t *testing.T) {
	var noteRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/notes"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[]`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/versions"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[{"base_commit_sha":"base","start_commit_sha":"start","head_commit_sha":"head"}]`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/discussions"):
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"message":"server failed"}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/notes"):
			noteRequests++
			w.WriteHeader(http.StatusCreated)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	p := &Publisher{GitLab: gitlab.New(server.URL, "token", time.Second)}
	job := &store.ReviewJob{ProjectID: 105, MRIID: 7, HeadSHA: "head"}
	if err := p.PublishLiveFinding(context.Background(), job, review.Comment{Path: "main.go", StartLine: 99, Content: "issue"}); err != nil {
		t.Fatal(err)
	}
	if noteRequests != 1 {
		t.Fatalf("regular note requests = %d, want 1", noteRequests)
	}
}

func TestPublishLiveFindingUsesRegularNoteWhenGitLabReturnsGenericBadRequest(t *testing.T) {
	var noteRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/notes"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[]`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/versions"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[{"base_commit_sha":"base","start_commit_sha":"start","head_commit_sha":"head"}]`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/discussions"):
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `Bad Request`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/notes"):
			noteRequests++
			body := readFormBody(t, r)
			if !strings.Contains(body.Get("body"), "已作为普通评论发布") {
				t.Fatalf("generic 400 fallback did not publish regular note: %q", body.Get("body"))
			}
			w.WriteHeader(http.StatusCreated)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	p := &Publisher{GitLab: gitlab.New(server.URL, "token", time.Second)}
	job := &store.ReviewJob{ProjectID: 105, MRIID: 7, HeadSHA: "head"}
	if err := p.PublishLiveFinding(context.Background(), job, review.Comment{Path: "main.go", StartLine: 99, Content: "issue"}); err != nil {
		t.Fatalf("PublishLiveFinding returned generic 400 fallback error: %v", err)
	}
	if noteRequests != 1 {
		t.Fatalf("fallback note requests = %d, want 1", noteRequests)
	}
}

func TestPublishProgressIncludesLiveTokenUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/notes"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[]`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/notes"):
			body := readFormBody(t, r).Get("body")
			if !strings.Contains(body, "Token：168（输入 123 / 输出 45）") {
				t.Fatalf("progress note missing live token usage: %q", body)
			}
			w.WriteHeader(http.StatusCreated)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	p := &Publisher{GitLab: gitlab.New(server.URL, "token", time.Second)}
	job := &store.ReviewJob{ProjectID: 105, MRIID: 7, HeadSHA: "head"}
	if err := p.PublishProgress(context.Background(), job, review.Progress{
		Kind: review.ProgressStarted, Message: "正在审核", InputTokens: 123, OutputTokens: 45, TotalTokens: 168,
	}); err != nil {
		t.Fatalf("PublishProgress returned error: %v", err)
	}
}

func TestPublishDeletesProgressAndCreatesLatestHeadConclusion(t *testing.T) {
	var deletedNotes, ordinaryNotes, createdConclusion int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/notes"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[
				{"id":11,"body":"<!-- ocr-progress:105:7:new-head -->\n## 代码审查中"},
				{"id":12,"body":"<!-- ocr-summary:105:7 -->\n## 旧审查结论"},
				{"id":13,"body":"<!-- ocr-summary:105:7:new-head -->\n## 本次旧结论"}
			]`)
		case r.Method == http.MethodDelete && (strings.HasSuffix(r.URL.Path, "/notes/11") || strings.HasSuffix(r.URL.Path, "/notes/12") || strings.HasSuffix(r.URL.Path, "/notes/13")):
			deletedNotes++
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/notes"):
			body := readFormBody(t, r).Get("body")
			if strings.Contains(body, "<!-- ocr-live-finding:") {
				ordinaryNotes++
				if !strings.Contains(body, "已作为普通评论发布") || !strings.Contains(body, "FarmAutomationPlatform.Application.xml") || strings.Contains(body, ":0-0") {
					t.Fatalf("ordinary finding body = %q", body)
				}
			} else {
				createdConclusion++
				if !strings.Contains(body, "<!-- ocr-summary:105:7:new-head -->") || !strings.Contains(body, "代码审查结果不通过") || !strings.Contains(body, "本次变更影响分析") || !strings.Contains(body, "运维配置更新") || !strings.Contains(body, "在目标分支 `master` 的仓库根目录中未找到项目审查规则文件") || !strings.Contains(body, "https://reviews.example.com/quality?mr_iid=7&project_id=105") || strings.Contains(body, "未能发布为行级评论的问题") {
					t.Fatalf("latest conclusion body = %q", body)
				}
			}
			w.WriteHeader(http.StatusCreated)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	p := &Publisher{GitLab: gitlab.New(server.URL, "token", time.Second), ViewerURL: "https://reviews.example.com"}
	job := &store.ReviewJob{ProjectID: 105, MRIID: 7, HeadSHA: "new-head", TargetBranch: "master"}
	result := review.Result{Status: "complete", Summary: review.Summary{TotalTokens: 10}, Comments: []review.Comment{{Path: "FarmAutomationPlatform.Application/FarmAutomationPlatform.Application.xml", Category: "maintainability", Severity: "low", Content: "确认返回类型仍为 List"}}, ChangeAnalysis: "### 涉及的功能模块\n设备模块\n\n### 运维配置更新\n新增超时配置\n\n### 建议测试范围\n集成测试"}
	if err := p.Publish(context.Background(), job, result, true, true); err != nil {
		t.Fatal(err)
	}
	if deletedNotes != 3 || ordinaryNotes != 1 || createdConclusion != 1 {
		t.Fatalf("deleted notes = %d, ordinary notes = %d, created conclusion = %d", deletedNotes, ordinaryNotes, createdConclusion)
	}
}

func TestResolveSupersededFindingsClosesReviewedAndGraphAffectedFiles(t *testing.T) {
	var resolved []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/discussions"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[
				{"id":"old-reviewed","notes":[{"body":"<!-- ocr-live-finding:105:7:old-head:a.go:10 -->","resolvable":true,"resolved":false,"position":{"new_path":"a.go"}}]},
				{"id":"old-reused","notes":[{"body":"<!-- ocr-live-finding:105:7:old-head:b.go:20 -->","resolvable":true,"resolved":false,"position":{"new_path":"b.go"}}]},
				{"id":"current-reviewed","notes":[{"body":"<!-- ocr-live-finding:105:7:new-head:a.go:11 -->","resolvable":true,"resolved":false,"position":{"new_path":"a.go"}}]},
				{"id":"human-thread","notes":[{"body":"human comment","resolvable":true,"resolved":false,"position":{"new_path":"a.go"}}]}
			]`)
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/discussions/"):
			body := readFormBody(t, r)
			if body.Get("resolved") != "true" {
				t.Fatalf("resolved form = %q", body.Get("resolved"))
			}
			resolved = append(resolved, r.URL.Path)
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	p := &Publisher{GitLab: gitlab.New(server.URL, "token", time.Second)}
	job := &store.ReviewJob{ProjectID: 105, MRIID: 7, HeadSHA: "new-head"}
	result := review.Result{AffectedFiles: []string{"b.go"}, Manifest: &review.Manifest{Coverage: review.ManifestCoverage{
		Completed: []review.ManifestItem{{Path: "a.go"}},
		Reused:    []review.ManifestItem{{Path: "b.go"}},
	}}}
	if err := p.resolveSupersededFindings(context.Background(), job, result); err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 2 {
		t.Fatalf("resolved discussions = %#v, want reviewed and graph-affected findings", resolved)
	}
	joined := strings.Join(resolved, "\n")
	if !strings.Contains(joined, "/old-reviewed") || !strings.Contains(joined, "/old-reused") {
		t.Fatalf("resolved discussions = %#v", resolved)
	}
}

func TestReviewReportURLUsesViewerBaseForQualityDeepLink(t *testing.T) {
	p := &Publisher{ViewerURL: "https://reviews.example.com/"}
	job := &store.ReviewJob{ProjectID: 105, TargetProjectID: 205, MRIID: 7}
	got := p.ReviewReportURL(job, "session-1", "repo")
	want := "https://reviews.example.com/quality?mr_iid=7&project_id=205"
	if got != want {
		t.Fatalf("quality report URL = %q, want %q", got, want)
	}
	if empty := (&Publisher{}).ReviewReportURL(job, "session-1", "repo"); empty != "" {
		t.Fatalf("empty viewer report URL = %q", empty)
	}
}

func readFormBody(t *testing.T, r *http.Request) url.Values {
	t.Helper()
	if err := r.ParseForm(); err != nil {
		t.Fatal(err)
	}
	if r.Form == nil {
		t.Fatal(fmt.Errorf("missing form body"))
	}
	return r.Form
}
