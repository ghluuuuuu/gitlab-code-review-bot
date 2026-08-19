package worker

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/gitlab"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/publisher"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/review"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/store"
)

func TestFinishInfraPublishesReviewNoteWithoutCommitStatus(t *testing.T) {
	st, job := activeJob(t)
	defer st.Close()

	var notePosts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/statuses/") {
			t.Fatalf("worker must not update GitLab commit status: %s %s", r.Method, r.URL.Path)
		}
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/notes"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[]`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/notes"):
			notePosts++
			w.WriteHeader(http.StatusCreated)
		default:
			t.Fatalf("unexpected GitLab request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := gitlab.New(server.URL, "token", time.Second)
	w := &Worker{Store: st, GitLab: client, Publisher: &publisher.Publisher{GitLab: client}}
	w.finishInfra(context.Background(), job, context.DeadlineExceeded)

	if notePosts != 1 {
		t.Fatalf("note posts = %d, want 1", notePosts)
	}
	finished, err := st.GetJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.State != store.StateFailedInfra {
		t.Fatalf("state = %q, want %q", finished.State, store.StateFailedInfra)
	}
}

func TestFinishRuleFailurePublishesReviewNoteWithoutCommitStatus(t *testing.T) {
	st, job := activeJob(t)
	defer st.Close()

	var notePosts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/statuses/") {
			t.Fatalf("worker must not update GitLab commit status: %s %s", r.Method, r.URL.Path)
		}
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/notes"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[]`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/notes"):
			notePosts++
			w.WriteHeader(http.StatusCreated)
		default:
			t.Fatalf("unexpected GitLab request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := gitlab.New(server.URL, "token", time.Second)
	w := &Worker{Store: st, GitLab: client, Publisher: &publisher.Publisher{GitLab: client}}
	w.finishRuleFailure(context.Background(), job, store.StateRejectedRuleInvalid, "invalid rule")

	if notePosts != 1 {
		t.Fatalf("note posts = %d, want 1", notePosts)
	}
	finished, err := st.GetJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.State != store.StateRejectedRuleInvalid {
		t.Fatalf("state = %q, want %q", finished.State, store.StateRejectedRuleInvalid)
	}
}

func activeJob(t *testing.T) (*store.Store, *store.ReviewJob) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "worker.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err = st.Enqueue(ctx, store.EnqueueInput{
		ProjectID: 105, MRIID: 7, SourceProjectID: 105, TargetProjectID: 105,
		SourceBranch: "feature", TargetBranch: "main", HeadSHA: "head", TargetSHA: "target-1",
	})
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	job, err := st.Claim(ctx, "worker-test", time.Minute)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	return st, job
}

func TestReviewRetryPolicyBacksOffConfigurationErrorsAndBoundsTransientFailures(t *testing.T) {
	configurationErr := &review.LLMPreflightHTTPError{StatusCode: http.StatusUnauthorized, Body: "Invalid token"}
	delay, exhausted := reviewRetryPolicy(configurationErr, 100)
	if exhausted || delay != 10*time.Minute {
		t.Fatalf("configuration retry policy = %s exhausted=%v", delay, exhausted)
	}
	delay, exhausted = reviewRetryPolicy(context.DeadlineExceeded, 1)
	if exhausted || delay != 15*time.Second {
		t.Fatalf("first transient retry policy = %s exhausted=%v", delay, exhausted)
	}
	if _, exhausted = reviewRetryPolicy(context.DeadlineExceeded, maxTransientReviewAttempts); !exhausted {
		t.Fatal("transient retries must stop at the attempt limit")
	}
}
