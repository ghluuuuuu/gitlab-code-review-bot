package tool

import (
	"testing"

	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/model"
)

func TestCommentCollectorCallbackRunsAfterCommit(t *testing.T) {
	var callbackCount int
	var collector *CommentCollector
	collector = NewCommentCollectorWithCallback(func(comment model.LlmComment) {
		callbackCount++
		if got := collectorCommentsCount(collector); got != 1 {
			t.Fatalf("callback observed %d committed comments, want 1", got)
		}
		if comment.Path != "main.go" {
			t.Fatalf("callback path = %q, want main.go", comment.Path)
		}
	})
	collector.Add(model.LlmComment{Path: "main.go", Content: "issue"})
	if callbackCount != 1 {
		t.Fatalf("callback count = %d, want 1", callbackCount)
	}
}

func collectorCommentsCount(collector *CommentCollector) int {
	return len(collector.Comments())
}
