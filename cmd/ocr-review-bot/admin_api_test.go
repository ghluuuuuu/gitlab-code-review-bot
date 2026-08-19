package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/config"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/gitlab"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/store"
)

func TestAdminReviewListDetailAndCancel(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "admin-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Enqueue(ctx, store.EnqueueInput{ProjectID: 1, MRIID: 2, SourceProjectID: 1, TargetProjectID: 1, SourceBranch: "feature", TargetBranch: "main", HeadSHA: "head-1", TargetSHA: "target-1", Title: "Review me", WebURL: "https://gitlab.example/mr/2"}); err != nil {
		t.Fatal(err)
	}
	job, err := st.ListQueue(ctx, 10)
	if err != nil || len(job) != 1 {
		t.Fatalf("queue = %#v, err = %v", job, err)
	}
	cfg := config.Default()
	handler := routes(st, gitlab.New("http://127.0.0.1:1", "token", time.Second), cfg, "")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/admin/reviews?scope=active&page_size=10", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", response.Code, response.Body.String())
	}
	var page struct {
		Items []adminReview `json:"items"`
		Total int           `json:"total"`
	}
	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Title != "Review me" {
		t.Fatalf("unexpected page: %#v", page)
	}
	response = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/reviews/"+strconv.FormatInt(job[0].ID, 10)+"/cancel", bytes.NewBufferString(`{"reason":"operator stop","expected_state":"queued"}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, body = %s", response.Code, response.Body.String())
	}
	updated, err := st.GetJob(ctx, job[0].ID)
	if err != nil || updated == nil || updated.State != store.StateCanceled {
		t.Fatalf("updated job = %#v, err = %v", updated, err)
	}
	audit, err := st.ListAuditEvents(ctx, 10)
	if err != nil || len(audit) != 1 || audit[0].Action != "review.cancel" {
		t.Fatalf("audit events = %#v, err = %v", audit, err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/admin/reviews/export?scope=history", nil))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte("Review me")) {
		t.Fatalf("export status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestAdminTokenProtectsManagementAPIs(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.Default()
	cfg.Server.AdminToken = "secret"
	handler := routes(st, gitlab.New("http://127.0.0.1:1", "token", time.Second), cfg, "")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/admin/reviews", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/reviews", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("authenticated status = %d, body = %s", response.Code, response.Body.String())
	}
	cfg.Server.AdminRole = "viewer"
	handler = routes(st, gitlab.New("http://127.0.0.1:1", "token", time.Second), cfg, "")
	request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/reconcile", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("viewer reconcile status = %d, want %d", response.Code, http.StatusForbidden)
	}
}

func TestDiscoveryStopsWhenDailyTokenBudgetReached(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "budget.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Enqueue(ctx, store.EnqueueInput{ProjectID: 1, MRIID: 2, TargetProjectID: 1, SourceProjectID: 1, TargetBranch: "main", SourceBranch: "feature", HeadSHA: "head", TargetSHA: "target"}); err != nil {
		t.Fatal(err)
	}
	job, err := st.Claim(ctx, "worker", time.Minute)
	if err != nil || job == nil {
		t.Fatalf("claim budget seed job: %#v %v", job, err)
	}
	if err := st.Finish(ctx, job.ID, store.StateCompletedPass, "", "", store.Usage{TotalTokens: 100}); err != nil {
		t.Fatal(err)
	}
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		http.Error(w, "unexpected GitLab call", http.StatusInternalServerError)
	}))
	defer server.Close()
	cfg := config.Default()
	cfg.Review.DailyTokenBudget = 50
	discoverReviews(ctx, gitlab.New(server.URL, "token", time.Second), st, cfg, 1)
	if called {
		t.Fatal("discovery called GitLab after daily token budget was reached")
	}
}
