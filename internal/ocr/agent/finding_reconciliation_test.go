package agent

import (
	"testing"

	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/model"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/session"
)

func TestValidateReconciliationRequiresCompleteSafeDecisions(t *testing.T) {
	historical := []session.FindingReference{{ID: "old-1", Comment: model.LlmComment{Path: "a.go", Content: "old"}}}
	current := []session.FindingReference{{ID: "new-1", Comment: model.LlmComment{Path: "a.go", Content: "new"}}}

	decisions := validateReconciliation([]session.FindingDecision{{
		HistoricalID: "old-1", CurrentID: "new-1", Status: session.FindingStatusFixed,
	}}, historical, current)
	if len(decisions) != 2 {
		t.Fatalf("decisions = %#v, want conservative fallback for both findings", decisions)
	}
	if decisions[0].Status != session.FindingStatusUnfixed || decisions[1].Status != session.FindingStatusNew {
		t.Fatalf("decisions = %#v, want unfixed + new", decisions)
	}
}

func TestConservativeReconciliationNeverMarksHistoricalFindingFixed(t *testing.T) {
	entry := conservativeReconciliation(session.FindingTimeline{
		Historical: []session.FindingReference{{ID: "old-1"}},
		Current:    []session.FindingReference{{ID: "new-1"}},
	}, "adjudication failed")
	if entry.Decisions[0].Status != session.FindingStatusUnfixed {
		t.Fatalf("historical status = %q, want unfixed", entry.Decisions[0].Status)
	}
}
