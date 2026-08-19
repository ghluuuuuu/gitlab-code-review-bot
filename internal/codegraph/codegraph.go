package codegraph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	ocrmcp "github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/mcp"
)

const (
	ServerName = "code-review-graph"
	buildTool  = "build_or_update_graph_tool"
)

var reviewTools = []string{
	"detect_changes_tool",
	"get_impact_radius_tool",
	"get_review_context_tool",
	"get_minimal_context_tool",
	"get_affected_flows_tool",
	"query_graph_tool",
	"traverse_graph_tool",
	"list_graph_stats_tool",
}

// Options configures one repository graph build and its MCP server.
type Options struct {
	Command string
	RepoDir string
	DataDir string
	Base    string
	Timeout time.Duration
	Version string
}

// Session owns the live MCP client and its repository-scoped server process.
type Session struct {
	Client        *ocrmcp.Client
	stop          context.CancelFunc
	done          <-chan error
	closeOnce     sync.Once
	closeErr      error
	AffectedFiles []string
}

// Close disconnects the MCP client and terminates the server process.
func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		if s.Client != nil {
			s.closeErr = s.Client.Close()
		}
		s.stop()
		select {
		case <-s.done:
		case <-time.After(3 * time.Second):
		}
	})
	return s.closeErr
}

// ReviewTools returns the read-only code intelligence tools exposed to OCR.
func ReviewTools() []string {
	return slices.Clone(reviewTools)
}

// Build starts a repository-scoped MCP server, builds or incrementally updates
// its persistent graph, and returns the live session used by the OCR review.
func Build(ctx context.Context, opts Options) (*Session, error) {
	if opts.Command == "" || opts.RepoDir == "" || opts.DataDir == "" {
		return nil, errors.New("code graph command, repo directory, and data directory are required")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Minute
	}
	repoDir, err := filepath.Abs(opts.RepoDir)
	if err != nil {
		return nil, fmt.Errorf("resolve code graph repository: %w", err)
	}
	dataDir, err := filepath.Abs(opts.DataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve code graph data directory: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create code graph data directory: %w", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("reserve code graph MCP port: %w", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return nil, fmt.Errorf("release code graph MCP port: %w", err)
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("resolve code graph MCP port: %w", err)
	}

	processCtx, stopProcess := context.WithCancel(ctx)
	commandArgs := []string{"serve", "--repo", repoDir, "--http", "--host", "127.0.0.1", "--port", port}
	cmd := exec.CommandContext(processCtx, opts.Command, commandArgs...)
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), "CRG_DATA_DIR="+dataDir, "CRG_REPO_ROOT="+repoDir)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		stopProcess()
		return nil, fmt.Errorf("start code graph MCP server: %w", err)
	}
	processDone := make(chan error, 1)
	go func() { processDone <- cmd.Wait() }()
	session := &Session{stop: stopProcess, done: processDone}
	fail := func(err error) (*Session, error) {
		return nil, errors.Join(err, session.Close())
	}

	graphCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	if err := waitForServer(graphCtx, address, processDone); err != nil {
		return fail(err)
	}
	endpoint := "http://" + address + "/mcp"
	client, err := ocrmcp.NewRemoteClient(graphCtx, ServerName, endpoint, nil, opts.Version)
	if err != nil {
		return fail(err)
	}
	session.Client = client
	_, statErr := os.Stat(filepath.Join(dataDir, "graph.db"))
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return fail(fmt.Errorf("inspect code graph database: %w", statErr))
	}
	fullRebuild := errors.Is(statErr, os.ErrNotExist)
	_, err = client.CallTool(graphCtx, buildTool, map[string]any{
		"full_rebuild": fullRebuild,
		"repo_root":    repoDir,
		"base":         opts.Base,
		"postprocess":  "full",
	})
	if err != nil {
		return fail(fmt.Errorf("build or update code graph: %w", err))
	}
	impactRaw, impactErr := client.CallTool(graphCtx, "get_impact_radius_tool", map[string]any{
		"repo_root": repoDir, "base": opts.Base, "max_depth": 2,
	})
	if impactErr != nil {
		return fail(fmt.Errorf("query code graph impact radius: %w", impactErr))
	}
	session.AffectedFiles = normalizeAffectedFiles(repoDir, impactRaw)
	slog.Info("code review graph ready", "repo", repoDir, "data_dir", dataDir, "base", opts.Base, "affected_files", len(session.AffectedFiles))
	return session, nil
}

func waitForServer(ctx context.Context, address string, processDone <-chan error) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for code graph MCP server: %w", context.Cause(ctx))
		case err := <-processDone:
			return fmt.Errorf("code graph MCP server exited before readiness: %w", err)
		case <-ticker.C:
		}
	}
}

func normalizeAffectedFiles(repoDir, raw string) []string {
	var payload struct {
		ImpactedFiles []string `json:"impacted_files"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil
	}
	root, err := filepath.Abs(repoDir)
	if err != nil {
		return nil
	}
	seen := make(map[string]struct{}, len(payload.ImpactedFiles))
	files := make([]string, 0, len(payload.ImpactedFiles))
	for _, file := range payload.ImpactedFiles {
		if filepath.IsAbs(file) {
			if relative, relErr := filepath.Rel(root, file); relErr == nil {
				file = relative
			}
		}
		file = filepath.ToSlash(filepath.Clean(file))
		if file == "." || file == "" || strings.HasPrefix(file, "../") {
			continue
		}
		if _, exists := seen[file]; exists {
			continue
		}
		seen[file] = struct{}{}
		files = append(files, file)
	}
	return files
}

// ProjectLocks serializes graph mutation and MCP reads for the same project.
// Different projects remain independent.
type ProjectLocks struct {
	locks sync.Map
}

// Acquire waits for exclusive access to one project until ctx is cancelled.
func (l *ProjectLocks) Acquire(ctx context.Context, projectID int64) (func(), error) {
	candidate := make(chan struct{}, 1)
	candidate <- struct{}{}
	value, _ := l.locks.LoadOrStore(strconv.FormatInt(projectID, 10), candidate)
	semaphore := value.(chan struct{})
	select {
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	case <-semaphore:
	}
	var once sync.Once
	return func() {
		once.Do(func() { semaphore <- struct{}{} })
	}, nil
}
