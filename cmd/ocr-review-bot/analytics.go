package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/gitlab"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/store"
)

const maxAnalyticsRangeDays = 366

type adminAnalyticsReport struct {
	From         string                      `json:"from"`
	To           string                      `json:"to"`
	Groups       []adminAnalyticsGroup       `json:"groups"`
	Summary      adminAnalyticsSummary       `json:"summary"`
	Quality      adminAnalyticsQuality       `json:"quality"`
	Projects     []adminProjectAnalytics     `json:"projects"`
	Contributors []adminContributorAnalytics `json:"contributors"`
}

type adminAnalyticsGroup struct {
	Path         string `json:"path"`
	Name         string `json:"name"`
	ProjectCount int    `json:"project_count"`
}

type adminAnalyticsSummary struct {
	ProjectCount        int `json:"project_count"`
	UpdatedProjects     int `json:"updated_projects"`
	UnavailableProjects int `json:"unavailable_projects"`
	CommitCount         int `json:"commit_count"`
	ContributorCount    int `json:"contributor_count"`
	ReviewCount         int `json:"review_count"`
	PassedReviews       int `json:"passed_reviews"`
	FailedReviews       int `json:"failed_reviews"`
	FindingCount        int `json:"finding_count"`
	BlockingFindings    int `json:"blocking_findings"`
}

type adminAnalyticsQuality struct {
	PassRate       float64        `json:"pass_rate"`
	SeverityCounts map[string]int `json:"severity_counts"`
	CategoryCounts map[string]int `json:"category_counts"`
}

type adminProjectAnalytics struct {
	ProjectID           int64          `json:"project_id"`
	Name                string         `json:"name"`
	PathWithNamespace   string         `json:"path_with_namespace"`
	WebURL              string         `json:"web_url"`
	CommitCount         int            `json:"commit_count"`
	ContributorCount    int            `json:"contributor_count"`
	LatestCommitAt      string         `json:"latest_commit_at,omitempty"`
	ReviewCount         int            `json:"review_count"`
	PassedReviews       int            `json:"passed_reviews"`
	FailedReviews       int            `json:"failed_reviews"`
	PassRate            float64        `json:"pass_rate"`
	FindingCount        int            `json:"finding_count"`
	BlockingFindings    int            `json:"blocking_findings"`
	SeverityCounts      map[string]int `json:"severity_counts"`
	CategoryCounts      map[string]int `json:"category_counts"`
	CommitDataAvailable bool           `json:"commit_data_available"`
	CommitDataError     string         `json:"commit_data_error,omitempty"`
}

type adminContributorAnalytics struct {
	UserID         int64    `json:"user_id,omitempty"`
	Username       string   `json:"username,omitempty"`
	Name           string   `json:"name"`
	Email          string   `json:"email,omitempty"`
	AvatarURL      string   `json:"avatar_url,omitempty"`
	WebURL         string   `json:"web_url,omitempty"`
	IdentitySource string   `json:"identity_source"`
	AddedLines     int      `json:"added_lines"`
	DeletedLines   int      `json:"deleted_lines"`
	ChangedLines   int      `json:"changed_lines"`
	ProjectCount   int      `json:"project_count"`
	Projects       []string `json:"projects"`
	LatestCommitAt string   `json:"latest_commit_at,omitempty"`
}

type projectCommitAnalytics struct {
	commits []gitlab.Commit
	err     error
}

type contributorIdentity struct {
	key      string
	userID   int64
	username string
	name     string
	email    string
	avatar   string
	webURL   string
	source   string
}

type contributorAccumulator struct {
	identity     contributorIdentity
	addedLines   int
	deletedLines int
	changedLines int
	projects     map[string]struct{}
	latest       time.Time
}

func (a *adminAPI) handleAnalytics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if adminRole(r.Context()) != "admin" {
		writeAdminError(w, http.StatusForbidden, "superadmin_required", nil)
		return
	}
	from, to, err := analyticsRange(r)
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_analytics_range", err)
		return
	}
	report, err := loadAdminAnalytics(r.Context(), a.store, a.gl, from, to, r.URL.Query()["group"])
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, "analytics_failed", err)
		return
	}
	writeJSON(w, report, nil)
}

func analyticsRange(r *http.Request) (time.Time, time.Time, error) {
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	from, to := today.AddDate(0, 0, -29), today.AddDate(0, 0, 1)
	if value := strings.TrimSpace(r.URL.Query().Get("from")); value != "" {
		parsed, err := time.Parse("2006-01-02", value)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("from must use YYYY-MM-DD")
		}
		from = parsed.UTC()
	}
	if value := strings.TrimSpace(r.URL.Query().Get("to")); value != "" {
		parsed, err := time.Parse("2006-01-02", value)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("to must use YYYY-MM-DD")
		}
		to = parsed.UTC().AddDate(0, 0, 1)
	}
	if !from.Before(to) {
		return time.Time{}, time.Time{}, errors.New("from must not be after to")
	}
	if to.Sub(from) > maxAnalyticsRangeDays*24*time.Hour {
		return time.Time{}, time.Time{}, fmt.Errorf("range must not exceed %d days", maxAnalyticsRangeDays)
	}
	return from, to, nil
}

func analyticsProjectGroup(project gitlab.Project) string {
	path := strings.Trim(strings.TrimSpace(project.PathWithNamespace), "/")
	if separator := strings.LastIndex(path, "/"); separator > 0 {
		return path[:separator]
	}
	return path
}

func analyticsProjectGroups(projects []gitlab.Project) []adminAnalyticsGroup {
	counts := make(map[string]int)
	for _, project := range projects {
		path := strings.Trim(strings.TrimSpace(project.PathWithNamespace), "/")
		parts := strings.Split(path, "/")
		for index := 1; index < len(parts); index++ {
			counts[strings.Join(parts[:index], "/")]++
		}
	}
	result := make([]adminAnalyticsGroup, 0, len(counts))
	for path, count := range counts {
		name := path
		if separator := strings.LastIndex(path, "/"); separator >= 0 {
			name = path[separator+1:]
		}
		result = append(result, adminAnalyticsGroup{Path: path, Name: name, ProjectCount: count})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result
}

func filterAnalyticsProjects(projects []gitlab.Project, selectedGroups []string) []gitlab.Project {
	if len(selectedGroups) == 0 {
		return projects
	}
	selected := make([]string, 0, len(selectedGroups))
	for _, group := range selectedGroups {
		group = strings.Trim(strings.TrimSpace(group), "/")
		if group != "" {
			selected = append(selected, group)
		}
	}
	if len(selected) == 0 {
		return projects
	}
	result := make([]gitlab.Project, 0, len(projects))
	for _, project := range projects {
		group := analyticsProjectGroup(project)
		for _, selectedGroup := range selected {
			if group == selectedGroup || strings.HasPrefix(group, selectedGroup+"/") {
				result = append(result, project)
				break
			}
		}
	}
	return result
}

func loadAdminAnalytics(ctx context.Context, st *store.Store, gl *gitlab.Client, from, to time.Time, selectedGroups []string) (adminAnalyticsReport, error) {
	allProjects, err := gl.ListProjects(ctx)
	if err != nil {
		return adminAnalyticsReport{}, fmt.Errorf("list analytics projects: %w", err)
	}
	groups := analyticsProjectGroups(allProjects)
	projects := filterAnalyticsProjects(allProjects, selectedGroups)
	qualityRows, err := st.ProjectQualityAnalytics(ctx, from, to)
	if err != nil {
		return adminAnalyticsReport{}, fmt.Errorf("load quality analytics: %w", err)
	}
	qualityByProject := make(map[int64]store.ProjectQualityAnalytics, len(qualityRows))
	for _, row := range qualityRows {
		qualityByProject[row.ProjectID] = row
	}
	commitRows := loadProjectCommitAnalytics(ctx, gl, projects, from, to)
	report := adminAnalyticsReport{
		From:         from.Format("2006-01-02"),
		To:           to.AddDate(0, 0, -1).Format("2006-01-02"),
		Groups:       groups,
		Summary:      adminAnalyticsSummary{ProjectCount: len(projects)},
		Quality:      adminAnalyticsQuality{SeverityCounts: make(map[string]int), CategoryCounts: make(map[string]int)},
		Projects:     make([]adminProjectAnalytics, 0, len(projects)),
		Contributors: make([]adminContributorAnalytics, 0),
	}
	contributors := make(map[string]*contributorAccumulator)
	identities := resolveContributorIdentities(ctx, gl, commitRows)
	for index, project := range projects {
		quality := qualityByProject[project.ID]
		item := adminProjectAnalytics{
			ProjectID: project.ID, Name: project.Name, PathWithNamespace: project.PathWithNamespace, WebURL: project.WebURL,
			ReviewCount: quality.ReviewCount, PassedReviews: quality.PassedReviews, FailedReviews: quality.FailedReviews,
			FindingCount: quality.FindingCount, BlockingFindings: quality.BlockingCount,
			SeverityCounts: copyCountMap(quality.SeverityCounts), CategoryCounts: copyCountMap(quality.CategoryCounts),
			CommitDataAvailable: commitRows[index].err == nil,
		}
		if quality.ReviewCount > 0 {
			item.PassRate = float64(quality.PassedReviews) * 100 / float64(quality.ReviewCount)
		}
		if commitRows[index].err != nil {
			item.CommitDataError = "提交记录暂时不可用"
			report.Summary.UnavailableProjects++
		} else {
			item.CommitCount = len(commitRows[index].commits)
			if item.CommitCount > 0 {
				report.Summary.UpdatedProjects++
			}
			projectContributors := make(map[string]struct{})
			for _, commit := range commitRows[index].commits {
				committedAt, _ := time.Parse(time.RFC3339, commit.CommittedDate)
				if committedAt.After(parseAnalyticsTime(item.LatestCommitAt)) {
					item.LatestCommitAt = committedAt.UTC().Format(time.RFC3339)
				}
				identity := identities[commitIdentityKey(commit)]
				projectContributors[identity.key] = struct{}{}
				contributor := contributors[identity.key]
				if contributor == nil {
					contributor = &contributorAccumulator{identity: identity, projects: make(map[string]struct{})}
					contributors[identity.key] = contributor
				}
				contributor.addedLines += commit.Stats.Additions
				contributor.deletedLines += commit.Stats.Deletions
				changedLines := commit.Stats.Total
				if changedLines == 0 {
					changedLines = commit.Stats.Additions + commit.Stats.Deletions
				}
				contributor.changedLines += changedLines
				contributor.projects[project.PathWithNamespace] = struct{}{}
				if committedAt.After(contributor.latest) {
					contributor.latest = committedAt
				}
			}
			item.ContributorCount = len(projectContributors)
		}
		report.Summary.CommitCount += item.CommitCount
		report.Summary.ReviewCount += item.ReviewCount
		report.Summary.PassedReviews += item.PassedReviews
		report.Summary.FailedReviews += item.FailedReviews
		report.Summary.FindingCount += item.FindingCount
		report.Summary.BlockingFindings += item.BlockingFindings
		mergeCountMaps(report.Quality.SeverityCounts, item.SeverityCounts)
		mergeCountMaps(report.Quality.CategoryCounts, item.CategoryCounts)
		report.Projects = append(report.Projects, item)
	}
	if report.Summary.ReviewCount > 0 {
		report.Quality.PassRate = float64(report.Summary.PassedReviews) * 100 / float64(report.Summary.ReviewCount)
	}
	for _, contributor := range contributors {
		projects := make([]string, 0, len(contributor.projects))
		for project := range contributor.projects {
			projects = append(projects, project)
		}
		sort.Strings(projects)
		item := adminContributorAnalytics{UserID: contributor.identity.userID, Username: contributor.identity.username, Name: contributor.identity.name, Email: contributor.identity.email, AvatarURL: contributor.identity.avatar, WebURL: contributor.identity.webURL, IdentitySource: contributor.identity.source, AddedLines: contributor.addedLines, DeletedLines: contributor.deletedLines, ChangedLines: contributor.changedLines, ProjectCount: len(projects), Projects: projects}
		if !contributor.latest.IsZero() {
			item.LatestCommitAt = contributor.latest.UTC().Format(time.RFC3339)
		}
		report.Contributors = append(report.Contributors, item)
	}
	report.Summary.ContributorCount = len(report.Contributors)
	sort.Slice(report.Contributors, func(i, j int) bool {
		if report.Contributors[i].ChangedLines == report.Contributors[j].ChangedLines {
			return report.Contributors[i].Name < report.Contributors[j].Name
		}
		return report.Contributors[i].ChangedLines > report.Contributors[j].ChangedLines
	})
	sort.Slice(report.Projects, func(i, j int) bool {
		if report.Projects[i].CommitCount == report.Projects[j].CommitCount {
			return report.Projects[i].PathWithNamespace < report.Projects[j].PathWithNamespace
		}
		return report.Projects[i].CommitCount > report.Projects[j].CommitCount
	})
	return report, nil
}

func loadProjectCommitAnalytics(ctx context.Context, gl *gitlab.Client, projects []gitlab.Project, from, to time.Time) []projectCommitAnalytics {
	result := make([]projectCommitAnalytics, len(projects))
	jobs := make(chan int)
	var workers sync.WaitGroup
	workerCount := min(8, len(projects))
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for index := range jobs {
				result[index].commits, result[index].err = gl.ListProjectCommits(ctx, projects[index].ID, from, to.Add(-time.Nanosecond))
			}
		}()
	}
	for index := range projects {
		jobs <- index
	}
	close(jobs)
	workers.Wait()
	return result
}

func resolveContributorIdentities(ctx context.Context, gl *gitlab.Client, rows []projectCommitAnalytics) map[string]contributorIdentity {
	commits := make(map[string]gitlab.Commit)
	for _, row := range rows {
		for _, commit := range row.commits {
			commits[commitIdentityKey(commit)] = commit
		}
	}
	result := make(map[string]contributorIdentity, len(commits))
	var resultMu sync.Mutex
	jobs := make(chan gitlab.Commit)
	var workers sync.WaitGroup
	workerCount := min(8, len(commits))
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for commit := range jobs {
				identity := resolveContributorIdentity(ctx, gl, commit)
				resultMu.Lock()
				result[commitIdentityKey(commit)] = identity
				resultMu.Unlock()
			}
		}()
	}
	for _, commit := range commits {
		jobs <- commit
	}
	close(jobs)
	workers.Wait()
	return result
}

func resolveContributorIdentity(ctx context.Context, gl *gitlab.Client, commit gitlab.Commit) contributorIdentity {
	name := normalizeIdentityText(commit.AuthorName)
	email := normalizeIdentityEmail(commit.AuthorEmail)
	fallback := contributorIdentity{key: contributorFallbackKey(name, email), name: name, email: email, source: "commit"}
	for _, candidateEmail := range uniqueIdentityQueries(email, nameIfEmail(name)) {
		users, err := gl.SearchUsers(ctx, candidateEmail)
		if err != nil {
			continue
		}
		if user, ok := exactEmailUser(users, candidateEmail); ok {
			return contributorIdentityFromUser(user, email)
		}
	}
	for _, username := range uniqueIdentityQueries(name, gitHubNoreplyUsername(email)) {
		if strings.EqualFold(username, "unknown") {
			continue
		}
		users, err := gl.SearchUsers(ctx, username)
		if err != nil {
			continue
		}
		if user, ok := exactUsernameUser(users, username); ok {
			return contributorIdentityFromUser(user, email)
		}
	}
	return fallback
}

func nameIfEmail(name string) string {
	if isEmailAddress(name) {
		return normalizeIdentityEmail(name)
	}
	return ""
}

func isEmailAddress(value string) bool {
	value = normalizeIdentityText(value)
	return strings.Count(value, "@") == 1 && !strings.HasPrefix(value, "@") && !strings.HasSuffix(value, "@")
}

func exactEmailUser(users []gitlab.User, email string) (gitlab.User, bool) {
	var match gitlab.User
	found := 0
	seen := make(map[int64]struct{})
	for _, user := range users {
		if user.ID <= 0 || (normalizeIdentityEmail(user.Email) != email && normalizeIdentityEmail(user.PublicEmail) != email) {
			continue
		}
		if _, exists := seen[user.ID]; exists {
			continue
		}
		seen[user.ID] = struct{}{}
		match, found = user, found+1
	}
	return match, found == 1
}

func exactUsernameUser(users []gitlab.User, username string) (gitlab.User, bool) {
	var match gitlab.User
	found := 0
	seen := make(map[int64]struct{})
	for _, user := range users {
		if user.ID <= 0 || !strings.EqualFold(normalizeIdentityText(user.Username), username) {
			continue
		}
		if _, exists := seen[user.ID]; exists {
			continue
		}
		seen[user.ID] = struct{}{}
		match, found = user, found+1
	}
	return match, found == 1
}

func contributorIdentityFromUser(user gitlab.User, commitEmail string) contributorIdentity {
	resolvedEmail := normalizeIdentityEmail(user.PublicEmail)
	if resolvedEmail == "" {
		resolvedEmail = normalizeIdentityEmail(user.Email)
	}
	if resolvedEmail == "" {
		resolvedEmail = commitEmail
	}
	name := normalizeIdentityText(user.Name)
	if name == "" {
		name = normalizeIdentityText(user.Username)
	}
	return contributorIdentity{key: fmt.Sprintf("user:%d", user.ID), userID: user.ID, username: normalizeIdentityText(user.Username), name: name, email: resolvedEmail, avatar: strings.TrimSpace(user.AvatarURL), webURL: strings.TrimSpace(user.WebURL), source: "gitlab_user"}
}

func normalizeIdentityText(value string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.IsSpace(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, value))
}

func normalizeIdentityEmail(value string) string {
	return strings.ToLower(normalizeIdentityText(value))
}

func emailLocalPart(email string) string {
	if at := strings.IndexByte(email, '@'); at > 0 {
		return email[:at]
	}
	return email
}

func uniqueIdentityQueries(values ...string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = normalizeIdentityText(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

func gitHubNoreplyUsername(email string) string {
	const suffix = "@users.noreply.github.com"
	email = normalizeIdentityEmail(email)
	if !strings.HasSuffix(email, suffix) {
		return ""
	}
	local := strings.TrimSuffix(email, suffix)
	if plus := strings.LastIndex(local, "+"); plus >= 0 {
		local = local[plus+1:]
	}
	return strings.TrimSpace(local)
}

func commitIdentityKey(commit gitlab.Commit) string {
	if email := normalizeIdentityEmail(commit.AuthorEmail); email != "" {
		return "email:" + email
	}
	return "name:" + normalizeIdentityText(commit.AuthorName)
}

func contributorFallbackKey(name, email string) string {
	if email := normalizeIdentityEmail(email); email != "" {
		return "email:" + email
	}
	return "name:" + normalizeIdentityText(name)
}

func parseAnalyticsTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339, value)
	return parsed
}

func copyCountMap(source map[string]int) map[string]int {
	result := make(map[string]int, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func mergeCountMaps(target, source map[string]int) {
	for key, value := range source {
		target[key] += value
	}
}
