package viewer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSessionAggregatesTokenUsageAcrossAppendedRuns(t *testing.T) {
	root := t.TempDir()
	repoDir := filepath.Join(root, "repo")
	if err := os.MkdirAll(repoDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repoDir, "mr-session.jsonl")
	data := `{"type":"session_start","sessionId":"mr-session","timestamp":"2026-08-18T00:00:00Z"}
{"type":"llm_request","filePath":"old.go","taskType":"main_task","request_no":1,"messages":[]}
{"type":"llm_response","filePath":"old.go","taskType":"main_task","usage":{"prompt_tokens":10,"completion_tokens":3},"content":"old"}
{"type":"session_end","sessionId":"mr-session","timestamp":"2026-08-18T00:01:00Z"}
{"type":"session_start","sessionId":"mr-session","timestamp":"2026-08-18T00:02:00Z","resumedFrom":"mr-session"}
{"type":"llm_request","filePath":"new.go","taskType":"main_task","request_no":1,"messages":[]}
{"type":"llm_response","filePath":"new.go","taskType":"main_task","usage":{"prompt_tokens":20,"completion_tokens":7},"content":"new"}
{"type":"session_end","sessionId":"mr-session","timestamp":"2026-08-18T00:03:00Z"}
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}

	report, err := LoadSession(root, "repo", "mr-session")
	if err != nil {
		t.Fatal(err)
	}
	if report.TokenUsage.TotalPromptTokens != 30 || report.TokenUsage.TotalCompletionTokens != 10 {
		t.Fatalf("token usage = %#v, want prompt=30 completion=10", report.TokenUsage)
	}
	if report.TokenUsage.RequestCount != 2 {
		t.Fatalf("request count = %d, want 2", report.TokenUsage.RequestCount)
	}
}

func TestLoadSessionClassifiesIncrementalFindings(t *testing.T) {
	root := t.TempDir()
	repoDir := filepath.Join(root, "repo")
	if err := os.MkdirAll(repoDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repoDir, "mr-session.jsonl")
	data := `{"type":"session_start","sessionId":"mr-session","timestamp":"2026-08-18T00:00:00Z"}
{"type":"review_item_done","filePath":"a.go","fingerprint":"old-a","comments":[{"path":"a.go","content":"still broken","category":"bug"},{"path":"a.go","content":"fixed now","category":"security"}]}
{"type":"session_end","sessionId":"mr-session","timestamp":"2026-08-18T00:01:00Z"}
{"type":"session_start","sessionId":"mr-session","timestamp":"2026-08-18T00:02:00Z"}
{"type":"review_item_done","filePath":"a.go","fingerprint":"new-a","comments":[{"path":"a.go","content":"still broken","category":"bug"},{"path":"a.go","content":"new issue","category":"performance"}]}
{"type":"session_end","sessionId":"mr-session","timestamp":"2026-08-18T00:03:00Z"}
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}

	report, err := LoadSession(root, "repo", "mr-session")
	if err != nil {
		t.Fatal(err)
	}
	if report.Changes.New != 1 || report.Changes.Unfixed != 1 || report.Changes.Fixed != 1 {
		t.Fatalf("changes = %#v, want new=1 unfixed=1 fixed=1", report.Changes)
	}
	statuses := map[FindingStatus]int{}
	for _, comment := range report.Comments {
		statuses[comment.Status]++
	}
	if statuses[FindingNew] != 1 || statuses[FindingUnfixed] != 1 || statuses[FindingFixed] != 1 {
		t.Fatalf("statuses = %#v", statuses)
	}
}

func TestSessionTemplateParsesFindingStatusFields(t *testing.T) {
	if _, err := parseTemplate("session.html"); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSessionUsesAIReconciliationTimeline(t *testing.T) {
	root := t.TempDir()
	repoDir := filepath.Join(root, "repo")
	if err := os.MkdirAll(repoDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repoDir, "mr-session.jsonl")
	data := `{"type":"session_start","sessionId":"mr-session","timestamp":"2026-08-18T00:00:00Z"}
{"type":"review_item_done","filePath":"a.go","fingerprint":"current","comments":[{"path":"a.go","content":"new wording","category":"bug"}]}
{"type":"finding_reconciliation","timeline":{"commit_sha":"head-2","start_sha":"head-1","exact_range":"head-1..head-2","historical_findings":[{"id":"old-1","comment":{"path":"a.go","content":"old wording","category":"bug"}}],"current_findings":[{"id":"new-1","comment":{"path":"a.go","content":"new wording","category":"bug"}}],"decisions":[{"historical_id":"old-1","current_id":"new-1","status":"unfixed","confidence":"high","reason":"根因仍在"}]}}
{"type":"session_end","sessionId":"mr-session","timestamp":"2026-08-18T00:01:00Z"}
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := LoadSession(root, "repo", "mr-session")
	if err != nil {
		t.Fatal(err)
	}
	if report.Changes.Unfixed != 1 || len(report.Timeline) != 1 || report.Timeline[0].ExactRange != "head-1..head-2" {
		t.Fatalf("report changes/timeline = %#v/%#v", report.Changes, report.Timeline)
	}
}
