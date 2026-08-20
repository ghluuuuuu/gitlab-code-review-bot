package main

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/config"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/gitlab"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/publisher"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/review"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/store"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/workspace"
)

type qualityAuthor struct {
	ID        int64  `json:"id,omitempty"`
	Name      string `json:"name"`
	Username  string `json:"username,omitempty"`
	AvatarURL string `json:"avatar_url,omitempty"`
	WebURL    string `json:"web_url,omitempty"`
}

type qualityMR struct {
	ProjectID           int64                     `json:"project_id"`
	MRIID               int64                     `json:"mr_iid"`
	Title               string                    `json:"title"`
	WebURL              string                    `json:"web_url"`
	SourceBranch        string                    `json:"source_branch"`
	TargetBranch        string                    `json:"target_branch"`
	State               string                    `json:"state"`
	Reviewed            bool                      `json:"reviewed"`
	UpdatedAt           string                    `json:"updated_at"`
	Author              qualityAuthor             `json:"author"`
	IssueCounts         map[string]int            `json:"issue_counts"`
	SeverityCounts      map[string]int            `json:"severity_counts"`
	FileIssueCounts     map[string]int            `json:"file_issue_counts"`
	FileIssueTypeCounts map[string]map[string]int `json:"file_issue_type_counts"`
	FileBlockingCounts  map[string]int            `json:"file_blocking_counts"`
	ChangeAnalysis      string                    `json:"change_analysis,omitempty"`
	ReportURL           string                    `json:"report_url,omitempty"`
}

type qualityProject struct {
	ID                int64  `json:"id"`
	Name              string `json:"name"`
	Description       string `json:"description"`
	PathWithNamespace string `json:"path_with_namespace"`
	WebURL            string `json:"web_url"`
	TechStack         string `json:"tech_stack"`
}
type qualityBranch struct {
	Name              string `json:"name"`
	ChangedFiles      int    `json:"changed_files"`
	OpenMergeRequests int    `json:"open_merge_requests"`
}

type qualityBranchRelation struct {
	Source       string `json:"source"`
	Target       string `json:"target"`
	Kind         string `json:"kind"`
	MRIID        int64  `json:"mr_iid"`
	Title        string `json:"title"`
	State        string `json:"state"`
	WebURL       string `json:"web_url,omitempty"`
	ChangedFiles int    `json:"changed_files"`
}

type qualityBranchGraph struct {
	ProjectID int64                   `json:"project_id"`
	Branches  []qualityBranch         `json:"branches"`
	Relations []qualityBranchRelation `json:"relations"`
}

type branchDiffCacheEntry struct {
	Count     int
	ExpiresAt time.Time
}

var branchDiffCache = struct {
	sync.Mutex
	Items map[string]branchDiffCacheEntry
}{Items: make(map[string]branchDiffCacheEntry)}

type branchRefsCacheEntry struct {
	Names     []string
	ExpiresAt time.Time
}

var branchRefsCache = struct {
	sync.Mutex
	Items map[string]branchRefsCacheEntry
}{Items: make(map[string]branchRefsCacheEntry)}

type commitSequenceCacheEntry struct {
	Count     int64
	ExpiresAt time.Time
}

var commitSequenceCache = struct {
	sync.Mutex
	Items map[string]commitSequenceCacheEntry
}{Items: make(map[string]commitSequenceCacheEntry)}

func mergeRequestChangedFileCount(ctx context.Context, gl *gitlab.Client, projectID int64, mr gitlab.MergeRequest) (int, error) {
	key := fmt.Sprintf("%d:%d:%s", projectID, mr.IID, mr.UpdatedAt)
	now := time.Now().UTC()
	branchDiffCache.Lock()
	cached, exists := branchDiffCache.Items[key]
	branchDiffCache.Unlock()
	if exists && cached.ExpiresAt.After(now) {
		return cached.Count, nil
	}
	diffs, err := gl.ListMergeRequestDiffs(ctx, projectID, mr.IID)
	if err != nil {
		return 0, err
	}
	paths := make(map[string]struct{}, len(diffs))
	for _, diff := range diffs {
		path := strings.TrimSpace(diff.NewPath)
		if path == "" {
			path = strings.TrimSpace(diff.OldPath)
		}
		if path != "" {
			paths[path] = struct{}{}
		}
	}
	entry := branchDiffCacheEntry{Count: len(paths), ExpiresAt: now.Add(10 * time.Minute)}
	branchDiffCache.Lock()
	branchDiffCache.Items[key] = entry
	branchDiffCache.Unlock()
	return entry.Count, nil
}

func commitContainingBranches(ctx context.Context, gl *gitlab.Client, projectID int64, commitSHA string) ([]string, error) {
	key := fmt.Sprintf("%d:%s", projectID, commitSHA)
	now := time.Now().UTC()
	branchRefsCache.Lock()
	cached, exists := branchRefsCache.Items[key]
	branchRefsCache.Unlock()
	if exists && cached.ExpiresAt.After(now) {
		return append([]string(nil), cached.Names...), nil
	}
	refs, err := gl.ListCommitBranchRefs(ctx, projectID, commitSHA)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref.Type == "branch" && strings.TrimSpace(ref.Name) != "" {
			names = append(names, ref.Name)
		}
	}
	branchRefsCache.Lock()
	branchRefsCache.Items[key] = branchRefsCacheEntry{Names: append([]string(nil), names...), ExpiresAt: now.Add(10 * time.Minute)}
	branchRefsCache.Unlock()
	return names, nil
}

func commitSequence(ctx context.Context, gl *gitlab.Client, projectID int64, commitSHA string) (int64, error) {
	key := fmt.Sprintf("%d:%s", projectID, commitSHA)
	now := time.Now().UTC()
	commitSequenceCache.Lock()
	cached, exists := commitSequenceCache.Items[key]
	commitSequenceCache.Unlock()
	if exists && cached.ExpiresAt.After(now) {
		return cached.Count, nil
	}
	count, err := gl.GetCommitSequence(ctx, projectID, commitSHA)
	if err != nil {
		return 0, err
	}
	commitSequenceCache.Lock()
	commitSequenceCache.Items[key] = commitSequenceCacheEntry{Count: count, ExpiresAt: now.Add(10 * time.Minute)}
	commitSequenceCache.Unlock()
	return count, nil
}

func inferGitBranchRelations(ctx context.Context, gl *gitlab.Client, projectID int64, branches []gitlab.Branch) ([]qualityBranchRelation, error) {
	branchTips := make(map[string]string, len(branches))
	for _, branch := range branches {
		name, sha := strings.TrimSpace(branch.Name), strings.TrimSpace(branch.Commit.ID)
		if name != "" && sha != "" {
			branchTips[name] = sha
		}
	}
	reachable := make(map[string]map[string]bool, len(branchTips))
	sequences := make(map[string]int64, len(branchTips))
	var resultMu sync.Mutex
	var loadWG sync.WaitGroup
	semaphore := make(chan struct{}, 6)
	errCh := make(chan error, len(branchTips))
	for source, sourceSHA := range branchTips {
		source, sourceSHA := source, sourceSHA
		loadWG.Add(1)
		go func() {
			defer loadWG.Done()
			semaphore <- struct{}{}
			containing, refsErr := commitContainingBranches(ctx, gl, projectID, sourceSHA)
			sequence, sequenceErr := commitSequence(ctx, gl, projectID, sourceSHA)
			<-semaphore
			if refsErr != nil {
				errCh <- refsErr
				return
			}
			if sequenceErr != nil {
				errCh <- sequenceErr
				return
			}
			targets := make(map[string]bool)
			for _, target := range containing {
				if targetSHA, exists := branchTips[target]; exists && target != source && targetSHA != sourceSHA {
					targets[target] = true
				}
			}
			resultMu.Lock()
			reachable[source] = targets
			sequences[source] = sequence
			resultMu.Unlock()
		}()
	}
	loadWG.Wait()
	close(errCh)
	if err := <-errCh; err != nil {
		return nil, err
	}
	result := make([]qualityBranchRelation, 0, len(branchTips))
	for source, targets := range reachable {
		sourceSequence := sequences[source]
		nearestTarget := ""
		var nearestSequence int64
		for target := range targets {
			targetSequence := sequences[target]
			if targetSequence <= sourceSequence {
				continue
			}
			if nearestTarget == "" || targetSequence < nearestSequence || (targetSequence == nearestSequence && target < nearestTarget) {
				nearestTarget = target
				nearestSequence = targetSequence
			}
		}
		if nearestTarget != "" {
			result = append(result, qualityBranchRelation{Source: source, Target: nearestTarget, Kind: "git", Title: "Git 提交树最近关系", State: "inferred"})
		}
	}
	return result, nil
}

func loadProjectBranchGraph(ctx context.Context, st *store.Store, gl *gitlab.Client, projectID int64) (qualityBranchGraph, error) {
	branches, err := gl.ListBranches(ctx, projectID)
	if err != nil {
		return qualityBranchGraph{}, err
	}
	mrs, err := gl.ListProjectMergeRequests(ctx, projectID)
	if err != nil {
		return qualityBranchGraph{}, err
	}
	jobs, err := st.ListAllReviews(ctx)
	if err != nil {
		return qualityBranchGraph{}, err
	}
	latestJobs := make(map[int64]store.ReviewJob)
	for _, job := range jobs {
		if job.TargetProjectID == projectID {
			if _, exists := latestJobs[job.MRIID]; !exists {
				latestJobs[job.MRIID] = job
			}
		}
	}
	changedByMR := make(map[int64]int, len(mrs))
	var changedMu sync.Mutex
	var changedWG sync.WaitGroup
	semaphore := make(chan struct{}, 6)
	for _, mr := range mrs {
		mr := mr
		changedWG.Add(1)
		go func() {
			defer changedWG.Done()
			semaphore <- struct{}{}
			count, diffErr := mergeRequestChangedFileCount(ctx, gl, projectID, mr)
			<-semaphore
			if diffErr != nil {
				if job, exists := latestJobs[mr.IID]; exists {
					count = job.ProgressTotal
				}
			}
			changedMu.Lock()
			changedByMR[mr.IID] = count
			changedMu.Unlock()
		}()
	}
	changedWG.Wait()
	branchFiles := make(map[string]int)
	for _, mr := range mrs {
		branchFiles[mr.SourceBranch] += changedByMR[mr.IID]
		branchFiles[mr.TargetBranch] += changedByMR[mr.IID]
	}
	openMergeRequests := make(map[string]int)
	for _, mr := range mrs {
		if mr.State == "opened" {
			openMergeRequests[mr.SourceBranch]++
		}
	}
	seen := make(map[string]struct{}, len(branches))
	result := qualityBranchGraph{ProjectID: projectID, Branches: make([]qualityBranch, 0, len(branches)), Relations: make([]qualityBranchRelation, 0)}
	addBranch := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, exists := seen[name]; exists {
			return
		}
		seen[name] = struct{}{}
		result.Branches = append(result.Branches, qualityBranch{Name: name, ChangedFiles: branchFiles[name], OpenMergeRequests: openMergeRequests[name]})
	}
	for _, branch := range branches {
		addBranch(branch.Name)
	}
	forkSources := make(map[string]struct{})
	for _, mr := range mrs {
		addBranch(mr.SourceBranch)
		addBranch(mr.TargetBranch)
		if mr.SourceBranch != "" && mr.TargetBranch != "" {
			changedFiles := changedByMR[mr.IID]
			result.Relations = append(result.Relations, qualityBranchRelation{Source: mr.SourceBranch, Target: mr.TargetBranch, Kind: "mr", MRIID: mr.IID, Title: mr.Title, State: mr.State, WebURL: mr.WebURL, ChangedFiles: changedFiles})
			if _, exists := forkSources[mr.SourceBranch]; !exists {
				forkSources[mr.SourceBranch] = struct{}{}
				result.Relations = append(result.Relations, qualityBranchRelation{Source: mr.TargetBranch, Target: mr.SourceBranch, Kind: "fork", MRIID: mr.IID, Title: "Fork 来源（MR 目标分支推断）", State: "inferred", WebURL: mr.WebURL})
			}
		}
	}
	mrRelations := make(map[string]struct{}, len(result.Relations))
	for _, relation := range result.Relations {
		mrRelations[relation.Source+"\x00"+relation.Target] = struct{}{}
	}
	if inferred, inferErr := inferGitBranchRelations(ctx, gl, projectID, branches); inferErr == nil {
		for _, relation := range inferred {
			if _, exists := mrRelations[relation.Source+"\x00"+relation.Target]; exists {
				continue
			}
			result.Relations = append(result.Relations, qualityBranchRelation{Source: relation.Source, Target: relation.Target, Kind: "git", Title: "Git 提交树关系", State: "inferred"})
		}
	}
	sort.Slice(result.Branches, func(i, j int) bool { return result.Branches[i].Name < result.Branches[j].Name })
	relationKindOrder := map[string]int{"mr": 0, "git": 1, "fork": 2}
	sort.Slice(result.Relations, func(i, j int) bool {
		if result.Relations[i].Kind != result.Relations[j].Kind {
			return relationKindOrder[result.Relations[i].Kind] < relationKindOrder[result.Relations[j].Kind]
		}
		if result.Relations[i].MRIID != result.Relations[j].MRIID {
			return result.Relations[i].MRIID > result.Relations[j].MRIID
		}
		if result.Relations[i].Source != result.Relations[j].Source {
			return result.Relations[i].Source < result.Relations[j].Source
		}
		return result.Relations[i].Target < result.Relations[j].Target
	})
	return result, nil
}

type qualityFile struct {
	Path      string          `json:"path"`
	OldPath   string          `json:"old_path,omitempty"`
	Additions int             `json:"additions"`
	Deletions int             `json:"deletions"`
	Authors   []qualityAuthor `json:"authors"`
}

type qualityFileFinding struct {
	Content        string `json:"content"`
	SuggestionCode string `json:"suggestion_code,omitempty"`
	ExistingCode   string `json:"existing_code,omitempty"`
	StartLine      int    `json:"start_line"`
	EndLine        int    `json:"end_line"`
	Category       string `json:"category,omitempty"`
	Severity       string `json:"severity,omitempty"`
	Status         string `json:"status"`
}

type qualityFileDetail struct {
	Path     string               `json:"path"`
	Ref      string               `json:"ref"`
	Content  string               `json:"content"`
	Findings []qualityFileFinding `json:"findings"`
}

type fixTrendPoint struct {
	Time       string `json:"time"`
	IssueCount int    `json:"issue_count"`
	FixedCount int    `json:"fixed_count"`
}

func registerQualityRoutes(mux *http.ServeMux, st *store.Store, gl *gitlab.Client, cfg config.Config) {
	mux.HandleFunc("/api/v1/admin/quality/projects", func(w http.ResponseWriter, r *http.Request) {
		projects, err := loadQualityProjects(r.Context(), gl)
		writeJSON(w, projects, err)
	})
	mux.HandleFunc("/api/v1/admin/quality/projects/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/mrs") {
			value := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/quality/projects/"), "/mrs")
			projectID, err := strconv.ParseInt(strings.Trim(value, "/"), 10, 64)
			if err != nil || projectID <= 0 {
				http.Error(w, `{"error":"invalid project_id"}`, http.StatusBadRequest)
				return
			}
			mrs, loadErr := loadQualityMergeRequests(r.Context(), st, gl, cfg, projectID)
			writeJSON(w, mrs, loadErr)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/branches") {
			value := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/quality/projects/"), "/branches")
			projectID, err := strconv.ParseInt(strings.Trim(value, "/"), 10, 64)
			if err != nil || projectID <= 0 {
				http.Error(w, `{"error":"invalid project_id"}`, http.StatusBadRequest)
				return
			}
			branches, branchErr := loadProjectBranchGraph(r.Context(), st, gl, projectID)
			writeJSON(w, branches, branchErr)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/file") {
			projectID, mrIID, err := parseQualityMRPath(r.URL.Path, "file")
			if err != nil {
				http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
				return
			}
			path := strings.TrimSpace(r.URL.Query().Get("path"))
			if path == "" {
				http.Error(w, `{"error":"missing file path"}`, http.StatusBadRequest)
				return
			}
			detail, loadErr := loadQualityFileDetail(r.Context(), st, gl, projectID, mrIID, path)
			writeJSON(w, detail, loadErr)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/trend") {
			projectID, mrIID, err := parseQualityMRPath(r.URL.Path, "trend")
			if err != nil {
				http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
				return
			}
			points, loadErr := loadFixTrend(r.Context(), st, projectID, mrIID)
			writeJSON(w, points, loadErr)
			return
		}
		projectID, mrIID, err := parseQualityMRPath(r.URL.Path, "files")
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
			return
		}
		files, loadErr := loadQualityFiles(r.Context(), gl, projectID, mrIID)
		writeJSON(w, files, loadErr)
	})
}

func loadQualityProjects(ctx context.Context, gl *gitlab.Client) ([]qualityProject, error) {
	remoteProjects, err := gl.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]qualityProject, len(remoteProjects))
	for i, remoteProject := range remoteProjects {
		result[i] = qualityProject{
			ID: remoteProject.ID, Name: remoteProject.Name, Description: remoteProject.Description,
			PathWithNamespace: remoteProject.PathWithNamespace, WebURL: remoteProject.WebURL,
		}
	}

	jobs := make(chan int)
	var workers sync.WaitGroup
	workers.Add(min(8, len(result)))
	for range min(8, len(result)) {
		go func() {
			defer workers.Done()
			for index := range jobs {
				languages, languageErr := gl.GetProjectLanguages(ctx, result[index].ID)
				if languageErr == nil {
					result[index].TechStack = primaryTechnology(languages)
				}
			}
		}()
	}
	for index := range result {
		jobs <- index
	}
	close(jobs)
	workers.Wait()

	sort.Slice(result, func(i, j int) bool { return result[i].PathWithNamespace < result[j].PathWithNamespace })
	return result, nil
}

func primaryTechnology(languages map[string]float64) string {
	name := ""
	percentage := -1.0
	for candidate, candidatePercentage := range languages {
		if candidatePercentage > percentage || (candidatePercentage == percentage && candidate < name) {
			name = candidate
			percentage = candidatePercentage
		}
	}
	return name
}

func loadQualityMergeRequests(ctx context.Context, st *store.Store, gl *gitlab.Client, cfg config.Config, projectID int64) ([]qualityMR, error) {
	remoteMRs, err := gl.ListProjectMergeRequests(ctx, projectID)
	if err != nil {
		return nil, err
	}
	jobs, err := st.ListAllReviews(ctx)
	if err != nil {
		return nil, err
	}
	latest := make(map[int64]store.ReviewJob)
	for _, job := range jobs {
		if job.TargetProjectID == projectID {
			if _, exists := latest[job.MRIID]; !exists {
				latest[job.MRIID] = job
			}
		}
	}
	remoteProject, err := gl.GetProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	absDataDir, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	repoDir := filepath.Join(absDataDir, "workspaces", workspace.WorkloadName(remoteProject))
	reportPublisher := &publisher.Publisher{ViewerURL: cfg.Review.ViewerURL}
	result := make([]qualityMR, 0, len(remoteMRs))
	for _, remoteMR := range remoteMRs {
		job, reviewed := latest[remoteMR.IID]
		item := qualityMR{
			ProjectID: projectID, MRIID: remoteMR.IID, Title: remoteMR.Title, WebURL: remoteMR.WebURL,
			SourceBranch: remoteMR.SourceBranch, TargetBranch: remoteMR.TargetBranch, State: remoteMR.State,
			Reviewed: reviewed, UpdatedAt: remoteMR.UpdatedAt, Author: qualityAuthorFromUser(remoteMR.Author),
			IssueCounts: make(map[string]int), SeverityCounts: make(map[string]int), FileIssueCounts: make(map[string]int), FileIssueTypeCounts: make(map[string]map[string]int), FileBlockingCounts: make(map[string]int),
		}
		if reviewed {
			if item.Title == "" {
				item.Title = job.Title
			}
			if item.WebURL == "" {
				item.WebURL = job.WebURL
			}
			if item.SourceBranch == "" {
				item.SourceBranch = job.SourceBranch
			}
			if item.TargetBranch == "" {
				item.TargetBranch = job.TargetBranch
			}
			if item.UpdatedAt == "" {
				item.UpdatedAt = job.QueuedAt.Format(time.RFC3339)
			}
			if job.SessionID != "" {
				item.ReportURL = reportPublisher.ReportURL(job.SessionID, repoDir)
			}
			storedFindings, storedErr := st.ListFindings(ctx, job.ID)
			if storedErr == nil && len(storedFindings) > 0 {
				for _, finding := range storedFindings {
					addQualityFinding(&item, finding.Path, finding.Category, finding.Severity)
				}
			}
			if job.ArtifactDir != "" {
				if parsed, parseErr := review.ParseResult(filepath.Join(job.ArtifactDir, "ocr-result.json")); parseErr == nil {
					item.ChangeAnalysis = parsed.ChangeAnalysis
					if len(storedFindings) == 0 {
						for _, comment := range parsed.Comments {
							addQualityFinding(&item, comment.Path, comment.Category, comment.Severity)
						}
					}
				}
			}
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].UpdatedAt == result[j].UpdatedAt {
			return result[i].MRIID > result[j].MRIID
		}
		return result[i].UpdatedAt > result[j].UpdatedAt
	})
	return result, nil
}

func addQualityFinding(item *qualityMR, path, category, severity string) {
	if item == nil {
		return
	}
	category = strings.TrimSpace(strings.ToLower(category))
	if category == "" {
		category = "other"
	}
	severity = strings.TrimSpace(strings.ToLower(severity))
	if severity == "" {
		severity = "unknown"
	}
	item.IssueCounts[category]++
	item.SeverityCounts[severity]++
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	item.FileIssueCounts[path]++
	if severity == "critical" || severity == "high" {
		item.FileBlockingCounts[path]++
	}
	if item.FileIssueTypeCounts[path] == nil {
		item.FileIssueTypeCounts[path] = make(map[string]int)
	}
	item.FileIssueTypeCounts[path][category]++
}

func parseQualityMRPath(path, action string) (int64, int64, error) {
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/admin/quality/projects/"), "/")
	if len(parts) != 4 || parts[1] != "mrs" || parts[3] != action {
		return 0, 0, fmt.Errorf("expected /api/v1/admin/quality/projects/{project_id}/mrs/{mr_iid}/%s", action)
	}
	projectID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || projectID <= 0 {
		return 0, 0, fmt.Errorf("invalid project_id")
	}
	mrIID, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || mrIID <= 0 {
		return 0, 0, fmt.Errorf("invalid mr_iid")
	}
	return projectID, mrIID, nil
}

func loadFixTrend(ctx context.Context, st *store.Store, projectID, mrIID int64) ([]fixTrendPoint, error) {
	jobs, err := st.ListAllReviews(ctx)
	if err != nil {
		return nil, err
	}
	selected := make([]store.ReviewJob, 0)
	for _, job := range jobs {
		if job.TargetProjectID == projectID && job.MRIID == mrIID && job.ArtifactDir != "" {
			selected = append(selected, job)
		}
	}
	sort.Slice(selected, func(i, j int) bool {
		if selected[i].QueuedAt.Equal(selected[j].QueuedAt) {
			return selected[i].ID < selected[j].ID
		}
		return selected[i].QueuedAt.Before(selected[j].QueuedAt)
	})
	points := make([]fixTrendPoint, 0, len(selected))
	var previous map[string]int
	fixedTotal := 0
	for _, job := range selected {
		parsed, parseErr := review.ParseResult(filepath.Join(job.ArtifactDir, "ocr-result.json"))
		if parseErr != nil {
			continue
		}
		current := make(map[string]int, len(parsed.Comments))
		for _, comment := range parsed.Comments {
			current[fixTrendFindingKey(comment)]++
		}
		if previous != nil {
			for key, count := range previous {
				if remaining := count - current[key]; remaining > 0 {
					fixedTotal += remaining
				}
			}
		}
		pointTime := job.QueuedAt
		if job.FinishedAt != nil {
			pointTime = *job.FinishedAt
		}
		points = append(points, fixTrendPoint{Time: pointTime.UTC().Format(time.RFC3339), IssueCount: len(parsed.Comments), FixedCount: fixedTotal})
		previous = current
	}
	return points, nil
}

func fixTrendFindingKey(comment review.Comment) string {
	return strings.Join([]string{strings.TrimSpace(comment.Path), strconv.Itoa(comment.StartLine), strings.TrimSpace(strings.ToLower(comment.Category)), strings.TrimSpace(comment.Content)}, "\x00")
}

func loadQualityFiles(ctx context.Context, gl *gitlab.Client, projectID, mrIID int64) ([]qualityFile, error) {
	diffs, err := gl.ListMergeRequestDiffs(ctx, projectID, mrIID)
	if err != nil {
		return nil, err
	}
	mr, _ := gl.GetMergeRequest(ctx, projectID, mrIID)
	fallbackAuthor := qualityAuthorFromUser(mr.Author)
	authorsByPath := make(map[string][]qualityAuthor)
	commits, commitsErr := gl.ListMergeRequestCommits(ctx, projectID, mrIID)
	if commitsErr == nil {
		resolvedAuthors := make(map[string]qualityAuthor)
		for _, commit := range commits {
			commitDiffs, diffErr := gl.ListCommitDiffs(ctx, projectID, commit.ID)
			if diffErr != nil {
				continue
			}
			identity := strings.ToLower(commit.AuthorEmail + "\x00" + commit.AuthorName)
			author, exists := resolvedAuthors[identity]
			if !exists {
				author = resolveCommitAuthor(ctx, gl, commit, mr.Author)
				resolvedAuthors[identity] = author
			}
			for _, commitDiff := range commitDiffs {
				path := commitDiff.NewPath
				if path == "" {
					path = commitDiff.OldPath
				}
				authorsByPath[path] = appendUniqueAuthor(authorsByPath[path], author)
			}
		}
	}
	files := make([]qualityFile, 0, len(diffs))
	for _, diff := range diffs {
		path := diff.NewPath
		if path == "" {
			path = diff.OldPath
		}
		additions, deletions := countDiffLines(diff.Diff)
		authors := authorsByPath[path]
		if len(authors) == 0 && fallbackAuthor.Name != "" {
			authors = []qualityAuthor{fallbackAuthor}
		}
		files = append(files, qualityFile{Path: path, OldPath: diff.OldPath, Additions: additions, Deletions: deletions, Authors: authors})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func loadQualityFileDetail(ctx context.Context, st *store.Store, gl *gitlab.Client, projectID, mrIID int64, path string) (qualityFileDetail, error) {
	mr, err := gl.GetMergeRequest(ctx, projectID, mrIID)
	if err != nil {
		return qualityFileDetail{}, err
	}
	jobs, err := st.ListAllReviews(ctx)
	if err != nil {
		return qualityFileDetail{}, err
	}
	var latest store.ReviewJob
	hasReview := false
	for _, job := range jobs {
		if job.TargetProjectID == projectID && job.MRIID == mrIID {
			latest = job
			hasReview = true
			break
		}
	}

	repositoryProjectID := mr.SourceProjectID
	if repositoryProjectID <= 0 && hasReview {
		repositoryProjectID = latest.SourceProjectID
	}
	if repositoryProjectID <= 0 {
		repositoryProjectID = projectID
	}
	ref := mr.SHA
	if ref == "" && hasReview {
		ref = latest.HeadSHA
	}
	if ref == "" {
		ref = mr.SourceBranch
	}
	content, _, contentErr := gl.GetRepositoryFile(ctx, repositoryProjectID, path, ref)
	if contentErr != nil && hasReview && latest.TargetSHA != "" {
		content, _, contentErr = gl.GetRepositoryFile(ctx, latest.TargetProjectID, path, latest.TargetSHA)
		if contentErr == nil {
			ref = latest.TargetSHA
		}
	}
	if contentErr != nil {
		return qualityFileDetail{}, contentErr
	}
	if len(content) > 2*1024*1024 {
		return qualityFileDetail{}, fmt.Errorf("file is too large to display")
	}
	if !utf8.Valid(content) {
		return qualityFileDetail{}, fmt.Errorf("file is not valid UTF-8 text")
	}

	findings := make([]qualityFileFinding, 0)
	if hasReview {
		storedFindings, storedErr := st.ListFindings(ctx, latest.ID)
		if storedErr != nil {
			return qualityFileDetail{}, storedErr
		}
		for _, finding := range storedFindings {
			if finding.Path == path {
				findings = append(findings, qualityFileFinding{
					Content: finding.Content, SuggestionCode: finding.SuggestionCode, ExistingCode: finding.ExistingCode,
					StartLine: finding.StartLine, EndLine: finding.EndLine, Category: finding.Category,
					Severity: finding.Severity, Status: finding.Status,
				})
			}
		}
		if len(storedFindings) == 0 && latest.ArtifactDir != "" {
			if parsed, parseErr := review.ParseResult(filepath.Join(latest.ArtifactDir, "ocr-result.json")); parseErr == nil {
				for _, finding := range parsed.Comments {
					if finding.Path == path {
						findings = append(findings, qualityFileFinding{
							Content: finding.Content, SuggestionCode: finding.SuggestionCode, ExistingCode: finding.ExistingCode,
							StartLine: finding.StartLine, EndLine: finding.EndLine, Category: finding.Category,
							Severity: finding.Severity, Status: "current",
						})
					}
				}
			}
		}
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].StartLine != findings[j].StartLine {
			return findings[i].StartLine < findings[j].StartLine
		}
		return findings[i].EndLine < findings[j].EndLine
	})
	return qualityFileDetail{Path: path, Ref: ref, Content: string(content), Findings: findings}, nil
}

func resolveCommitAuthor(ctx context.Context, gl *gitlab.Client, commit gitlab.Commit, mrAuthor gitlab.User) qualityAuthor {
	if strings.EqualFold(commit.AuthorName, mrAuthor.Name) ||
		(commit.AuthorEmail != "" && strings.EqualFold(commit.AuthorEmail, mrAuthor.PublicEmail)) {
		return qualityAuthorFromUser(mrAuthor)
	}
	query := commit.AuthorEmail
	if query == "" {
		query = commit.AuthorName
	}
	if query != "" {
		if users, err := gl.SearchUsers(ctx, query); err == nil {
			for _, user := range users {
				if strings.EqualFold(user.PublicEmail, commit.AuthorEmail) || strings.EqualFold(user.Name, commit.AuthorName) || strings.EqualFold(user.Username, commit.AuthorName) {
					return qualityAuthorFromUser(user)
				}
			}
			if len(users) > 0 {
				return qualityAuthorFromUser(users[0])
			}
		}
	}
	return qualityAuthor{Name: commit.AuthorName}
}

func appendUniqueAuthor(authors []qualityAuthor, candidate qualityAuthor) []qualityAuthor {
	if candidate.Name == "" && candidate.Username == "" {
		return authors
	}
	for _, author := range authors {
		if candidate.ID != 0 && author.ID == candidate.ID {
			return authors
		}
		if candidate.ID == 0 && author.ID == 0 && strings.EqualFold(author.Name, candidate.Name) {
			return authors
		}
	}
	return append(authors, candidate)
}

func countDiffLines(diff string) (int, int) {
	var additions, deletions int
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			continue
		case strings.HasPrefix(line, "+"):
			additions++
		case strings.HasPrefix(line, "-"):
			deletions++
		}
	}
	return additions, deletions
}

func qualityAuthorFromUser(user gitlab.User) qualityAuthor {
	name := user.Name
	if name == "" {
		name = user.Username
	}
	return qualityAuthor{ID: user.ID, Name: name, Username: user.Username, AvatarURL: user.AvatarURL, WebURL: user.WebURL}
}
