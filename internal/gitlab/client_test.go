package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGetCurrentUserUsesAuthenticatedUserAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/user" {
			http.NotFound(w, r)
			return
		}
		if token := r.Header.Get("PRIVATE-TOKEN"); token != "test-token" {
			t.Fatalf("PRIVATE-TOKEN = %q", token)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(User{ID: 42, Username: "ocr-review-bot"})
	}))
	defer server.Close()

	client := New(server.URL, "test-token", time.Second)
	user, err := client.GetCurrentUser(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != 42 || user.Username != "ocr-review-bot" {
		t.Fatalf("unexpected current user: %#v", user)
	}
}

func TestGetCurrentUserRejectsMissingID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"username":"missing-id"}`))
	}))
	defer server.Close()

	client := New(server.URL, "test-token", time.Second)
	if _, err := client.GetCurrentUser(context.Background()); err == nil {
		t.Fatal("expected missing current-user id to fail")
	}
}

func TestListProjectsReturnsEveryVisiblePage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/projects" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("simple") != "true" || r.URL.Query().Get("per_page") != "100" {
			t.Fatalf("unexpected projects query: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("X-Next-Page", "2")
			_ = json.NewEncoder(w).Encode([]Project{{ID: 1, PathWithNamespace: "group/a"}})
			return
		}
		_ = json.NewEncoder(w).Encode([]Project{{ID: 2, PathWithNamespace: "group/b"}})
	}))
	defer server.Close()

	projects, err := New(server.URL, "test-token", time.Second).ListProjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 || projects[0].ID != 1 || projects[1].ID != 2 {
		t.Fatalf("visible projects = %#v", projects)
	}
}

func TestGetProjectLanguagesReturnsPercentages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/projects/7/languages" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Go":72.5,"Vue":27.5}`))
	}))
	defer server.Close()

	languages, err := New(server.URL, "test-token", time.Second).GetProjectLanguages(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if languages["Go"] != 72.5 || languages["Vue"] != 27.5 {
		t.Fatalf("project languages = %#v", languages)
	}
}

func TestListCommitBranchRefsReturnsEveryContainingBranch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/projects/1/repository/commits/abc123/refs" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("type") != "branch" || r.URL.Query().Get("per_page") != "100" {
			t.Fatalf("unexpected commit refs query: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("X-Next-Page", "2")
			_ = json.NewEncoder(w).Encode([]CommitRef{{Type: "branch", Name: "feature"}})
			return
		}
		_ = json.NewEncoder(w).Encode([]CommitRef{{Type: "branch", Name: "main"}})
	}))
	defer server.Close()

	refs, err := New(server.URL, "test-token", time.Second).ListCommitBranchRefs(context.Background(), 1, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 || refs[0].Name != "feature" || refs[1].Name != "main" {
		t.Fatalf("commit branch refs = %#v", refs)
	}
}

func TestGetCommitSequenceReturnsRepositoryPosition(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/projects/1/repository/commits/abc123/sequence" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(CommitSequence{Count: 42})
	}))
	defer server.Close()

	count, err := New(server.URL, "test-token", time.Second).GetCommitSequence(context.Background(), 1, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if count != 42 {
		t.Fatalf("commit sequence = %d, want 42", count)
	}
}

func TestGetDiffVersionForHeadSelectsReviewRevision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/projects/1/merge_requests/2/versions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"head_commit_sha":"new-head","base_commit_sha":"base-new","start_commit_sha":"start-new"},
			{"head_commit_sha":"old-head","base_commit_sha":"base-old","start_commit_sha":"start-old"}
		]`))
	}))
	defer server.Close()

	version, err := New(server.URL, "test-token", time.Second).GetDiffVersionForHead(context.Background(), 1, 2, "old-head")
	if err != nil {
		t.Fatal(err)
	}
	if version.HeadCommitSHA != "old-head" || version.BaseCommitSHA != "base-old" || version.StartCommitSHA != "start-old" {
		t.Fatalf("selected diff version = %#v", version)
	}
}
