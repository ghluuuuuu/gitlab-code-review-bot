package llmloop

import (
	"testing"

	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/llm"
)

func TestRecordUsagePublishesAggregateSnapshot(t *testing.T) {
	var snapshots []UsageSnapshot
	runner := NewRunner(Deps{OnUsage: func(usage UsageSnapshot) {
		snapshots = append(snapshots, usage)
	}})

	runner.RecordUsage(&llm.UsageInfo{PromptTokens: 100, CompletionTokens: 20})
	runner.RecordUsage(&llm.UsageInfo{PromptTokens: 50, CompletionTokens: 10})

	if len(snapshots) != 2 {
		t.Fatalf("usage snapshots = %d, want 2", len(snapshots))
	}
	got := snapshots[1]
	if got.InputTokens != 150 || got.OutputTokens != 30 || got.TotalTokens != 180 {
		t.Fatalf("unexpected aggregate usage: %#v", got)
	}
}
