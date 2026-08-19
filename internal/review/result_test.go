package review

import "testing"

func TestParseResult(t *testing.T) {
	result, err := ParseResult("testdata/result.json")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "complete" {
		t.Fatalf("expected status complete, got %q", result.Status)
	}
	if result.LLM == nil || result.LLM.Model != "glm-5.2" {
		t.Fatalf("expected llm model glm-5.2, got %#v", result.LLM)
	}
	if result.ToolCalls == nil || result.ToolCalls.Total != 16 {
		t.Fatalf("expected tool calls total 16, got %#v", result.ToolCalls)
	}
	if len(result.Comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(result.Comments))
	}
	if result.Comments[0].Severity != "high" {
		t.Fatalf("expected severity high, got %q", result.Comments[0].Severity)
	}
	if result.Manifest == nil || result.Manifest.TerminalState != "complete" {
		t.Fatalf("unexpected manifest: %#v", result.Manifest)
	}
	if !IsBlocking(result, []string{"critical", "high"}) {
		t.Fatal("expected high-severity finding to block")
	}
	if CoverageIncomplete(result) {
		t.Fatal("expected coverage to be complete")
	}
}

func TestCoverageIncomplete(t *testing.T) {
	result := Result{Manifest: &Manifest{TerminalState: "partial", Coverage: ManifestCoverage{Selected: []ManifestItem{{Path: "a.go"}}, Completed: []ManifestItem{{Path: "a.go"}}}}}
	if CoverageIncomplete(result) {
		t.Fatal("expected complete coverage")
	}
	result.Manifest.Coverage.Failed = []ManifestItem{{Path: "b.go"}}
	if !CoverageIncomplete(result) {
		t.Fatal("expected failed coverage to be incomplete")
	}
	result.Manifest.Coverage = ManifestCoverage{Selected: []ManifestItem{{Path: "a.go"}, {Path: "b.go"}}, Completed: []ManifestItem{{Path: "a.go"}}}
	if !CoverageIncomplete(result) {
		t.Fatal("expected partial completion to be incomplete")
	}
}
