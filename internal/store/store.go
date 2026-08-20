package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	StateQueued              = "queued"
	StateRunning             = "running"
	StateRetryWait           = "retry_wait"
	StatePublishing          = "publishing"
	StateCompletedPass       = "completed_pass"
	StateCompletedFail       = "completed_fail"
	StateRejectedRuleMissing = "rejected_rule_missing"
	StateRejectedRuleInvalid = "rejected_rule_invalid"
	StateStale               = "stale"
	StateFailedInfra         = "failed_infra"
	StateCanceled            = "canceled"
)

type eventSubscriber struct {
	jobID int64
	ch    chan ReviewEvent
}

type Store struct {
	db          *sql.DB
	eventMu     sync.RWMutex
	subscribers map[uint64]eventSubscriber
	nextSubID   uint64
}

type EnqueueInput struct {
	ProjectID       int64
	MRIID           int64
	Title           string
	WebURL          string
	SourceProjectID int64
	SourceBranch    string
	TargetProjectID int64
	TargetBranch    string
	HeadSHA         string
	TargetSHA       string
}

type ReviewJob struct {
	ID                int64      `json:"id"`
	ProjectID         int64      `json:"project_id"`
	MRIID             int64      `json:"mr_iid"`
	Title             string     `json:"title"`
	WebURL            string     `json:"web_url"`
	SourceProjectID   int64      `json:"source_project_id"`
	SourceBranch      string     `json:"source_branch"`
	TargetProjectID   int64      `json:"target_project_id"`
	TargetBranch      string     `json:"target_branch"`
	HeadSHA           string     `json:"head_sha"`
	Files             []string   `json:"files,omitempty"`
	TargetSHA         string     `json:"target_sha"`
	BaseSHA           string     `json:"base_sha"`
	RuleSHA256        string     `json:"rule_sha256"`
	State             string     `json:"state"`
	Stage             string     `json:"stage"`
	ProgressCompleted int        `json:"progress_completed"`
	ProgressTotal     int        `json:"progress_total"`
	Priority          int        `json:"priority"`
	Attempt           int        `json:"attempt"`
	FailureReason     string     `json:"failure_reason"`
	QueuedAt          time.Time  `json:"queued_at"`
	StartedAt         *time.Time `json:"started_at,omitempty"`
	FinishedAt        *time.Time `json:"finished_at,omitempty"`
	LeaseOwner        string     `json:"lease_owner,omitempty"`
	LeaseUntil        *time.Time `json:"lease_until,omitempty"`
	ArtifactDir       string     `json:"artifact_dir,omitempty"`
	RepoDir           string     `json:"repo_dir,omitempty"`
	InputTokens       int64      `json:"input_tokens"`
	OutputTokens      int64      `json:"output_tokens"`
	TotalTokens       int64      `json:"total_tokens"`
	Comments          int64      `json:"comments"`
	LLMProvider       string     `json:"llm_provider,omitempty"`
	LLMModel          string     `json:"llm_model,omitempty"`
	SessionID         string     `json:"session_id,omitempty"`
	ReportURL         string     `json:"report_url,omitempty"`
	ToolCalls         int64      `json:"tool_calls"`
}

type Dashboard struct {
	Queued      int64 `json:"queued"`
	Running     int64 `json:"running"`
	RetryWait   int64 `json:"retry_wait"`
	Publishing  int64 `json:"publishing"`
	Failed      int64 `json:"failed"`
	FailedInfra int64 `json:"failed_infra"`
	Passed      int64 `json:"passed"`
	Stale       int64 `json:"stale"`
	Canceled    int64 `json:"canceled"`
	TodayTokens int64 `json:"today_tokens"`
	MonthTokens int64 `json:"month_tokens"`
}

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)&_pragma=wal_autocheckpoint(1000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, subscribers: make(map[uint64]eventSubscriber)}, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS review_job (
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
 progress_completed INTEGER NOT NULL DEFAULT 0,
 progress_total INTEGER NOT NULL DEFAULT 0,
	UNIQUE(project_id, mr_iid, head_sha, target_sha)
);
CREATE INDEX IF NOT EXISTS idx_review_job_queue ON review_job(state, priority DESC, queued_at);
CREATE INDEX IF NOT EXISTS idx_review_job_usage ON review_job(finished_at, project_id);
CREATE TABLE IF NOT EXISTS scheduler_cursor (
 id INTEGER PRIMARY KEY CHECK(id = 1),
 last_poll_started_at TEXT NOT NULL
);
INSERT INTO scheduler_cursor(id, last_poll_started_at)
VALUES (1, '1970-01-01T00:00:00Z') ON CONFLICT(id) DO NOTHING;
CREATE TABLE IF NOT EXISTS audit_event (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 actor TEXT NOT NULL,
 action TEXT NOT NULL,
 review_job_id INTEGER,
 detail TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 FOREIGN KEY(review_job_id) REFERENCES review_job(id)
);
CREATE TABLE IF NOT EXISTS review_event (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 review_job_id INTEGER NOT NULL,
 event_type TEXT NOT NULL,
 stage TEXT NOT NULL DEFAULT '',
 safe_message TEXT NOT NULL DEFAULT '',
 completed INTEGER NOT NULL DEFAULT 0,
 total INTEGER NOT NULL DEFAULT 0,
 input_tokens INTEGER NOT NULL DEFAULT 0,
 output_tokens INTEGER NOT NULL DEFAULT 0,
 total_tokens INTEGER NOT NULL DEFAULT 0,
 created_at TEXT NOT NULL,
 FOREIGN KEY(review_job_id) REFERENCES review_job(id)
);
CREATE INDEX IF NOT EXISTS idx_review_event_job_time ON review_event(review_job_id, created_at, id);
CREATE TABLE IF NOT EXISTS review_finding (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 review_job_id INTEGER NOT NULL,
 path TEXT NOT NULL DEFAULT '',
 content TEXT NOT NULL DEFAULT '',
 suggestion_code TEXT NOT NULL DEFAULT '',
 existing_code TEXT NOT NULL DEFAULT '',
 start_line INTEGER NOT NULL DEFAULT 0,
 end_line INTEGER NOT NULL DEFAULT 0,
 category TEXT NOT NULL DEFAULT '',
 severity TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL DEFAULT 'current',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 FOREIGN KEY(review_job_id) REFERENCES review_job(id),
 UNIQUE(review_job_id,path,start_line,end_line,category,severity,content)
);
CREATE TABLE IF NOT EXISTS app_user (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 username TEXT NOT NULL COLLATE NOCASE UNIQUE,
 email TEXT NOT NULL COLLATE NOCASE UNIQUE,
 password_hash TEXT NOT NULL DEFAULT '',
 role TEXT NOT NULL CHECK(role IN ('superadmin','user')),
 enabled INTEGER NOT NULL DEFAULT 1,
 auth_source TEXT NOT NULL DEFAULT 'local' CHECK(auth_source IN ('local','oidc')),
 oidc_issuer TEXT NOT NULL DEFAULT '',
 oidc_subject TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 last_login_at TEXT,
 mcp_token_hash TEXT NOT NULL DEFAULT '',
 mcp_token_created_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_app_user_email ON app_user(email);
CREATE UNIQUE INDEX IF NOT EXISTS idx_app_user_oidc_identity ON app_user(oidc_issuer,oidc_subject) WHERE oidc_issuer<>'' AND oidc_subject<>'';
CREATE TABLE IF NOT EXISTS user_session (
 token_hash TEXT PRIMARY KEY,
 user_id INTEGER NOT NULL,
 expires_at TEXT NOT NULL,
 created_at TEXT NOT NULL,
 FOREIGN KEY(user_id) REFERENCES app_user(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_user_session_expiry ON user_session(expires_at);
CREATE TABLE IF NOT EXISTS oidc_login_state (
 state_hash TEXT PRIMARY KEY,
 nonce TEXT NOT NULL,
 code_verifier TEXT NOT NULL,
 return_to TEXT NOT NULL DEFAULT '/',
 expires_at TEXT NOT NULL,
 created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_oidc_state_expiry ON oidc_login_state(expires_at);
CREATE INDEX IF NOT EXISTS idx_review_finding_job ON review_finding(review_job_id, path, severity, category);
`)
	if err != nil {
		return err
	}
	if err := migrateReviewJobIdentity(db); err != nil {
		return err
	}
	if err := migrateUserMCPToken(db); err != nil {
		return err
	}
	return migrateReviewProgress(db)
}

func migrateReviewJobIdentity(db *sql.DB) error {
	var schema string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='review_job'`).Scan(&schema); err != nil {
		return err
	}
	normalized := strings.NewReplacer(" ", "", "\n", "", "\r", "", "\t", "").Replace(strings.ToLower(schema))
	if strings.Contains(normalized, "unique(project_id,mr_iid,head_sha,target_sha)") {
		return nil
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer func() { _, _ = db.Exec(`PRAGMA foreign_keys=ON`) }()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`
DROP TABLE IF EXISTS review_job_migrated;
CREATE TABLE review_job_migrated (
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
 progress_completed INTEGER NOT NULL DEFAULT 0,
 progress_total INTEGER NOT NULL DEFAULT 0,
 UNIQUE(project_id, mr_iid, head_sha, target_sha)
);
INSERT INTO review_job_migrated(
 id,project_id,mr_iid,title,web_url,source_project_id,source_branch,target_project_id,target_branch,head_sha,target_sha,base_sha,rule_sha256,state,stage,priority,attempt,failure_reason,queued_at,started_at,finished_at,lease_owner,artifact_dir,lease_until,repo_dir,llm_provider,llm_model,session_id,tool_calls,input_tokens,output_tokens,total_tokens,comments
)
SELECT
 id,project_id,mr_iid,title,web_url,source_project_id,source_branch,target_project_id,target_branch,head_sha,target_sha,base_sha,rule_sha256,state,stage,priority,attempt,failure_reason,queued_at,started_at,finished_at,lease_owner,artifact_dir,lease_until,repo_dir,llm_provider,llm_model,session_id,tool_calls,input_tokens,output_tokens,total_tokens,comments
FROM review_job;
DROP TABLE review_job;
ALTER TABLE review_job_migrated RENAME TO review_job;
CREATE INDEX idx_review_job_queue ON review_job(state, priority DESC, queued_at);
CREATE INDEX idx_review_job_usage ON review_job(finished_at, project_id);
`)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, err = db.Exec(`PRAGMA foreign_keys=ON`)
	return err
}

func migrateUserMCPToken(db *sql.DB) error {
	for _, column := range []struct{ name, definition string }{
		{name: "mcp_token_hash", definition: "mcp_token_hash TEXT NOT NULL DEFAULT ''"},
		{name: "mcp_token_created_at", definition: "mcp_token_created_at TEXT"},
	} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('app_user') WHERE name=?`, column.name).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			if _, err := db.Exec(`ALTER TABLE app_user ADD COLUMN ` + column.definition); err != nil {
				return err
			}
		}
	}
	return nil
}
func migrateReviewProgress(db *sql.DB) error {
	for _, column := range []struct {
		name       string
		definition string
	}{
		{name: "progress_completed", definition: "progress_completed INTEGER NOT NULL DEFAULT 0"},
		{name: "progress_total", definition: "progress_total INTEGER NOT NULL DEFAULT 0"},
	} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('review_job') WHERE name=?`, column.name).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			if _, err := db.Exec(`ALTER TABLE review_job ADD COLUMN ` + column.definition); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) Enqueue(ctx context.Context, in EnqueueInput) (bool, error) {
	if in.ProjectID == 0 || in.MRIID == 0 || in.HeadSHA == "" || in.TargetSHA == "" || in.TargetBranch == "" {
		return false, errors.New("invalid review job identity")
	}
	if in.SourceProjectID == 0 {
		in.SourceProjectID = in.ProjectID
	}
	if in.TargetProjectID == 0 {
		in.TargetProjectID = in.ProjectID
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var resumeSessionID, resumeRuleSHA string
	err = tx.QueryRowContext(ctx, `
SELECT session_id, rule_sha256 FROM review_job
WHERE project_id=? AND mr_iid=? AND session_id<>''
ORDER BY id DESC LIMIT 1`, in.ProjectID, in.MRIID).Scan(&resumeSessionID, &resumeRuleSHA)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE review_job SET state=?, stage='finished', failure_reason=?, finished_at=?, lease_owner='', lease_until=NULL
WHERE project_id=? AND mr_iid=? AND (head_sha<>? OR target_sha<>?) AND state IN (?,?,?,?)`,
		StateStale, "superseded by newer revision "+in.HeadSHA+"/"+in.TargetSHA, now(), in.ProjectID, in.MRIID, in.HeadSHA, in.TargetSHA,
		StateQueued, StateRunning, StateRetryWait, StatePublishing); err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `
INSERT INTO review_job(project_id,mr_iid,title,web_url,source_project_id,source_branch,target_project_id,target_branch,head_sha,target_sha,rule_sha256,state,queued_at,session_id)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(project_id,mr_iid,head_sha,target_sha) DO UPDATE SET
 title=excluded.title, web_url=excluded.web_url
WHERE review_job.state=?`, in.ProjectID, in.MRIID, in.Title, in.WebURL, in.SourceProjectID, in.SourceBranch, in.TargetProjectID, in.TargetBranch, in.HeadSHA, in.TargetSHA, resumeRuleSHA, StateQueued, now(), resumeSessionID, StateQueued)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	count, _ := result.RowsAffected()
	return count > 0, nil
}

func (s *Store) IsActive(ctx context.Context, id int64, headSHA string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM review_job
WHERE id=? AND head_sha=? AND state IN (?,?,?,?)`, id, headSHA, StateQueued, StateRunning, StateRetryWait, StatePublishing).Scan(&count)
	return count == 1, err
}

func (s *Store) Claim(ctx context.Context, owner string, lease time.Duration) (*ReviewJob, error) {
	if owner == "" {
		return nil, errors.New("lease owner is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRowContext(ctx, `
SELECT id FROM review_job
WHERE state IN (?, ?) AND (lease_until IS NULL OR lease_until < ?)
ORDER BY priority DESC, queued_at ASC LIMIT 1`, StateQueued, StateRetryWait, now()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	started := now()
	leaseUntil := time.Now().UTC().Add(lease).Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `
UPDATE review_job SET state=?, stage='rule_preflight', lease_owner=?, lease_until=?, started_at=?, attempt=attempt+1, failure_reason=''
WHERE id=? AND state IN (?, ?)`, StateRunning, owner, leaseUntil, started, id, StateQueued, StateRetryWait)
	if err != nil {
		return nil, err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return nil, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	job, err := s.GetJob(ctx, id)
	if err == nil && job != nil {
		_ = s.RecordEvent(ctx, ReviewEvent{ReviewJobID: id, EventType: "stage_started", Stage: "rule_preflight", SafeMessage: "worker claimed review"})
	}
	return job, err
}

func (s *Store) GetJob(ctx context.Context, id int64) (*ReviewJob, error) {
	row := s.db.QueryRowContext(ctx, selectJob+` WHERE id=?`, id)
	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return job, err
}

func (s *Store) ListQueue(ctx context.Context, limit int) ([]ReviewJob, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, selectJob+` WHERE state IN (?,?,?,?) ORDER BY priority DESC, queued_at LIMIT ?`, StateQueued, StateRunning, StateRetryWait, StatePublishing, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ReviewJob
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *job)
	}
	return result, rows.Err()
}

func (s *Store) ListHistory(ctx context.Context, limit int) ([]ReviewJob, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, selectJob+` WHERE state IN (?,?,?,?,?,?,?,?,?) ORDER BY finished_at DESC LIMIT ?`,
		StateCompletedPass, StateCompletedFail, StateRejectedRuleMissing, StateRejectedRuleInvalid,
		StateStale, StateFailedInfra, StateCanceled, StateQueued, StateRunning, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ReviewJob
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *job)
	}
	return result, rows.Err()
}

// ListAllReviews returns every recorded review, including active and terminal jobs.
func (s *Store) ListAllReviews(ctx context.Context) ([]ReviewJob, error) {
	rows, err := s.db.QueryContext(ctx, selectJob+` ORDER BY queued_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ReviewJob
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *job)
	}
	return result, rows.Err()
}

func (s *Store) GetReview(ctx context.Context, projectID, mrIID int64, headSHA string) (*ReviewJob, error) {
	row := s.db.QueryRowContext(ctx, selectJob+` WHERE project_id=? AND mr_iid=? AND head_sha=? ORDER BY id DESC LIMIT 1`, projectID, mrIID, headSHA)
	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return job, err
}

// ListReusableSessions returns persisted OCR sessions from the same target
// project whose review rules match the current run. Newest histories win when
// duplicate file fingerprints are merged by the review component.
func (s *Store) ListReusableSessions(ctx context.Context, targetProjectID int64, ruleSHA string, excludeJobID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT session_id FROM review_job
WHERE target_project_id=? AND rule_sha256=? AND id<>? AND session_id<>''
ORDER BY COALESCE(finished_at, queued_at) DESC, id DESC`, targetProjectID, ruleSHA, excludeJobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := make(map[string]struct{})
	sessions := make([]string, 0)
	for rows.Next() {
		var sessionID string
		if err := rows.Scan(&sessionID); err != nil {
			return nil, err
		}
		if _, exists := seen[sessionID]; exists {
			continue
		}
		seen[sessionID] = struct{}{}
		sessions = append(sessions, sessionID)
	}
	return sessions, rows.Err()
}

func (s *Store) SetProgress(ctx context.Context, id int64, completed, total int) error {
	if completed < 0 {
		completed = 0
	}
	if total < 0 {
		total = 0
	}
	if total > 0 && completed > total {
		completed = total
	}
	_, err := s.db.ExecContext(ctx, `UPDATE review_job SET progress_completed=?, progress_total=? WHERE id=?`, completed, total, id)
	if err == nil {
		_ = s.RecordEvent(ctx, ReviewEvent{ReviewJobID: id, EventType: "progress_updated", Completed: completed, Total: total})
	}
	return err
}

func (s *Store) SetUsage(ctx context.Context, id int64, inputTokens, outputTokens, totalTokens int64) error {
	if inputTokens < 0 {
		inputTokens = 0
	}
	if outputTokens < 0 {
		outputTokens = 0
	}
	if totalTokens < 0 {
		totalTokens = 0
	}
	_, err := s.db.ExecContext(ctx, `UPDATE review_job SET input_tokens=?, output_tokens=?, total_tokens=? WHERE id=? AND total_tokens<=?`, inputTokens, outputTokens, totalTokens, id, totalTokens)
	if err == nil {
		_ = s.RecordEvent(ctx, ReviewEvent{ReviewJobID: id, EventType: "usage_updated", InputTokens: inputTokens, OutputTokens: outputTokens, TotalTokens: totalTokens})
	}
	return err
}

func (s *Store) SetStage(ctx context.Context, id int64, stage string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE review_job SET stage=? WHERE id=?`, stage, id)
	if err == nil {
		_ = s.RecordEvent(ctx, ReviewEvent{ReviewJobID: id, EventType: "stage_started", Stage: stage})
	}
	return err
}

func (s *Store) SetGitMetadata(ctx context.Context, id int64, targetSHA, baseSHA, ruleSHA string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE review_job SET base_sha=?, rule_sha256=? WHERE id=? AND target_sha=?`, baseSHA, ruleSHA, id, targetSHA)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("review target revision changed")
	}
	return nil
}

func (s *Store) SetLLMMetadata(ctx context.Context, id int64, provider, model, sessionID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE review_job SET llm_provider=?, llm_model=?, session_id=? WHERE id=?`, provider, model, sessionID, id)
	return err
}

func (s *Store) Finish(ctx context.Context, id int64, state, reason, artifactDir string, usage Usage) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE review_job SET state=?, stage='finished', failure_reason=?, artifact_dir=?, input_tokens=?, output_tokens=?, total_tokens=?, comments=?, tool_calls=?, finished_at=?, lease_owner='', lease_until=NULL
WHERE id=? AND (state<>? OR ?=?)`, state, reason, artifactDir, usage.InputTokens, usage.OutputTokens, usage.TotalTokens, usage.Comments, usage.ToolCalls, now(), id, StateStale, state, StateStale)
	if err == nil {
		_ = s.RecordEvent(ctx, ReviewEvent{ReviewJobID: id, EventType: "job_finished", Stage: "finished", SafeMessage: reason, InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, TotalTokens: usage.TotalTokens})
	}
	return err
}
func (s *Store) Retry(ctx context.Context, id int64, reason, artifactDir string, usage Usage) error {
	return s.RetryAfter(ctx, id, reason, artifactDir, usage, time.Time{})
}

func (s *Store) RetryAfter(ctx context.Context, id int64, reason, artifactDir string, usage Usage, notBefore time.Time) error {
	var retryAt any
	if !notBefore.IsZero() {
		retryAt = notBefore.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE review_job SET state=?, stage='queued', failure_reason=?, artifact_dir=?, input_tokens=?, output_tokens=?, total_tokens=?, comments=?, tool_calls=?, lease_owner='', lease_until=?
WHERE id=? AND state<>?`, StateRetryWait, reason, artifactDir, usage.InputTokens, usage.OutputTokens, usage.TotalTokens, usage.Comments, usage.ToolCalls, retryAt, id, StateStale)
	if err == nil {
		_ = s.RecordEvent(ctx, ReviewEvent{ReviewJobID: id, EventType: "retry_scheduled", Stage: "queued", SafeMessage: reason, InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, TotalTokens: usage.TotalTokens})
	}
	return err
}

func (s *Store) MarkStale(ctx context.Context, id int64, reason string) error {
	return s.Finish(ctx, id, StateStale, reason, "", Usage{})
}

func (s *Store) RecoverInterrupted(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
UPDATE review_job SET state=?, stage='queued', lease_owner='', lease_until=NULL, failure_reason='worker interrupted; resuming OCR source session'
WHERE state IN (?,?)`, StateRetryWait, StateRunning, StatePublishing)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	return count, err
}

func (s *Store) RecoverExpired(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE review_job SET state=?, stage='queued', lease_owner='', lease_until=NULL, failure_reason='worker lease expired'
WHERE state IN (?,?,?) AND lease_until IS NOT NULL AND lease_until < ?`, StateRetryWait, StateRunning, StatePublishing, now())
	return err
}

func (s *Store) Dashboard(ctx context.Context) (Dashboard, error) {
	var d Dashboard
	err := s.db.QueryRowContext(ctx, `
SELECT
 COALESCE(SUM(CASE WHEN state='queued' THEN 1 ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN state='running' THEN 1 ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN state='retry_wait' THEN 1 ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN state='publishing' THEN 1 ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN state IN ('completed_fail','rejected_rule_missing','rejected_rule_invalid','failed_infra') THEN 1 ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN state='failed_infra' THEN 1 ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN state='completed_pass' THEN 1 ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN state='stale' THEN 1 ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN state='canceled' THEN 1 ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN finished_at >= date('now') THEN total_tokens ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN finished_at >= date('now','start of month') THEN total_tokens ELSE 0 END),0)
FROM review_job`).Scan(&d.Queued, &d.Running, &d.RetryWait, &d.Publishing, &d.Failed, &d.FailedInfra, &d.Passed, &d.Stale, &d.Canceled, &d.TodayTokens, &d.MonthTokens)
	return d, err
}

type Usage struct {
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
	Comments     int64
	ToolCalls    int64
}

const selectJob = `SELECT id,project_id,mr_iid,title,web_url,source_project_id,source_branch,target_project_id,target_branch,head_sha,target_sha,base_sha,rule_sha256,state,stage,priority,attempt,failure_reason,queued_at,started_at,finished_at,lease_owner,lease_until,artifact_dir,repo_dir,llm_provider,llm_model,session_id,tool_calls,input_tokens,output_tokens,total_tokens,comments,progress_completed,progress_total FROM review_job`

type scanner interface{ Scan(...any) error }

func scanJob(s scanner) (*ReviewJob, error) {
	var j ReviewJob
	var queued string
	var started, finished, leaseUntil sql.NullString
	if err := s.Scan(&j.ID, &j.ProjectID, &j.MRIID, &j.Title, &j.WebURL, &j.SourceProjectID, &j.SourceBranch, &j.TargetProjectID, &j.TargetBranch, &j.HeadSHA, &j.TargetSHA, &j.BaseSHA, &j.RuleSHA256, &j.State, &j.Stage, &j.Priority, &j.Attempt, &j.FailureReason, &queued, &started, &finished, &j.LeaseOwner, &leaseUntil, &j.ArtifactDir, &j.RepoDir, &j.LLMProvider, &j.LLMModel, &j.SessionID, &j.ToolCalls, &j.InputTokens, &j.OutputTokens, &j.TotalTokens, &j.Comments, &j.ProgressCompleted, &j.ProgressTotal); err != nil {
		return nil, err
	}
	j.QueuedAt, _ = time.Parse(time.RFC3339Nano, queued)
	j.StartedAt = parseNullableTime(started)
	j.FinishedAt = parseNullableTime(finished)
	j.LeaseUntil = parseNullableTime(leaseUntil)
	return &j, nil
}

func parseNullableTime(value sql.NullString) *time.Time {
	if !value.Valid {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil
	}
	return &parsed
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func (s *Store) Close() error   { return s.db.Close() }
func (s *Store) String() string { return fmt.Sprintf("Store(%p)", s.db) }
