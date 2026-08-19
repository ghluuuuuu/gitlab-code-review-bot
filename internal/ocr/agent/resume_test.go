package agent

import (
	"testing"

	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/model"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/session"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/tool"
)

func TestApplyResumeSkipsMatchingHistoricalFingerprint(t *testing.T) {
	reusedDiff := model.Diff{OldPath: "shared.go", NewPath: "shared.go", Diff: "@@ -1 +1 @@\n-old\n+new"}
	newDiff := model.Diff{OldPath: "new.go", NewPath: "new.go", Diff: "@@ -0,0 +1 @@\n+new"}
	fingerprint := reviewItemFingerprint(session.ReviewModeRange, reusedDiff)
	collector := tool.NewCommentCollector()
	historicalComment := model.LlmComment{Path: "shared.go", Content: "historical conclusion"}
	agent := &Agent{
		args: Args{
			ReviewMode:       session.ReviewModeRange,
			CommentCollector: collector,
			Resume: &session.ResumeState{
				SessionID: "latest-history",
				Items: map[string]session.ResumeItem{
					fingerprint: {Fingerprint: fingerprint, SourceSessionID: "mr-1-session", Comments: []model.LlmComment{historicalComment}},
				},
			},
		},
	}

	remaining := agent.applyResume([]model.Diff{reusedDiff, newDiff})
	if len(remaining) != 1 || remaining[0].NewPath != "new.go" {
		t.Fatalf("matching historical file was dispatched again: %#v", remaining)
	}
	comments := collector.Comments()
	if len(comments) != 1 || comments[0].Content != historicalComment.Content {
		t.Fatalf("historical conclusion was not reused: %#v", comments)
	}
	info := agent.ResumeInfo()
	if info == nil || info.ReusedFiles != 1 || info.RerunFiles != 1 {
		t.Fatalf("unexpected reuse accounting: %#v", info)
	}
}
