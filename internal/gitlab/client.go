package gitlab

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

type MergeRequest struct {
	ProjectID       int64  `json:"project_id"`
	IID             int64  `json:"iid"`
	Title           string `json:"title"`
	Description     string `json:"description"`
	State           string `json:"state"`
	Draft           bool   `json:"draft"`
	SHA             string `json:"sha"`
	SourceProjectID int64  `json:"source_project_id"`
	TargetProjectID int64  `json:"target_project_id"`
	SourceBranch    string `json:"source_branch"`
	TargetBranch    string `json:"target_branch"`
	WebURL          string `json:"web_url"`
	Reviewers       []User `json:"reviewers"`
	Author          User   `json:"author"`
	UpdatedAt       string `json:"updated_at"`
	References      struct {
		Full string `json:"full"`
	} `json:"references"`
}

type User struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	Name        string `json:"name"`
	AvatarURL   string `json:"avatar_url"`
	WebURL      string `json:"web_url"`
	PublicEmail string `json:"public_email"`
}

type Project struct {
	ID                  int64  `json:"id"`
	Name                string `json:"name"`
	Description         string `json:"description"`
	PathWithNamespace   string `json:"path_with_namespace"`
	HTTPURLToRepository string `json:"http_url_to_repo"`
	WebURL              string `json:"web_url"`
}

type RepositoryFile struct {
	FilePath string `json:"file_path"`
	BlobID   string `json:"blob_id"`
	CommitID string `json:"commit_id"`
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

type DiffVersion struct {
	ID             int64  `json:"id"`
	HeadCommitSHA  string `json:"head_commit_sha"`
	BaseCommitSHA  string `json:"base_commit_sha"`
	StartCommitSHA string `json:"start_commit_sha"`
}

type CommitStatus struct {
	Status string `json:"status"`
	Name   string `json:"name"`
}

type Branch struct {
	Name   string `json:"name"`
	Commit struct {
		ID string `json:"id"`
	} `json:"commit"`
}

type CommitRef struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type CommitSequence struct {
	Count int64 `json:"count"`
}

func (c *Client) ListProjects(ctx context.Context) ([]Project, error) {
	var result []Project
	for page := 1; ; page++ {
		var batch []Project
		endpoint := fmt.Sprintf("/api/v4/projects?simple=true&order_by=path&sort=asc&per_page=100&page=%d", page)
		resp, err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &batch)
		if err != nil {
			return nil, err
		}
		result = append(result, batch...)
		if resp.Header.Get("X-Next-Page") == "" {
			return result, nil
		}
	}
}

func (c *Client) ListBranches(ctx context.Context, projectID int64) ([]Branch, error) {
	var result []Branch
	for page := 1; ; page++ {
		var batch []Branch
		endpoint := fmt.Sprintf("/api/v4/projects/%d/repository/branches?per_page=100&page=%d", projectID, page)
		resp, err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &batch)
		if err != nil {
			return nil, err
		}
		result = append(result, batch...)
		if resp.Header.Get("X-Next-Page") == "" {
			return result, nil
		}
	}
}

func (c *Client) ListCommitBranchRefs(ctx context.Context, projectID int64, commitSHA string) ([]CommitRef, error) {
	var result []CommitRef
	for page := 1; ; page++ {
		var batch []CommitRef
		endpoint := fmt.Sprintf("/api/v4/projects/%d/repository/commits/%s/refs?type=branch&per_page=100&page=%d", projectID, url.PathEscape(commitSHA), page)
		resp, err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &batch)
		if err != nil {
			return nil, err
		}
		result = append(result, batch...)
		if resp.Header.Get("X-Next-Page") == "" {
			return result, nil
		}
	}
}

func (c *Client) GetCommitSequence(ctx context.Context, projectID int64, commitSHA string) (int64, error) {
	var sequence CommitSequence
	endpoint := fmt.Sprintf("/api/v4/projects/%d/repository/commits/%s/sequence", projectID, url.PathEscape(commitSHA))
	_, err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &sequence)
	return sequence.Count, err
}

func (c *Client) ListProjectMergeRequests(ctx context.Context, projectID int64) ([]MergeRequest, error) {
	var result []MergeRequest
	for page := 1; ; page++ {
		var batch []MergeRequest
		endpoint := fmt.Sprintf("/api/v4/projects/%d/merge_requests?state=all&scope=all&order_by=updated_at&sort=desc&per_page=100&page=%d", projectID, page)
		resp, err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &batch)
		if err != nil {
			return nil, err
		}
		result = append(result, batch...)
		if resp.Header.Get("X-Next-Page") == "" {
			return result, nil
		}
	}
}

type Note struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
}

type MergeRequestDiff struct {
	OldPath     string `json:"old_path"`
	NewPath     string `json:"new_path"`
	Diff        string `json:"diff"`
	NewFile     bool   `json:"new_file"`
	RenamedFile bool   `json:"renamed_file"`
	DeletedFile bool   `json:"deleted_file"`
}

type Commit struct {
	ID          string `json:"id"`
	AuthorName  string `json:"author_name"`
	AuthorEmail string `json:"author_email"`
}

type Discussion struct {
	ID    string           `json:"id"`
	Notes []DiscussionNote `json:"notes"`
}

type DiscussionNote struct {
	ID         int64               `json:"id"`
	Body       string              `json:"body"`
	Resolvable bool                `json:"resolvable"`
	Resolved   bool                `json:"resolved"`
	Position   *DiscussionPosition `json:"position"`
}

type DiscussionPosition struct {
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
}

type HTTPError struct {
	Method     string
	Endpoint   string
	StatusCode int
	Status     string
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("gitlab %s %s: %s: %s", e.Method, e.Endpoint, e.Status, e.Body)
}

func IsNotFound(err error) bool {
	var target *HTTPError
	return errors.As(err, &target) && target.StatusCode == http.StatusNotFound
}

// IsInvalidDiffPosition reports a GitLab 400 from a diff discussion.
// GitLab sometimes returns a detailed line_code validation error and sometimes
// only returns a generic "Bad Request" when it cannot derive line_code.
func IsInvalidDiffPosition(err error) bool {
	var target *HTTPError
	return errors.As(err, &target) && target.StatusCode == http.StatusBadRequest
}

func New(baseURL, token string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// GetCurrentUser returns the GitLab identity authenticated by the configured token.
func (c *Client) GetCurrentUser(ctx context.Context) (User, error) {
	var user User
	_, err := c.doJSON(ctx, http.MethodGet, "/api/v4/user", nil, &user)
	if err != nil {
		return User{}, err
	}
	if user.ID == 0 {
		return User{}, errors.New("GitLab current user response is missing id")
	}
	return user, nil
}

func (c *Client) ListReviewsForMe(ctx context.Context, botID int64) ([]MergeRequest, error) {
	var result []MergeRequest
	for page := 1; ; page++ {
		values := url.Values{}
		values.Set("scope", "all")
		values.Set("state", "opened")
		values.Set("reviewer_id", strconv.FormatInt(botID, 10))
		values.Set("order_by", "updated_at")
		values.Set("sort", "asc")
		values.Set("per_page", "100")
		values.Set("page", strconv.Itoa(page))
		var batch []MergeRequest
		resp, err := c.doJSON(ctx, http.MethodGet, "/api/v4/merge_requests?"+values.Encode(), nil, &batch)
		if err != nil {
			return nil, err
		}
		result = append(result, batch...)
		if resp.Header.Get("X-Next-Page") == "" {
			break
		}
	}
	return result, nil
}

func (c *Client) GetMergeRequest(ctx context.Context, projectID, mrIID int64) (MergeRequest, error) {
	var mr MergeRequest
	_, err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/api/v4/projects/%d/merge_requests/%d", projectID, mrIID), nil, &mr)
	return mr, err
}

func (c *Client) GetProject(ctx context.Context, projectID int64) (Project, error) {
	var project Project
	_, err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/api/v4/projects/%d", projectID), nil, &project)
	return project, err
}

func (c *Client) GetBranch(ctx context.Context, projectID int64, branch string) (Branch, error) {
	var result Branch
	_, err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/api/v4/projects/%d/repository/branches/%s", projectID, url.PathEscape(branch)), nil, &result)
	return result, err
}

func (c *Client) GetRepositoryFile(ctx context.Context, projectID int64, path, ref string) ([]byte, RepositoryFile, error) {
	var file RepositoryFile
	endpoint := fmt.Sprintf("/api/v4/projects/%d/repository/files/%s?ref=%s", projectID, url.PathEscape(path), url.QueryEscape(ref))
	_, err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &file)
	if err != nil {
		return nil, file, err
	}
	if file.Encoding != "base64" {
		return nil, file, fmt.Errorf("unsupported repository file encoding %q", file.Encoding)
	}
	content, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
	return content, file, err
}

func (c *Client) GetLatestDiffVersion(ctx context.Context, projectID, mrIID int64) (DiffVersion, error) {
	versions, err := c.ListDiffVersions(ctx, projectID, mrIID)
	if err != nil {
		return DiffVersion{}, err
	}
	if len(versions) == 0 {
		return DiffVersion{}, errors.New("merge request has no diff versions")
	}
	return versions[0], nil
}

func (c *Client) GetDiffVersionForHead(ctx context.Context, projectID, mrIID int64, headSHA string) (DiffVersion, error) {
	versions, err := c.ListDiffVersions(ctx, projectID, mrIID)
	if err != nil {
		return DiffVersion{}, err
	}
	for _, version := range versions {
		if version.HeadCommitSHA == headSHA {
			return version, nil
		}
	}
	return DiffVersion{}, fmt.Errorf("merge request has no diff version for head %s", headSHA)
}

func (c *Client) ListDiffVersions(ctx context.Context, projectID, mrIID int64) ([]DiffVersion, error) {
	var versions []DiffVersion
	_, err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/api/v4/projects/%d/merge_requests/%d/versions?per_page=100", projectID, mrIID), nil, &versions)
	return versions, err

}

func (c *Client) CreateNote(ctx context.Context, projectID, mrIID int64, body string) error {
	form := url.Values{"body": {body}}
	_, err := c.doForm(ctx, http.MethodPost, fmt.Sprintf("/api/v4/projects/%d/merge_requests/%d/notes", projectID, mrIID), form, nil)
	return err
}

func (c *Client) ListNotes(ctx context.Context, projectID, mrIID int64) ([]Note, error) {
	var notes []Note
	_, err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/api/v4/projects/%d/merge_requests/%d/notes?per_page=100", projectID, mrIID), nil, &notes)
	return notes, err
}

func (c *Client) UpdateNote(ctx context.Context, projectID, mrIID, noteID int64, body string) error {
	form := url.Values{"body": {body}}
	_, err := c.doForm(ctx, http.MethodPut, fmt.Sprintf("/api/v4/projects/%d/merge_requests/%d/notes/%d", projectID, mrIID, noteID), form, nil)
	return err
}

func (c *Client) DeleteNote(ctx context.Context, projectID, mrIID, noteID int64) error {
	_, err := c.doJSON(ctx, http.MethodDelete, fmt.Sprintf("/api/v4/projects/%d/merge_requests/%d/notes/%d", projectID, mrIID, noteID), nil, nil)
	return err
}

func (c *Client) DeleteNotesWithMarkers(ctx context.Context, projectID, mrIID int64, markers ...string) error {
	notes, err := c.ListNotes(ctx, projectID, mrIID)
	if err != nil {
		return err
	}
	var deleteErr error
	for _, note := range notes {
		for _, marker := range markers {
			if strings.Contains(note.Body, marker) {
				deleteErr = errors.Join(deleteErr, c.DeleteNote(ctx, projectID, mrIID, note.ID))
				break
			}
		}
	}
	return deleteErr
}

func (c *Client) UpsertSummaryNote(ctx context.Context, projectID, mrIID int64, marker, body string) error {
	notes, err := c.ListNotes(ctx, projectID, mrIID)
	if err != nil {
		return err
	}
	fullBody := marker + "\n" + body
	for _, note := range notes {
		if strings.Contains(note.Body, marker) {
			return c.UpdateNote(ctx, projectID, mrIID, note.ID, fullBody)
		}
	}
	return c.CreateNote(ctx, projectID, mrIID, fullBody)
}
func (c *Client) NoteExists(ctx context.Context, projectID, mrIID int64, marker string) (bool, error) {
	notes, err := c.ListNotes(ctx, projectID, mrIID)
	if err != nil {
		return false, err
	}
	for _, note := range notes {
		if strings.Contains(note.Body, marker) {
			return true, nil
		}
	}
	return false, nil
}

func (c *Client) CreateDiffDiscussion(ctx context.Context, projectID, mrIID int64, version DiffVersion, path string, newLine int, body string) error {
	form := url.Values{}
	form.Set("position[position_type]", "text")
	form.Set("position[base_sha]", version.BaseCommitSHA)
	form.Set("position[start_sha]", version.StartCommitSHA)
	form.Set("position[head_sha]", version.HeadCommitSHA)
	form.Set("position[old_path]", path)
	form.Set("position[new_path]", path)
	form.Set("position[new_line]", strconv.Itoa(newLine))
	form.Set("body", body)
	endpoint := fmt.Sprintf("/api/v4/projects/%d/merge_requests/%d/discussions", projectID, mrIID)
	_, err := c.doForm(ctx, http.MethodPost, endpoint, form, nil)
	if err != nil {
		var httpErr *HTTPError
		if errors.As(err, &httpErr) {
			slog.Error("gitlab diff discussion rejected", "project_id", projectID, "mr_iid", mrIID, "path", path, "new_line", newLine, "position_type", "text", "old_path", path, "new_path", path, "base_sha", version.BaseCommitSHA, "start_sha", version.StartCommitSHA, "head_sha", version.HeadCommitSHA, "response_body", httpErr.Body, "error", err)
		} else {
			slog.Error("gitlab diff discussion request failed", "project_id", projectID, "mr_iid", mrIID, "path", path, "new_line", newLine, "position_type", "text", "old_path", path, "new_path", path, "base_sha", version.BaseCommitSHA, "start_sha", version.StartCommitSHA, "head_sha", version.HeadCommitSHA, "error", err)
		}
	}
	return err
}

func (c *Client) ListMergeRequestDiffs(ctx context.Context, projectID, mrIID int64) ([]MergeRequestDiff, error) {
	var result []MergeRequestDiff
	for page := 1; ; page++ {
		var batch []MergeRequestDiff
		endpoint := fmt.Sprintf("/api/v4/projects/%d/merge_requests/%d/diffs?per_page=100&page=%d", projectID, mrIID, page)
		resp, err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &batch)
		if err != nil {
			return nil, err
		}
		result = append(result, batch...)
		if resp.Header.Get("X-Next-Page") == "" {
			return result, nil
		}
	}
}

func (c *Client) ListMergeRequestCommits(ctx context.Context, projectID, mrIID int64) ([]Commit, error) {
	var result []Commit
	for page := 1; ; page++ {
		var batch []Commit
		endpoint := fmt.Sprintf("/api/v4/projects/%d/merge_requests/%d/commits?per_page=100&page=%d", projectID, mrIID, page)
		resp, err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &batch)
		if err != nil {
			return nil, err
		}
		result = append(result, batch...)
		if resp.Header.Get("X-Next-Page") == "" {
			return result, nil
		}
	}
}

func (c *Client) ListCommitDiffs(ctx context.Context, projectID int64, commitSHA string) ([]MergeRequestDiff, error) {
	var result []MergeRequestDiff
	for page := 1; ; page++ {
		var batch []MergeRequestDiff
		endpoint := fmt.Sprintf("/api/v4/projects/%d/repository/commits/%s/diff?per_page=100&page=%d", projectID, url.PathEscape(commitSHA), page)
		resp, err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &batch)
		if err != nil {
			return nil, err
		}
		result = append(result, batch...)
		if resp.Header.Get("X-Next-Page") == "" {
			return result, nil
		}
	}
}

func (c *Client) SearchUsers(ctx context.Context, query string) ([]User, error) {
	var users []User
	endpoint := "/api/v4/users?per_page=20&search=" + url.QueryEscape(query)
	_, err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &users)
	return users, err
}

func (c *Client) ListDiscussions(ctx context.Context, projectID, mrIID int64) ([]Discussion, error) {
	var result []Discussion
	for page := 1; ; page++ {
		var batch []Discussion
		endpoint := fmt.Sprintf("/api/v4/projects/%d/merge_requests/%d/discussions?per_page=100&page=%d", projectID, mrIID, page)
		resp, err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &batch)
		if err != nil {
			return nil, err
		}
		result = append(result, batch...)
		if resp.Header.Get("X-Next-Page") == "" {
			return result, nil
		}
	}
}

func (c *Client) ResolveDiscussion(ctx context.Context, projectID, mrIID int64, discussionID string) error {
	form := url.Values{"resolved": {"true"}}
	endpoint := fmt.Sprintf("/api/v4/projects/%d/merge_requests/%d/discussions/%s", projectID, mrIID, url.PathEscape(discussionID))
	_, err := c.doForm(ctx, http.MethodPut, endpoint, form, nil)
	return err
}
func (c *Client) AuthenticatedURL(repoURL string) string {
	if c.token == "" || repoURL == "" {
		return repoURL
	}
	scheme := ""
	if strings.HasPrefix(repoURL, "https://") {
		scheme = "https://"
		repoURL = strings.TrimPrefix(repoURL, scheme)
	} else if strings.HasPrefix(repoURL, "http://") {
		scheme = "http://"
		repoURL = strings.TrimPrefix(repoURL, scheme)
	} else {
		return repoURL
	}
	return scheme + "oauth2:" + c.token + "@" + repoURL
}

func (c *Client) doJSON(ctx context.Context, method, endpoint string, body io.Reader, destination any) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		httpErr := &HTTPError{Method: method, Endpoint: endpoint, StatusCode: resp.StatusCode, Status: resp.Status, Body: strings.TrimSpace(string(data))}
		slog.Error("gitlab API request failed", "method", method, "endpoint", endpoint, "status_code", resp.StatusCode, "status", resp.Status, "response_body", httpErr.Body)
		return resp, httpErr
	}
	if destination != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(destination); err != nil {
			return resp, err
		}
	}
	return resp, nil
}

func (c *Client) doForm(ctx context.Context, method, endpoint string, form url.Values, destination any) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		httpErr := &HTTPError{Method: method, Endpoint: endpoint, StatusCode: resp.StatusCode, Status: resp.Status, Body: strings.TrimSpace(string(data))}
		slog.Error("gitlab API request failed", "method", method, "endpoint", endpoint, "status_code", resp.StatusCode, "status", resp.Status, "response_body", httpErr.Body)
		return resp, httpErr
	}
	if destination != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(destination); err != nil {
			return resp, err
		}
	}
	return resp, nil
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}
