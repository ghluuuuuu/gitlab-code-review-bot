package session

import (
	"testing"

	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/model"
)

func TestMergeResumeStatesReusesNewestFingerprintAcrossSessions(t *testing.T) {
	newest := &ResumeState{
		SessionID: "session-new",
		Items: map[string]ResumeItem{
			"shared":   {Fingerprint: "shared", Comments: []model.LlmComment{{Path: "shared.go", Content: "new conclusion"}}},
			"new-only": {Fingerprint: "new-only"},
		},
	}
	older := &ResumeState{
		SessionID: "session-old",
		Items: map[string]ResumeItem{
			"shared":   {Fingerprint: "shared", Comments: []model.LlmComment{{Path: "shared.go", Content: "old conclusion"}}},
			"old-only": {Fingerprint: "old-only", SourceSessionID: "session-root"},
		},
	}

	merged := MergeResumeStates(newest, older)
	if merged == nil || merged.SessionID != "session-new" || len(merged.Items) != 3 {
		t.Fatalf("unexpected merged state: %#v", merged)
	}
	shared, ok := merged.Item("shared")
	if !ok || len(shared.Comments) != 1 || shared.Comments[0].Content != "new conclusion" || shared.SourceSessionID != "session-new" {
		t.Fatalf("newest fingerprint did not win: %#v", shared)
	}
	oldOnly, ok := merged.Item("old-only")
	if !ok || oldOnly.SourceSessionID != "session-root" {
		t.Fatalf("original source session was not retained: %#v", oldOnly)
	}
}
