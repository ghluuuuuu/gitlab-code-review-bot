package session

import (
	"time"

	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/model"
)

const (
	FindingStatusFixed   = "fixed"
	FindingStatusUnfixed = "unfixed"
	FindingStatusNew     = "new"
)

// ChangedFileScope records the exact file and hunk scope supplied to the
// adjudicator, preventing a closure decision from silently using unrelated code.
type ChangedFileScope struct {
	Path    string   `json:"path"`
	OldPath string   `json:"old_path,omitempty"`
	Hunks   []string `json:"hunks,omitempty"`
}

// FindingReference gives the adjudication model a stable opaque identifier for
// one finding. The identifier is only for correlating structured output; it is
// never used to decide semantic equivalence.
type FindingReference struct {
	ID      string           `json:"id"`
	Comment model.LlmComment `json:"comment"`
}

// FindingDecision is one evidence-backed relation decided by the LLM.
type FindingDecision struct {
	HistoricalID string `json:"historical_id,omitempty"`
	CurrentID    string `json:"current_id,omitempty"`
	Status       string `json:"status"`
	Confidence   string `json:"confidence,omitempty"`
	Reason       string `json:"reason"`
}

// FindingTimeline records the defect changes attributed to one reviewed commit.
type FindingTimeline struct {
	StartSHA          string             `json:"start_sha,omitempty"`
	EndSHA            string             `json:"end_sha,omitempty"`
	ExactRange        string             `json:"exact_range,omitempty"`
	CommitSHA         string             `json:"commit_sha"`
	PreviousSessionID string             `json:"previous_session_id,omitempty"`
	Timestamp         time.Time          `json:"timestamp"`
	ChangedFiles      []ChangedFileScope `json:"changed_files,omitempty"`
	Historical        []FindingReference `json:"historical_findings,omitempty"`
	Current           []FindingReference `json:"current_findings,omitempty"`
	Decisions         []FindingDecision  `json:"decisions"`
	AdjudicationError string             `json:"adjudication_error,omitempty"`
}

// RecordFindingTimeline persists one commit-level closure decision before the
// session_end record.
func (sh *SessionHistory) RecordFindingTimeline(entry FindingTimeline) {
	if sh == nil {
		return
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	if p := sh.persist; p != nil {
		p.WriteFindingTimeline(entry)
	}
}
