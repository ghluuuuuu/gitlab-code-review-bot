package codegraph

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestReviewToolsExposeReadOnlyGraphQueries(t *testing.T) {
	for _, name := range ReviewTools() {
		if name == buildTool || name == "apply_refactor_tool" || name == "refactor_tool" {
			t.Fatalf("mutating tool %q must not be exposed to OCR", name)
		}
	}
}

func TestProjectLocksSerializeOneProjectOnly(t *testing.T) {
	locks := &ProjectLocks{}
	release, err := locks.Acquire(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	blockedCtx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := locks.Acquire(blockedCtx, 1); err == nil {
		t.Fatal("same-project acquisition succeeded while lock was held")
	}
	otherRelease, err := locks.Acquire(context.Background(), 2)
	if err != nil {
		t.Fatalf("different project was blocked: %v", err)
	}
	otherRelease()
}

func TestNormalizeAffectedFilesReturnsRepositoryRelativePaths(t *testing.T) {
	root := t.TempDir()
	abs := filepath.Join(root, "pkg", "caller.go")
	raw, err := json.Marshal(map[string]any{"impacted_files": []string{abs, "other.go", abs, "../outside.go"}})
	if err != nil {
		t.Fatal(err)
	}
	got := normalizeAffectedFiles(root, string(raw))
	if len(got) != 2 || got[0] != "pkg/caller.go" || got[1] != "other.go" {
		t.Fatalf("affected files = %#v", got)
	}
}
