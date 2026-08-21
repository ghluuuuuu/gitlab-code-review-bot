package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type ReviewListQuery struct {
	Scope       string
	States      []string
	Stages      []string
	ProjectID   int64
	MRIID       int64
	HasBlocking *bool
	Page        int
	PageSize    int
	Sort        string
}

type ReviewPage struct {
	Items    []ReviewJob `json:"items"`
	Page     int         `json:"page"`
	PageSize int         `json:"page_size"`
	Total    int         `json:"total"`
	HasNext  bool        `json:"has_next"`
}

type ReviewEvent struct {
	ID           int64     `json:"id"`
	ReviewJobID  int64     `json:"review_job_id"`
	EventType    string    `json:"event_type"`
	Stage        string    `json:"stage,omitempty"`
	SafeMessage  string    `json:"safe_message,omitempty"`
	Completed    int       `json:"completed,omitempty"`
	Total        int       `json:"total,omitempty"`
	InputTokens  int64     `json:"input_tokens,omitempty"`
	OutputTokens int64     `json:"output_tokens,omitempty"`
	TotalTokens  int64     `json:"total_tokens,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}
type ReviewFinding struct {
	ID             int64     `json:"id"`
	ReviewJobID    int64     `json:"review_job_id"`
	Path           string    `json:"path"`
	Content        string    `json:"content"`
	SuggestionCode string    `json:"suggestion_code,omitempty"`
	ExistingCode   string    `json:"existing_code,omitempty"`
	StartLine      int       `json:"start_line"`
	EndLine        int       `json:"end_line"`
	Category       string    `json:"category,omitempty"`
	Severity       string    `json:"severity,omitempty"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type ProjectQualityAnalytics struct {
	ProjectID      int64
	ReviewCount    int
	PassedReviews  int
	FailedReviews  int
	FindingCount   int
	BlockingCount  int
	SeverityCounts map[string]int
	CategoryCounts map[string]int
}

type AuditEvent struct {
	ID          int64     `json:"id"`
	Actor       string    `json:"actor"`
	Action      string    `json:"action"`
	ReviewJobID *int64    `json:"review_job_id,omitempty"`
	Detail      string    `json:"detail,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type UsageSummary struct {
	InputTokens    int64 `json:"input_tokens"`
	OutputTokens   int64 `json:"output_tokens"`
	TotalTokens    int64 `json:"total_tokens"`
	Comments       int64 `json:"comments"`
	ToolCalls      int64 `json:"tool_calls"`
	ReviewCount    int64 `json:"review_count"`
	FailedReviews  int64 `json:"failed_reviews"`
	RetriedReviews int64 `json:"retried_reviews"`
	StaleReviews   int64 `json:"stale_reviews"`
}

type UsageTrendPoint struct {
	Date         string `json:"date"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	TotalTokens  int64  `json:"total_tokens"`
	ReviewCount  int64  `json:"review_count"`
}
type UsageGroup struct {
	ProjectID    int64  `json:"project_id,omitempty"`
	Provider     string `json:"provider,omitempty"`
	Model        string `json:"model,omitempty"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	TotalTokens  int64  `json:"total_tokens"`
	ReviewCount  int64  `json:"review_count"`
}

func (s *Store) ListReviews(ctx context.Context, q ReviewListQuery) (ReviewPage, error) {
	if q.Page <= 0 {
		q.Page = 1
	}
	if q.PageSize <= 0 || q.PageSize > 200 {
		q.PageSize = 50
	}
	where, args := reviewWhere(q)
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM review_job WHERE `+where, args...).Scan(&total); err != nil {
		return ReviewPage{}, err
	}
	orderBy := "queued_at DESC, id DESC"
	switch q.Sort {
	case "priority.desc":
		orderBy = "priority DESC, queued_at ASC, id ASC"
	case "updated_at.asc":
		orderBy = "COALESCE(finished_at, started_at, queued_at) ASC, id ASC"
	case "updated_at.desc":
		orderBy = "COALESCE(finished_at, started_at, queued_at) DESC, id DESC"
	case "tokens.desc":
		orderBy = "total_tokens DESC, id DESC"
	}
	offset := (q.Page - 1) * q.PageSize
	rows, err := s.db.QueryContext(ctx, selectJob+` WHERE `+where+` ORDER BY `+orderBy+` LIMIT ? OFFSET ?`, append(args, q.PageSize, offset)...)
	if err != nil {
		return ReviewPage{}, err
	}
	defer rows.Close()
	items := make([]ReviewJob, 0, q.PageSize)
	for rows.Next() {
		job, scanErr := scanJob(rows)
		if scanErr != nil {
			return ReviewPage{}, scanErr
		}
		items = append(items, *job)
	}
	if err := rows.Err(); err != nil {
		return ReviewPage{}, err
	}
	return ReviewPage{Items: items, Page: q.Page, PageSize: q.PageSize, Total: total, HasNext: offset+len(items) < total}, nil
}

func reviewWhere(q ReviewListQuery) (string, []any) {
	clauses := []string{"1=1"}
	args := make([]any, 0, 8)
	switch q.Scope {
	case "active":
		clauses = append(clauses, "state IN ('queued','running','retry_wait','publishing')")
	case "history":
		clauses = append(clauses, "state IN ('completed_pass','completed_fail','rejected_rule_missing','rejected_rule_invalid','stale','failed_infra','canceled')")
	}
	if len(q.States) > 0 {
		values := make([]string, 0, len(q.States))
		for _, value := range q.States {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			values = append(values, "?")
			args = append(args, value)
		}
		if len(values) > 0 {
			clauses = append(clauses, "state IN ("+strings.Join(values, ",")+")")
		}
	}
	if len(q.Stages) > 0 {
		values := make([]string, 0, len(q.Stages))
		for _, value := range q.Stages {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			values = append(values, "?")
			args = append(args, value)
		}
		if len(values) > 0 {
			clauses = append(clauses, "stage IN ("+strings.Join(values, ",")+")")
		}
	}
	if q.ProjectID > 0 {
		clauses = append(clauses, "target_project_id=?")
		args = append(args, q.ProjectID)
	}
	if q.MRIID > 0 {
		clauses = append(clauses, "mr_iid=?")
		args = append(args, q.MRIID)
	}
	return strings.Join(clauses, " AND "), args
}

func (s *Store) ListReviewRevisions(ctx context.Context, projectID, mrIID int64) ([]ReviewJob, error) {
	rows, err := s.db.QueryContext(ctx, selectJob+` WHERE project_id=? AND mr_iid=? ORDER BY queued_at DESC, id DESC`, projectID, mrIID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ReviewJob, 0)
	for rows.Next() {
		job, scanErr := scanJob(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, *job)
	}
	return result, rows.Err()
}

func (s *Store) RecordEvent(ctx context.Context, event ReviewEvent) error {
	if event.ReviewJobID <= 0 || strings.TrimSpace(event.EventType) == "" {
		return errors.New("review event requires job id and type")
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	message := truncateStoreText(event.SafeMessage, 500)
	event.SafeMessage = message
	if event.EventType == "progress_updated" || event.EventType == "usage_updated" {
		result, err := s.db.ExecContext(ctx, `UPDATE review_event SET stage=?,safe_message=?,completed=?,total=?,input_tokens=?,output_tokens=?,total_tokens=?,created_at=? WHERE id=(SELECT id FROM review_event WHERE review_job_id=? AND event_type=? AND created_at>=? ORDER BY id DESC LIMIT 1)`, event.Stage, message, event.Completed, event.Total, event.InputTokens, event.OutputTokens, event.TotalTokens, event.CreatedAt.UTC().Format(time.RFC3339Nano), event.ReviewJobID, event.EventType, event.CreatedAt.Add(-5*time.Second).UTC().Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
		if count, err := result.RowsAffected(); err == nil && count == 1 {
			s.publishEvent(event)
			return nil
		}
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO review_event(review_job_id,event_type,stage,safe_message,completed,total,input_tokens,output_tokens,total_tokens,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, event.ReviewJobID, event.EventType, event.Stage, message, event.Completed, event.Total, event.InputTokens, event.OutputTokens, event.TotalTokens, event.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	event.ID, _ = result.LastInsertId()
	s.publishEvent(event)
	return nil
}

func (s *Store) SubscribeEvents(jobID int64) (<-chan ReviewEvent, func()) {
	s.eventMu.Lock()
	s.nextSubID++
	id := s.nextSubID
	ch := make(chan ReviewEvent, 64)
	s.subscribers[id] = eventSubscriber{jobID: jobID, ch: ch}
	s.eventMu.Unlock()
	return ch, func() {
		s.eventMu.Lock()
		if subscriber, exists := s.subscribers[id]; exists {
			delete(s.subscribers, id)
			close(subscriber.ch)
		}
		s.eventMu.Unlock()
	}
}

func (s *Store) publishEvent(event ReviewEvent) {
	s.eventMu.RLock()
	defer s.eventMu.RUnlock()
	for _, subscriber := range s.subscribers {
		if subscriber.jobID != 0 && subscriber.jobID != event.ReviewJobID {
			continue
		}
		select {
		case subscriber.ch <- event:
		default:
			select {
			case <-subscriber.ch:
			default:
			}
			select {
			case subscriber.ch <- event:
			default:
			}
		}
	}
}

func (s *Store) RecordFinding(ctx context.Context, finding ReviewFinding) error {
	if finding.ReviewJobID <= 0 {
		return errors.New("review finding requires job id")
	}
	if finding.Status == "" {
		finding.Status = "current"
	}
	nowValue := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `INSERT INTO review_finding(review_job_id,path,content,suggestion_code,existing_code,start_line,end_line,category,severity,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(review_job_id,path,start_line,end_line,category,severity,content) DO UPDATE SET suggestion_code=excluded.suggestion_code,existing_code=excluded.existing_code,status=excluded.status,updated_at=excluded.updated_at`, finding.ReviewJobID, finding.Path, finding.Content, finding.SuggestionCode, finding.ExistingCode, finding.StartLine, finding.EndLine, finding.Category, finding.Severity, finding.Status, nowValue.Format(time.RFC3339Nano), nowValue.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	return s.RecordEvent(ctx, ReviewEvent{ReviewJobID: finding.ReviewJobID, EventType: "finding_updated", Stage: "ocr_review", SafeMessage: strings.TrimSpace(strings.Join([]string{finding.Path, finding.Severity, finding.Category}, " "))})
}

func (s *Store) ReplaceFindings(ctx context.Context, jobID int64, findings []ReviewFinding) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM review_finding WHERE review_job_id=?`, jobID); err != nil {
		return err
	}
	for _, finding := range findings {
		finding.ReviewJobID = jobID
		if err := s.RecordFinding(ctx, finding); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListFindings(ctx context.Context, jobID int64) ([]ReviewFinding, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,review_job_id,path,content,suggestion_code,existing_code,start_line,end_line,category,severity,status,created_at,updated_at FROM review_finding WHERE review_job_id=? ORDER BY severity,path,start_line,id`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ReviewFinding, 0)
	for rows.Next() {
		var finding ReviewFinding
		var created, updated string
		if err := rows.Scan(&finding.ID, &finding.ReviewJobID, &finding.Path, &finding.Content, &finding.SuggestionCode, &finding.ExistingCode, &finding.StartLine, &finding.EndLine, &finding.Category, &finding.Severity, &finding.Status, &created, &updated); err != nil {
			return nil, err
		}
		finding.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		finding.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		result = append(result, finding)
	}
	return result, rows.Err()
}

func (s *Store) ListEvents(ctx context.Context, jobID int64, limit int) ([]ReviewEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,review_job_id,event_type,stage,safe_message,completed,total,input_tokens,output_tokens,total_tokens,created_at FROM review_event WHERE review_job_id=? ORDER BY id DESC LIMIT ?`, jobID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ReviewEvent, 0)
	for rows.Next() {
		var event ReviewEvent
		var created string
		if err := rows.Scan(&event.ID, &event.ReviewJobID, &event.EventType, &event.Stage, &event.SafeMessage, &event.Completed, &event.Total, &event.InputTokens, &event.OutputTokens, &event.TotalTokens, &created); err != nil {
			return nil, err
		}
		event.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		result = append(result, event)
	}
	return result, rows.Err()
}

func (s *Store) RecordAudit(ctx context.Context, actor, action string, jobID *int64, detail string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_event(actor,action,review_job_id,detail,created_at) VALUES(?,?,?,?,?)`, truncateStoreText(actor, 120), truncateStoreText(action, 120), jobID, truncateStoreText(detail, 2000), now())
	return err
}

func (s *Store) ListAuditEvents(ctx context.Context, limit int) ([]AuditEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,actor,action,review_job_id,detail,created_at FROM audit_event ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]AuditEvent, 0)
	for rows.Next() {
		var event AuditEvent
		var jobID sql.NullInt64
		var created string
		if err := rows.Scan(&event.ID, &event.Actor, &event.Action, &jobID, &event.Detail, &created); err != nil {
			return nil, err
		}
		if jobID.Valid {
			value := jobID.Int64
			event.ReviewJobID = &value
		}
		event.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		result = append(result, event)
	}
	return result, rows.Err()
}

func (s *Store) CancelReview(ctx context.Context, id int64, reason string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE review_job SET state=?, stage='finished', failure_reason=?, finished_at=?, lease_owner='', lease_until=NULL WHERE id=? AND state IN ('queued','running','retry_wait','publishing')`, StateCanceled, truncateStoreText(reason, 1000), now(), id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("review is no longer active")
	}
	_ = s.RecordEvent(ctx, ReviewEvent{ReviewJobID: id, EventType: "job_canceled", SafeMessage: reason})
	return nil
}

func (s *Store) RetryReview(ctx context.Context, id int64, reason string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE review_job SET state=?, stage='queued', failure_reason=?, finished_at=NULL, lease_owner='', lease_until=NULL WHERE id=? AND state IN ('failed_infra','completed_fail','rejected_rule_missing','rejected_rule_invalid','canceled')`, StateQueued, truncateStoreText(reason, 1000), id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("review cannot be retried from its current state")
	}
	_ = s.RecordEvent(ctx, ReviewEvent{ReviewJobID: id, EventType: "retry_scheduled", SafeMessage: reason})
	return nil
}

func (s *Store) SetPriority(ctx context.Context, id int64, priority int, reason string) error {
	if priority < -1000 {
		priority = -1000
	}
	if priority > 1000 {
		priority = 1000
	}
	result, err := s.db.ExecContext(ctx, `UPDATE review_job SET priority=? WHERE id=? AND state IN ('queued','retry_wait')`, priority, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("review priority can only change for queued work")
	}
	_ = s.RecordEvent(ctx, ReviewEvent{ReviewJobID: id, EventType: "priority_changed", SafeMessage: reason})
	return nil
}

func (s *Store) ProjectQualityAnalytics(ctx context.Context, from, to time.Time) ([]ProjectQualityAnalytics, error) {
	fromValue, toValue := from.UTC().Format(time.RFC3339Nano), to.UTC().Format(time.RFC3339Nano)
	byProject := make(map[int64]*ProjectQualityAnalytics)
	rows, err := s.db.QueryContext(ctx, `SELECT target_project_id,COUNT(*),COALESCE(SUM(CASE WHEN state='completed_pass' THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN state='completed_fail' THEN 1 ELSE 0 END),0) FROM review_job WHERE queued_at>=? AND queued_at<? AND state IN ('completed_pass','completed_fail') GROUP BY target_project_id`, fromValue, toValue)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		item := &ProjectQualityAnalytics{SeverityCounts: make(map[string]int), CategoryCounts: make(map[string]int)}
		if err := rows.Scan(&item.ProjectID, &item.ReviewCount, &item.PassedReviews, &item.FailedReviews); err != nil {
			rows.Close()
			return nil, err
		}
		byProject[item.ProjectID] = item
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	findingRows, err := s.db.QueryContext(ctx, `SELECT j.target_project_id,LOWER(COALESCE(NULLIF(TRIM(f.severity),''),'unknown')),LOWER(COALESCE(NULLIF(TRIM(f.category),''),'other')),COUNT(*) FROM review_job j JOIN review_finding f ON f.review_job_id=j.id WHERE j.queued_at>=? AND j.queued_at<? AND j.state IN ('completed_pass','completed_fail') GROUP BY j.target_project_id,2,3`, fromValue, toValue)
	if err != nil {
		return nil, err
	}
	defer findingRows.Close()
	for findingRows.Next() {
		var projectID int64
		var severity, category string
		var count int
		if err := findingRows.Scan(&projectID, &severity, &category, &count); err != nil {
			return nil, err
		}
		item := byProject[projectID]
		if item == nil {
			item = &ProjectQualityAnalytics{ProjectID: projectID, SeverityCounts: make(map[string]int), CategoryCounts: make(map[string]int)}
			byProject[projectID] = item
		}
		item.FindingCount += count
		if severity == "critical" || severity == "high" {
			item.BlockingCount += count
		}
		item.SeverityCounts[severity] += count
		item.CategoryCounts[category] += count
	}
	if err := findingRows.Err(); err != nil {
		return nil, err
	}
	result := make([]ProjectQualityAnalytics, 0, len(byProject))
	for _, item := range byProject {
		result = append(result, *item)
	}
	return result, nil
}

func (s *Store) UsageSummary(ctx context.Context, from, to time.Time) (UsageSummary, error) {
	var summary UsageSummary
	args := []any{}
	where := "1=1"
	if !from.IsZero() {
		where += " AND queued_at>=?"
		args = append(args, from.UTC().Format(time.RFC3339Nano))
	}
	if !to.IsZero() {
		where += " AND queued_at<?"
		args = append(args, to.UTC().Format(time.RFC3339Nano))
	}
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(input_tokens),0),COALESCE(SUM(output_tokens),0),COALESCE(SUM(total_tokens),0),COALESCE(SUM(comments),0),COALESCE(SUM(tool_calls),0),COUNT(*),COALESCE(SUM(CASE WHEN state IN ('completed_fail','failed_infra','rejected_rule_missing','rejected_rule_invalid') THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN attempt>1 THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN state='stale' THEN 1 ELSE 0 END),0) FROM review_job WHERE `+where, args...).Scan(&summary.InputTokens, &summary.OutputTokens, &summary.TotalTokens, &summary.Comments, &summary.ToolCalls, &summary.ReviewCount, &summary.FailedReviews, &summary.RetriedReviews, &summary.StaleReviews)
	return summary, err
}

func (s *Store) UsageTrend(ctx context.Context, from, to time.Time) ([]UsageTrendPoint, error) {
	args := []any{from.UTC().Format("2006-01-02T15:04:05Z07:00"), to.UTC().Format("2006-01-02T15:04:05Z07:00")}
	rows, err := s.db.QueryContext(ctx, `SELECT substr(COALESCE(finished_at,queued_at),1,10),COALESCE(SUM(input_tokens),0),COALESCE(SUM(output_tokens),0),COALESCE(SUM(total_tokens),0),COUNT(*) FROM review_job WHERE queued_at>=? AND queued_at<? GROUP BY substr(COALESCE(finished_at,queued_at),1,10) ORDER BY 1`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]UsageTrendPoint, 0)
	for rows.Next() {
		var point UsageTrendPoint
		if err := rows.Scan(&point.Date, &point.InputTokens, &point.OutputTokens, &point.TotalTokens, &point.ReviewCount); err != nil {
			return nil, err
		}
		result = append(result, point)
	}
	return result, rows.Err()
}

func (s *Store) UsageByProject(ctx context.Context, from, to time.Time, limit int) ([]UsageGroup, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT target_project_id,COALESCE(SUM(input_tokens),0),COALESCE(SUM(output_tokens),0),COALESCE(SUM(total_tokens),0),COUNT(*) FROM review_job WHERE queued_at>=? AND queued_at<? GROUP BY target_project_id ORDER BY SUM(total_tokens) DESC LIMIT ?`, from.UTC().Format(time.RFC3339Nano), to.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]UsageGroup, 0)
	for rows.Next() {
		var group UsageGroup
		if err := rows.Scan(&group.ProjectID, &group.InputTokens, &group.OutputTokens, &group.TotalTokens, &group.ReviewCount); err != nil {
			return nil, err
		}
		result = append(result, group)
	}
	return result, rows.Err()
}

func (s *Store) UsageByModel(ctx context.Context, from, to time.Time, limit int) ([]UsageGroup, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT llm_provider,llm_model,COALESCE(SUM(input_tokens),0),COALESCE(SUM(output_tokens),0),COALESCE(SUM(total_tokens),0),COUNT(*) FROM review_job WHERE queued_at>=? AND queued_at<? GROUP BY llm_provider,llm_model ORDER BY SUM(total_tokens) DESC LIMIT ?`, from.UTC().Format(time.RFC3339Nano), to.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]UsageGroup, 0)
	for rows.Next() {
		var group UsageGroup
		if err := rows.Scan(&group.Provider, &group.Model, &group.InputTokens, &group.OutputTokens, &group.TotalTokens, &group.ReviewCount); err != nil {
			return nil, err
		}
		result = append(result, group)
	}
	return result, rows.Err()
}
func truncateStoreText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
