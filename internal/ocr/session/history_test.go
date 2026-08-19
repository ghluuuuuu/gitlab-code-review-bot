package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/llm"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/model"
)

func TestSessionIDAppendsExistingHistory(t *testing.T) {
	UseTestSessions()
	repoDir := filepath.Join(t.TempDir(), "repo")
	first := New(repoDir, "main", "model", SessionOptions{Operation: OperationReview})
	firstID := first.SessionID
	firstFile, err := SessionFilePath(repoDir, firstID)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(firstFile)

	content := "first response"
	firstRecord := first.GetOrCreateFileSession("a.go").AppendTaskRecord(MainTask, nil)
	firstRecord.SetResponse(&llm.ChatResponse{
		Model:   "model",
		Choices: []llm.Choice{{Message: llm.ResponseMessage{Role: "assistant", Content: &content}}},
		Usage:   &llm.UsageInfo{PromptTokens: 10, CompletionTokens: 3},
	}, time.Millisecond)
	if err := first.Finalize(); err != nil {
		t.Fatal(err)
	}

	before, err := os.ReadFile(firstFile)
	if err != nil {
		t.Fatal(err)
	}

	second := New(repoDir, "main", "model", SessionOptions{SessionID: firstID, Operation: OperationReview})
	secondContent := "second response"
	secondRecord := second.GetOrCreateFileSession("b.go").AppendTaskRecord(MainTask, nil)
	secondRecord.SetResponse(&llm.ChatResponse{
		Model:   "model",
		Choices: []llm.Choice{{Message: llm.ResponseMessage{Role: "assistant", Content: &secondContent}}},
		Usage:   &llm.UsageInfo{PromptTokens: 20, CompletionTokens: 7},
	}, time.Millisecond)
	if err := second.Finalize(); err != nil {
		t.Fatal(err)
	}

	after, err := os.ReadFile(firstFile)
	if err != nil {
		t.Fatal(err)
	}
	if firstID != second.SessionID {
		t.Fatalf("session ID changed from %q to %q", firstID, second.SessionID)
	}
	if !strings.HasPrefix(string(after), string(before)) {
		t.Fatal("incremental session did not preserve the original report records")
	}
	if got := strings.Count(string(after), `"type":"llm_response"`); got != 2 {
		t.Fatalf("llm response records = %d, want 2", got)
	}
}

func TestLoadResumeStateRetainsCommentsAfterReusedCheckpointAppend(t *testing.T) {
	UseTestSessions()
	repoDir := filepath.Join(t.TempDir(), "repo")
	first := New(repoDir, "main", "model", SessionOptions{Operation: OperationReview})
	comment := model.LlmComment{Path: "a.go", Content: "historical issue"}
	first.RecordReviewItemDone("a.go", "a.go", "a.go", "fp", []model.LlmComment{comment})
	if err := first.Finalize(); err != nil {
		t.Fatal(err)
	}
	second := New(repoDir, "main", "model", SessionOptions{SessionID: first.SessionID, Operation: OperationReview})
	second.RecordReviewItemReused("a.go", "a.go", "a.go", "fp", first.SessionID, []model.LlmComment{comment})
	if err := second.Finalize(); err != nil {
		t.Fatal(err)
	}
	state, err := LoadResumeState(repoDir, first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(state.Items["fp"].Comments); got != 1 {
		t.Fatalf("restored comments = %d, want 1", got)
	}
}
