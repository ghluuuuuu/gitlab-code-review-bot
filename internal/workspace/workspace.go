package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/gitlab"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/store"
)

type Manager struct {
	DataDir string
	GitLab  *gitlab.Client
}

type Prepared struct {
	RepoDir     string
	ArtifactDir string
	TargetRef   string
	TargetSHA   string
	BaseSHA     string
}

func (m *Manager) Prepare(ctx context.Context, job *store.ReviewJob) (Prepared, error) {
	if m.GitLab == nil {
		return Prepared{}, errors.New("gitlab client is required")
	}
	targetProject, err := m.GitLab.GetProject(ctx, job.TargetProjectID)
	if err != nil {
		return Prepared{}, err
	}
	sourceProject := targetProject
	if job.SourceProjectID != job.TargetProjectID {
		sourceProject, err = m.GitLab.GetProject(ctx, job.SourceProjectID)
		if err != nil {
			return Prepared{}, err
		}
	}
	absDataDir, err := filepath.Abs(m.DataDir)
	if err != nil {
		return Prepared{}, err
	}
	cacheRoot := filepath.ToSlash(filepath.Join(absDataDir, "git-cache"))
	workspaceDir := filepath.Join(absDataDir, "workspaces", workloadWorkspaceName(targetProject))
	artifactDir := filepath.Join(absDataDir, "artifacts", strconv.FormatInt(job.ID, 10))
	if err := os.RemoveAll(workspaceDir); err != nil {
		return Prepared{}, err
	}
	if err := os.MkdirAll(cacheRoot, 0o755); err != nil {
		return Prepared{}, err
	}
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return Prepared{}, err
	}
	targetMirror := filepath.ToSlash(filepath.Join(cacheRoot, fmt.Sprintf("project-%d.git", job.TargetProjectID)))
	if err := m.ensureMirror(ctx, targetMirror, m.GitLab.AuthenticatedURL(targetProject.HTTPURLToRepository), job.TargetBranch); err != nil {
		return Prepared{}, err
	}
	sourceMirror := targetMirror
	if sourceProject.ID != targetProject.ID {
		sourceMirror = filepath.ToSlash(filepath.Join(cacheRoot, fmt.Sprintf("project-%d.git", sourceProject.ID)))
		if err := m.ensureMirror(ctx, sourceMirror, m.GitLab.AuthenticatedURL(sourceProject.HTTPURLToRepository), job.SourceBranch); err != nil {
			return Prepared{}, err
		}
	} else if job.SourceBranch != job.TargetBranch {
		if err := m.fetchBranch(ctx, sourceMirror, job.SourceBranch); err != nil {
			return Prepared{}, err
		}
	}
	if err := run(ctx, "", "git", "clone", "--shared", "--no-checkout", sourceMirror, workspaceDir); err != nil {
		return Prepared{}, err
	}
	targetRef := "refs/remotes/target/" + job.TargetBranch
	if err := run(ctx, workspaceDir, "git", "fetch", targetMirror, "+refs/remotes/origin/"+job.TargetBranch+":"+targetRef); err != nil {
		return Prepared{}, err
	}
	if err := run(ctx, workspaceDir, "git", "checkout", "--detach", job.HeadSHA); err != nil {
		return Prepared{}, err
	}
	targetSHA, err := output(ctx, workspaceDir, "git", "rev-parse", targetRef)
	if err != nil {
		return Prepared{}, err
	}
	baseSHA, err := output(ctx, workspaceDir, "git", "merge-base", targetRef, job.HeadSHA)
	if err != nil {
		return Prepared{}, err
	}
	return Prepared{RepoDir: workspaceDir, ArtifactDir: artifactDir, TargetRef: targetRef, TargetSHA: targetSHA, BaseSHA: baseSHA}, nil
}

func WorkloadName(project gitlab.Project) string {
	return "workload-" + strconv.FormatInt(project.ID, 10) + "-" + sanitizeWorkspaceName(project.Name)
}

func workloadWorkspaceName(project gitlab.Project) string {
	return WorkloadName(project)
}

func sanitizeWorkspaceName(name string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || strings.ContainsRune(`< > : " / \\ | ? *`, r) {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimRight(b.String(), " .")
}

func (m *Manager) Cleanup(prepared Prepared) error {
	return os.RemoveAll(prepared.RepoDir)
}

func (m *Manager) ensureMirror(ctx context.Context, path, remoteURL, branch string) error {
	if _, err := os.Stat(filepath.Join(path, "HEAD")); errors.Is(err, os.ErrNotExist) {
		if err := run(ctx, "", "git", "clone", "--mirror", remoteURL, path); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	return m.fetchBranch(ctx, path, branch)
}

func (m *Manager) fetchBranch(ctx context.Context, mirror, branch string) error {
	return run(ctx, "", "git", "--git-dir="+mirror, "fetch", "--no-tags", "origin", "+refs/heads/"+branch+":refs/remotes/origin/"+branch)
}

func run(ctx context.Context, dir, name string, args ...string) error {
	safeArgs := redactArgs(args)
	slog.Info("command starting", "command", name, "args", strings.Join(safeArgs, " "), "dir", dir)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var captured bytes.Buffer
	cmd.Stdout = io.MultiWriter(os.Stdout, &captured)
	cmd.Stderr = io.MultiWriter(os.Stderr, &captured)
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(safeArgs, " "), err, captured.String())
	}
	slog.Info("command finished", "command", name, "dir", dir)
	return nil
}

func output(ctx context.Context, dir, name string, args ...string) (string, error) {
	safeArgs := redactArgs(args)
	slog.Info("command starting", "command", name, "args", strings.Join(safeArgs, " "), "dir", dir)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	data, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", name, strings.Join(safeArgs, " "), err)
	}
	value := strings.TrimSpace(string(data))
	slog.Info("command finished", "command", name, "dir", dir, "output", value)
	return value, nil
}

func redactArgs(args []string) []string {
	redacted := append([]string(nil), args...)
	for i, arg := range redacted {
		start := strings.Index(arg, "://oauth2:")
		if start < 0 {
			continue
		}
		tokenStart := start + len("://oauth2:")
		if relativeEnd := strings.Index(arg[tokenStart:], "@"); relativeEnd >= 0 {
			redacted[i] = arg[:tokenStart] + "***" + arg[tokenStart+relativeEnd:]
		}
	}
	return redacted
}
