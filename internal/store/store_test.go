package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestEnqueueIsIdempotentAndClaimable(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	input := EnqueueInput{ProjectID: 1, MRIID: 2, SourceProjectID: 1, TargetProjectID: 1, SourceBranch: "feature", TargetBranch: "main", HeadSHA: "abc", TargetSHA: "target-1"}
	if _, err := st.Enqueue(ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Enqueue(ctx, input); err != nil {
		t.Fatal(err)
	}
	jobs, err := st.ListQueue(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected one queued job, got %d", len(jobs))
	}
	job, err := st.Claim(ctx, "worker-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if job == nil || job.State != StateRunning || job.Attempt != 1 {
		t.Fatalf("unexpected claimed job: %#v", job)
	}
}

func TestSetProgressPersistsAndClampsCompletedFiles(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	input := EnqueueInput{ProjectID: 1, MRIID: 2, SourceProjectID: 1, TargetProjectID: 1, SourceBranch: "feature", TargetBranch: "main", HeadSHA: "abc", TargetSHA: "target-1"}
	if _, err := st.Enqueue(ctx, input); err != nil {
		t.Fatal(err)
	}
	job, err := st.Claim(ctx, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetProgress(ctx, job.ID, 8, 5); err != nil {
		t.Fatal(err)
	}
	if err := st.SetUsage(ctx, job.ID, 123, 45, 168); err != nil {
		t.Fatal(err)
	}
	if err := st.SetUsage(ctx, job.ID, 12, 4, 16); err != nil {
		t.Fatal(err)
	}
	job, err = st.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.ProgressCompleted != 5 || job.ProgressTotal != 5 || job.InputTokens != 123 || job.OutputTokens != 45 || job.TotalTokens != 168 {
		t.Fatalf("unexpected persisted progress and usage: %#v", job)
	}
}

func TestEnqueueNewHeadMarksOldJobStale(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	oldInput := EnqueueInput{ProjectID: 1, MRIID: 2, SourceProjectID: 1, TargetProjectID: 1, SourceBranch: "feature", TargetBranch: "main", HeadSHA: "old", TargetSHA: "target-1"}
	if _, err := st.Enqueue(ctx, oldInput); err != nil {
		t.Fatal(err)
	}
	oldJob, err := st.Claim(ctx, "worker-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetGitMetadata(ctx, oldJob.ID, oldInput.TargetSHA, "base-1", "rule-1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetLLMMetadata(ctx, oldJob.ID, "ocr-bot", "model", "session-1"); err != nil {
		t.Fatal(err)
	}
	newInput := oldInput
	newInput.HeadSHA = "new"
	if _, err := st.Enqueue(ctx, newInput); err != nil {
		t.Fatal(err)
	}
	oldJob, err = st.GetJob(ctx, oldJob.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Finish(ctx, oldJob.ID, StateFailedInfra, "late cancellation", "", Usage{}); err != nil {
		t.Fatal(err)
	}
	oldJob, err = st.GetJob(ctx, oldJob.ID)
	if err != nil {
		t.Fatal(err)
	}
	if oldJob.State != StateStale || oldJob.FailureReason != "superseded by newer revision new/target-1" {
		t.Fatalf("late completion overwrote stale job: %#v", oldJob)
	}
	active, err := st.IsActive(ctx, oldJob.ID, oldJob.HeadSHA)
	if err != nil {
		t.Fatal(err)
	}
	if active {
		t.Fatal("superseded job must not remain active")
	}
	queue, err := st.ListQueue(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue) != 1 || queue[0].HeadSHA != "new" || queue[0].SessionID != "session-1" || queue[0].RuleSHA256 != "rule-1" {
		t.Fatalf("expected the new head to continue the previous session: %#v", queue)
	}
}

func TestEnqueueNewTargetRevisionContinuesSession(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	input := EnqueueInput{ProjectID: 1, MRIID: 2, SourceProjectID: 1, TargetProjectID: 1, SourceBranch: "feature", TargetBranch: "main", HeadSHA: "head-1", TargetSHA: "target-1"}
	if _, err := st.Enqueue(ctx, input); err != nil {
		t.Fatal(err)
	}
	oldJob, err := st.Claim(ctx, "worker-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetGitMetadata(ctx, oldJob.ID, input.TargetSHA, "base-1", "rule-1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetLLMMetadata(ctx, oldJob.ID, "ocr-bot", "model", "session-1"); err != nil {
		t.Fatal(err)
	}
	input.TargetSHA = "target-2"
	if _, err := st.Enqueue(ctx, input); err != nil {
		t.Fatal(err)
	}
	oldJob, err = st.GetJob(ctx, oldJob.ID)
	if err != nil {
		t.Fatal(err)
	}
	if oldJob.State != StateStale {
		t.Fatalf("old target revision remained active: %#v", oldJob)
	}
	queue, err := st.ListQueue(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue) != 1 || queue[0].HeadSHA != "head-1" || queue[0].TargetSHA != "target-2" || queue[0].SessionID != "session-1" || queue[0].RuleSHA256 != "rule-1" {
		t.Fatalf("expected the new target revision to continue the previous session: %#v", queue)
	}
}
func TestListReusableSessionsAcrossMergeRequests(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	addSession := func(projectID, mrIID, targetProjectID int64, headSHA, targetSHA, ruleSHA, sessionID string) int64 {
		t.Helper()
		input := EnqueueInput{
			ProjectID: projectID, MRIID: mrIID, SourceProjectID: projectID, TargetProjectID: targetProjectID,
			SourceBranch: "feature", TargetBranch: "main", HeadSHA: headSHA, TargetSHA: targetSHA,
		}
		if _, err := st.Enqueue(ctx, input); err != nil {
			t.Fatal(err)
		}
		job, err := st.Claim(ctx, "worker", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SetGitMetadata(ctx, job.ID, targetSHA, "base", ruleSHA); err != nil {
			t.Fatal(err)
		}
		if err := st.SetLLMMetadata(ctx, job.ID, "ocr-bot", "model", sessionID); err != nil {
			t.Fatal(err)
		}
		if err := st.Finish(ctx, job.ID, StateCompletedPass, "", "", Usage{}); err != nil {
			t.Fatal(err)
		}
		return job.ID
	}
	compatibleID := addSession(1, 1, 1, "head-1", "target-1", "rule-1", "session-compatible")
	addSession(1, 2, 1, "head-2", "target-1", "rule-2", "session-other-rule")
	addSession(2, 1, 2, "head-3", "target-2", "rule-1", "session-other-project")

	sessions, err := st.ListReusableSessions(ctx, 1, "rule-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0] != "session-compatible" {
		t.Fatalf("unexpected reusable sessions: %#v", sessions)
	}
	sessions, err = st.ListReusableSessions(ctx, 1, "rule-1", compatibleID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("excluded job remained reusable: %#v", sessions)
	}
}

func TestRecoverInterruptedRequeuesRunningAndPublishing(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	for i, state := range []string{StateRunning, StatePublishing} {
		input := EnqueueInput{ProjectID: 1, MRIID: int64(i + 1), SourceProjectID: 1, TargetProjectID: 1, SourceBranch: "feature", TargetBranch: "main", HeadSHA: state, TargetSHA: "target-1"}
		if _, err := st.Enqueue(ctx, input); err != nil {
			t.Fatal(err)
		}
		job, err := st.Claim(ctx, "worker", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SetLLMMetadata(ctx, job.ID, "ocr-bot", "test-model", "session-"+state); err != nil {
			t.Fatal(err)
		}
		if state == StatePublishing {
			if err := st.SetStage(ctx, job.ID, "publishing"); err != nil {
				t.Fatal(err)
			}
			if _, err := st.db.ExecContext(ctx, `UPDATE review_job SET state=? WHERE id=?`, StatePublishing, job.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	recovered, err := st.RecoverInterrupted(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 2 {
		t.Fatalf("expected 2 recovered jobs, got %d", recovered)
	}
	queue, err := st.ListQueue(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue) != 2 {
		t.Fatalf("expected 2 requeued jobs, got %d", len(queue))
	}
	for _, job := range queue {
		if job.State != StateRetryWait || job.Stage != "queued" || job.LeaseOwner != "" || job.LeaseUntil != nil {
			t.Fatalf("unexpected recovered job: %#v", job)
		}
		if job.SessionID != "session-"+job.HeadSHA {
			t.Fatalf("recovery lost OCR session ID: %#v", job)
		}
	}
}
func TestRetryPreservesSessionAndUsage(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	_, err = st.Enqueue(ctx, EnqueueInput{ProjectID: 1, MRIID: 1, SourceProjectID: 1, TargetProjectID: 1, SourceBranch: "feature", TargetBranch: "main", HeadSHA: "head", TargetSHA: "target-1"})
	if err != nil {
		t.Fatal(err)
	}
	job, err := st.Claim(ctx, "worker", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetLLMMetadata(ctx, job.ID, "ocr-bot", "model", "resume-session"); err != nil {
		t.Fatal(err)
	}
	usage := Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15, Comments: 2, ToolCalls: 3}
	if err := st.Retry(ctx, job.ID, "temporary failure", "artifacts", usage); err != nil {
		t.Fatal(err)
	}
	resumed, err := st.Claim(ctx, "worker-2", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.SessionID != "resume-session" || resumed.Attempt != 2 {
		t.Fatalf("retry lost resume metadata: %#v", resumed)
	}
	if resumed.TotalTokens != 15 || resumed.Comments != 2 || resumed.ToolCalls != 3 {
		t.Fatalf("retry lost partial usage: %#v", resumed)
	}
}

func TestRetryAfterDefersClaimUntilScheduledTime(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err := st.Enqueue(ctx, EnqueueInput{ProjectID: 1, MRIID: 1, SourceProjectID: 1, TargetProjectID: 1, SourceBranch: "feature", TargetBranch: "main", HeadSHA: "head", TargetSHA: "target-1"}); err != nil {
		t.Fatal(err)
	}
	job, err := st.Claim(ctx, "worker", time.Hour)
	if err != nil || job == nil {
		t.Fatalf("claim: job=%#v err=%v", job, err)
	}
	notBefore := time.Now().UTC().Add(time.Hour)
	if err := st.RetryAfter(ctx, job.ID, "temporary failure", "artifacts", Usage{}, notBefore); err != nil {
		t.Fatal(err)
	}
	claimed, err := st.Claim(ctx, "worker-2", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if claimed != nil {
		t.Fatalf("delayed retry was claimed early: %#v", claimed)
	}
	deferred, err := st.GetJob(ctx, job.ID)
	if err != nil || deferred.LeaseUntil == nil || deferred.State != StateRetryWait {
		t.Fatalf("delayed retry state = %#v err=%v", deferred, err)
	}
}

func TestOpenMigratesLegacyReviewIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE review_job (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id INTEGER NOT NULL,
 mr_iid INTEGER NOT NULL,
 title TEXT NOT NULL DEFAULT '',
 web_url TEXT NOT NULL DEFAULT '',
 source_project_id INTEGER NOT NULL,
 source_branch TEXT NOT NULL,
 target_project_id INTEGER NOT NULL,
 target_branch TEXT NOT NULL,
 head_sha TEXT NOT NULL,
 target_sha TEXT NOT NULL DEFAULT '',
 base_sha TEXT NOT NULL DEFAULT '',
 rule_sha256 TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL,
 stage TEXT NOT NULL DEFAULT 'queued',
 priority INTEGER NOT NULL DEFAULT 0,
 attempt INTEGER NOT NULL DEFAULT 0,
 failure_reason TEXT NOT NULL DEFAULT '',
 queued_at TEXT NOT NULL,
 started_at TEXT,
 finished_at TEXT,
 lease_owner TEXT NOT NULL DEFAULT '',
 artifact_dir TEXT NOT NULL DEFAULT '',
 lease_until TEXT,
 repo_dir TEXT NOT NULL DEFAULT '',
 llm_provider TEXT NOT NULL DEFAULT '',
 llm_model TEXT NOT NULL DEFAULT '',
 session_id TEXT NOT NULL DEFAULT '',
 tool_calls INTEGER NOT NULL DEFAULT 0,
 input_tokens INTEGER NOT NULL DEFAULT 0,
 output_tokens INTEGER NOT NULL DEFAULT 0,
 total_tokens INTEGER NOT NULL DEFAULT 0,
 comments INTEGER NOT NULL DEFAULT 0,
 UNIQUE(project_id, mr_iid, head_sha)
);
INSERT INTO review_job(project_id,mr_iid,source_project_id,source_branch,target_project_id,target_branch,head_sha,target_sha,state,queued_at,session_id,rule_sha256)
VALUES(1,2,1,'feature',1,'main','head-1','target-1','completed_fail','2026-08-14T00:00:00Z','session-1','rule-1');
`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	input := EnqueueInput{ProjectID: 1, MRIID: 2, SourceProjectID: 1, TargetProjectID: 1, SourceBranch: "feature", TargetBranch: "main", HeadSHA: "head-1", TargetSHA: "target-2"}
	if _, err := st.Enqueue(ctx, input); err != nil {
		t.Fatal(err)
	}
	reviews, err := st.ListAllReviews(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(reviews) != 2 || reviews[0].TargetSHA != "target-2" || reviews[0].SessionID != "session-1" {
		t.Fatalf("legacy review identity was not migrated: %#v", reviews)
	}
}
