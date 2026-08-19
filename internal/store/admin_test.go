package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestAdminReviewQueriesAndActions(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "admin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Enqueue(ctx, EnqueueInput{ProjectID: 1, MRIID: 2, SourceProjectID: 1, TargetProjectID: 1, SourceBranch: "feature", TargetBranch: "main", HeadSHA: "head-1", TargetSHA: "target-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Enqueue(ctx, EnqueueInput{ProjectID: 1, MRIID: 3, SourceProjectID: 1, TargetProjectID: 1, SourceBranch: "feature-2", TargetBranch: "main", HeadSHA: "head-2", TargetSHA: "target-1"}); err != nil {
		t.Fatal(err)
	}
	page, err := st.ListReviews(ctx, ReviewListQuery{Scope: "active", Page: 1, PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Items) != 1 || !page.HasNext {
		t.Fatalf("unexpected active page: %#v", page)
	}
	job := page.Items[0]
	if err := st.SetStage(ctx, job.ID, "ocr_review"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProgress(ctx, job.ID, 2, 4); err != nil {
		t.Fatal(err)
	}
	events, err := st.ListEvents(ctx, job.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 2 {
		t.Fatalf("expected stage and progress events, got %#v", events)
	}
	if err := st.SetPriority(ctx, job.ID, 20, "urgent branch"); err != nil {
		t.Fatal(err)
	}
	if err := st.CancelReview(ctx, job.ID, "manual stop"); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordAudit(ctx, "operator", "review.cancel", &job.ID, "manual stop"); err != nil {
		t.Fatal(err)
	}
	updated, err := st.GetJob(ctx, job.ID)
	if err != nil || updated == nil {
		t.Fatalf("read canceled job: %v %#v", err, updated)
	}
	if updated.State != StateCanceled {
		t.Fatalf("state = %q, want %q", updated.State, StateCanceled)
	}
	audit, err := st.ListAuditEvents(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(audit) == 0 {
		t.Fatal("expected audit events")
	}
}

func TestFindingPersistencePublishesRealtimeEvent(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "finding-events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Enqueue(ctx, EnqueueInput{ProjectID: 1, MRIID: 2, SourceProjectID: 1, TargetProjectID: 1, SourceBranch: "feature", TargetBranch: "main", HeadSHA: "head", TargetSHA: "target"}); err != nil {
		t.Fatal(err)
	}
	jobs, err := st.ListQueue(ctx, 1)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("queue = %#v, err = %v", jobs, err)
	}
	events, unsubscribe := st.SubscribeEvents(jobs[0].ID)
	defer unsubscribe()
	if err := st.RecordFinding(ctx, ReviewFinding{ReviewJobID: jobs[0].ID, Path: "a.go", StartLine: 5, EndLine: 5, Category: "security", Severity: "high", Content: "unsafe"}); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		if event.EventType != "finding_updated" || event.ReviewJobID != jobs[0].ID {
			t.Fatalf("unexpected event: %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for finding SSE event")
	}
	findings, err := st.ListFindings(ctx, jobs[0].ID)
	if err != nil || len(findings) != 1 || findings[0].Severity != "high" {
		t.Fatalf("findings = %#v, err = %v", findings, err)
	}
}
