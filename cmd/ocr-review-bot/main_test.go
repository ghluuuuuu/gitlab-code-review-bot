package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/config"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/gitlab"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/store"
)

func TestDiscoverReviewsContinuesSessionWhenTargetRevisionChanges(t *testing.T) {
	targetSHA := "target-1"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v4/user":
			_ = json.NewEncoder(w).Encode(gitlab.User{ID: 9, Username: "ocr-review-bot"})
		case "/api/v4/merge_requests":
			if r.URL.Query().Get("reviewer_id") != "9" {
				t.Errorf("reviewer_id = %q, want authenticated user id", r.URL.Query().Get("reviewer_id"))
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"project_id": 1, "iid": 2, "title": "change", "state": "opened", "draft": false,
				"sha": "head-1", "source_project_id": 1, "target_project_id": 1,
				"source_branch": "feature", "target_branch": "main",
			}})
		case "/api/v4/projects/1":
			_ = json.NewEncoder(w).Encode(gitlab.Project{ID: 1, Name: "service", Description: "service project", PathWithNamespace: "group/platform/service", WebURL: "https://gitlab.example.com/group/platform/service"})
		case "/api/v4/projects/1/repository/branches/main":
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "main", "commit": map[string]string{"id": targetSHA}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.Default()
	client := gitlab.New(server.URL, "token", time.Second)
	ctx := context.Background()
	botUser, err := client.GetCurrentUser(ctx)
	if err != nil {
		t.Fatal(err)
	}

	discoverReviews(ctx, client, st, cfg, botUser.ID)
	oldJob, err := st.Claim(ctx, "worker-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if oldJob == nil || oldJob.TargetSHA != "target-1" {
		t.Fatalf("unexpected initial revision: %#v", oldJob)
	}
	if err := st.SetGitMetadata(ctx, oldJob.ID, "target-1", "base-1", "rule-1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetLLMMetadata(ctx, oldJob.ID, "ocr-bot", "model", "session-1"); err != nil {
		t.Fatal(err)
	}
	if err := st.Finish(ctx, oldJob.ID, store.StateCompletedFail, "", "artifacts", store.Usage{}); err != nil {
		t.Fatal(err)
	}

	targetSHA = "target-2"
	discoverReviews(ctx, client, st, cfg, botUser.ID)
	queue, err := st.ListQueue(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue) != 1 || queue[0].HeadSHA != "head-1" || queue[0].TargetSHA != "target-2" || queue[0].SessionID != "session-1" || queue[0].RuleSHA256 != "rule-1" {
		t.Fatalf("target update did not continue the existing review session: %#v", queue)
	}
	response := httptest.NewRecorder()
	routes(st, client, cfg, "").ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/admin/projects", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("projects API status = %d, body = %s", response.Code, response.Body.String())
	}
	var apiProjects []adminProject
	if err := json.NewDecoder(response.Body).Decode(&apiProjects); err != nil {
		t.Fatal(err)
	}
	if len(apiProjects) != 1 || apiProjects[0].PathWithNamespace != "group/platform/service" || len(apiProjects[0].Reviews) != 1 || apiProjects[0].Reviews[0].TargetSHA != "target-2" {
		t.Fatalf("projects API must expose only the latest MR review: %#v", apiProjects)
	}
}
