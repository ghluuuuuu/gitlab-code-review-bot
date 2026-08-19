package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/codegraph"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/config"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/gitlab"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/viewer"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/publisher"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/review"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/store"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/worker"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/workspace"
	webassets "github.com/ghluuuuuu/gitlab-code-review-bot/web"
)

func main() {
	configPath := flag.String("config", "", "path to config.json (default: env OCR_BOT_CONFIG)")
	showHelp := flag.Bool("help", false, "show usage")
	flag.Parse()
	if *showHelp {
		flag.Usage()
		os.Exit(0)
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("load config", "error", err)
		os.Exit(1)
	}
	gl := gitlab.New(cfg.GitLab.BaseURL, cfg.GitLab.Token, cfg.GitLab.HTTPTimeout)
	botUser, err := gl.GetCurrentUser(context.Background())
	if err != nil {
		slog.Error("resolve authenticated GitLab user", "error", err)
		os.Exit(1)
	}
	slog.Info("authenticated GitLab bot resolved", "user_id", botUser.ID, "username", botUser.Username)
	st, err := store.Open(cfg.DatabasePath)
	if err != nil {
		slog.Error("open store", "error", err)
		os.Exit(1)
	}
	defer st.Close()
	recovered, err := st.RecoverInterrupted(context.Background())
	if err != nil {
		slog.Error("recover interrupted jobs", "error", err)
		os.Exit(1)
	}
	if recovered > 0 {
		slog.Info("recovered interrupted review jobs", "count", recovered)
	}
	slog.Info("embedded OpenCodeReview component initialized", "version", "v1.9.2", "language", cfg.LLM.Language)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	viewerTarget := startOCRViewer(ctx, cfg)
	reportBase := cfg.Review.ViewerURL
	if viewerTarget == "" {
		reportBase = ""
	} else if reportBase == "" {
		reportBase = "http://localhost:" + portFromAddr(cfg.Server.Addr)
	}
	cfg.Review.ViewerURL = reportBase
	workspaceManager := &workspace.Manager{DataDir: cfg.DataDir, GitLab: gl}
	resultPublisher := &publisher.Publisher{GitLab: gl, ViewerURL: reportBase}
	projectLocks := &codegraph.ProjectLocks{}
	for i := range cfg.Review.Concurrency {
		instance := &worker.Worker{ID: "worker-" + strconv.Itoa(i+1), Config: cfg, BotUserID: botUser.ID, Store: st, GitLab: gl, Workspace: workspaceManager, Publisher: resultPublisher, ProjectLocks: projectLocks}
		go instance.Run(ctx)
	}
	go runDiscovery(ctx, gl, st, cfg, botUser.ID)
	server := &http.Server{Addr: cfg.Server.Addr, Handler: routes(st, gl, cfg, viewerTarget), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	slog.Info("ocr-review-bot started", "addr", cfg.Server.Addr, "workers", cfg.Review.Concurrency, "file_concurrency", cfg.Review.FileConcurrency, "code_graph", cfg.CodeGraph.Enabled)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server", "error", err)
		os.Exit(1)
	}
}

func startOCRViewer(ctx context.Context, cfg config.Config) string {
	viewerAddr := "127.0.0.1:5483"
	viewerURL := "http://" + viewerAddr
	errCh := make(chan error, 1)
	go func() { errCh <- viewer.StartServer(viewerAddr) }()
	readyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-errCh:
			slog.Warn("embedded OCR viewer exited before becoming ready", "error", err)
			return ""
		case <-readyCtx.Done():
			slog.Warn("embedded OCR viewer readiness timeout", "error", readyCtx.Err())
			return ""
		case <-ticker.C:
			request, _ := http.NewRequestWithContext(readyCtx, http.MethodGet, viewerURL+"/", nil)
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				continue
			}
			_ = response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 500 {
				slog.Info("embedded OCR viewer started", "addr", viewerAddr)
				return viewerURL
			}
		}
	}
}

func portFromAddr(addr string) string {
	if _, port, err := net.SplitHostPort(addr); err == nil {
		return port
	}
	return "8080"
}

func runDiscovery(ctx context.Context, gl *gitlab.Client, st *store.Store, cfg config.Config, botUserID int64) {
	discoverReviews(ctx, gl, st, cfg, botUserID)
	ticker := time.NewTicker(time.Duration(cfg.GitLab.PollSeconds) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			discoverReviews(ctx, gl, st, cfg, botUserID)
		}
	}
}

func discoverReviews(ctx context.Context, gl *gitlab.Client, st *store.Store, cfg config.Config, botUserID int64) {
	usage, usageErr := st.Dashboard(ctx)
	if usageErr != nil {
		slog.Error("read token budget usage", "error", usageErr)
		return
	}
	if cfg.Review.DailyTokenBudget > 0 && usage.TodayTokens >= cfg.Review.DailyTokenBudget {
		slog.Warn("daily token budget reached; discovery enqueue paused", "used", usage.TodayTokens, "budget", cfg.Review.DailyTokenBudget)
		return
	}
	if cfg.Review.MonthlyTokenBudget > 0 && usage.MonthTokens >= cfg.Review.MonthlyTokenBudget {
		slog.Warn("monthly token budget reached; discovery enqueue paused", "used", usage.MonthTokens, "budget", cfg.Review.MonthlyTokenBudget)
		return
	}
	mrs, err := gl.ListReviewsForMe(ctx, botUserID)
	if err != nil {
		slog.Error("discover merge requests", "error", err)
		return
	}
	for _, mr := range mrs {
		if mr.Draft || mr.State != "opened" || mr.SHA == "" {
			continue
		}
		targetBranch, err := gl.GetBranch(ctx, mr.TargetProjectID, mr.TargetBranch)
		if err != nil {
			slog.Error("read merge request target branch", "project", mr.ProjectID, "mr", mr.IID, "target_branch", mr.TargetBranch, "error", err)
			continue
		}
		enqueued, err := st.Enqueue(ctx, store.EnqueueInput{
			ProjectID: mr.ProjectID, MRIID: mr.IID, Title: mr.Title, WebURL: mr.WebURL,
			SourceProjectID: mr.SourceProjectID, SourceBranch: mr.SourceBranch,
			TargetProjectID: mr.TargetProjectID, TargetBranch: mr.TargetBranch, HeadSHA: mr.SHA, TargetSHA: targetBranch.Commit.ID,
		})
		if err != nil {
			slog.Error("enqueue merge request", "project", mr.ProjectID, "mr", mr.IID, "head_sha", mr.SHA, "target_sha", targetBranch.Commit.ID, "error", err)
		} else if enqueued {
			slog.Info("merge request review queued", "project", mr.ProjectID, "mr", mr.IID, "head_sha", mr.SHA, "target_sha", targetBranch.Commit.ID, "source_branch", mr.SourceBranch, "target_branch", mr.TargetBranch)
		}
	}
}

func routes(st *store.Store, gl *gitlab.Client, cfg config.Config, viewerTarget string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health/live", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/health/ready", func(w http.ResponseWriter, r *http.Request) {
		if _, err := st.Dashboard(r.Context()); err != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
	if viewerTarget != "" {
		if target, err := url.Parse(viewerTarget); err == nil && target.Host != "" {
			proxy := httputil.NewSingleHostReverseProxy(target)
			origDirector := proxy.Director
			proxy.Director = func(req *http.Request) {
				origDirector(req)
				req.Host = target.Host
			}
			mux.Handle("/r/", proxy)
			mux.Handle("/static/", proxy)
			mux.HandleFunc("/viewer", func(w http.ResponseWriter, r *http.Request) {
				r.URL.Path = "/"
				proxy.ServeHTTP(w, r)
			})
			slog.Info("viewer proxy mounted", "paths", "/r/,/static/", "target", viewerTarget)
		}
	}
	mux.HandleFunc("/api/v1/admin/dashboard", func(w http.ResponseWriter, r *http.Request) {
		dashboard, err := st.Dashboard(r.Context())
		writeJSON(w, dashboard, err)
	})
	mux.HandleFunc("/api/v1/admin/queue", func(w http.ResponseWriter, r *http.Request) {
		jobs, err := st.ListQueue(r.Context(), parseLimit(r, 100))
		writeJSON(w, jobs, err)
	})
	mux.HandleFunc("/api/v1/admin/history", func(w http.ResponseWriter, r *http.Request) {
		jobs, err := st.ListHistory(r.Context(), parseLimit(r, 100))
		writeJSON(w, jobs, err)
	})
	registerQualityRoutes(mux, st, gl, cfg)
	registerAdminRoutes(mux, st, gl, cfg)
	mux.HandleFunc("/api/v1/admin/projects", func(w http.ResponseWriter, r *http.Request) {
		jobs, err := st.ListAllReviews(r.Context())
		if err != nil {
			writeJSON(w, nil, err)
			return
		}
		projects := make(map[int64]*adminProject)
		seenMRs := make(map[[2]int64]struct{})
		for _, job := range jobs {
			key := [2]int64{job.TargetProjectID, job.MRIID}
			if _, exists := seenMRs[key]; exists {
				continue
			}
			seenMRs[key] = struct{}{}
			project := projects[job.TargetProjectID]
			if project == nil {
				project = &adminProject{ID: job.TargetProjectID}
				projects[job.TargetProjectID] = project
			}
			project.Reviews = append(project.Reviews, job)
		}
		result := make([]adminProject, 0, len(projects))
		absDataDir, absErr := filepath.Abs(cfg.DataDir)
		if absErr != nil {
			writeJSON(w, nil, absErr)
			return
		}
		reportPublisher := &publisher.Publisher{ViewerURL: cfg.Review.ViewerURL}
		for _, project := range projects {
			remote, projectErr := gl.GetProject(r.Context(), project.ID)
			if projectErr != nil {
				writeJSON(w, nil, projectErr)
				return
			}
			project.Name, project.Description, project.PathWithNamespace, project.WebURL = remote.Name, remote.Description, remote.PathWithNamespace, remote.WebURL
			repoDir := filepath.Join(absDataDir, "workspaces", workspace.WorkloadName(remote))
			for i := range project.Reviews {
				project.Reviews[i].ReportURL = reportPublisher.ReportURL(project.Reviews[i].SessionID, repoDir)
				if project.Reviews[i].ArtifactDir != "" {
					result, parseErr := review.ParseResult(filepath.Join(project.Reviews[i].ArtifactDir, "ocr-result.json"))
					if parseErr == nil && result.Manifest != nil {
						for _, item := range result.Manifest.Coverage.Selected {
							if item.Path != "" {
								project.Reviews[i].Files = append(project.Reviews[i].Files, item.Path)
							}
						}
					}
				}
			}
			result = append(result, *project)
		}
		sort.Slice(result, func(i, j int) bool { return result[i].PathWithNamespace < result[j].PathWithNamespace })
		writeJSON(w, result, nil)
	})
	mux.HandleFunc("/api/v1/reviews/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/reviews/"), "/")
		if len(parts) != 3 {
			http.Error(w, `{"error":"expected /api/v1/reviews/{project_id}/{mr_iid}/{head_sha}"}`, http.StatusBadRequest)
			return
		}
		projectID, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			http.Error(w, `{"error":"invalid project_id"}`, http.StatusBadRequest)
			return
		}
		mrIID, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			http.Error(w, `{"error":"invalid mr_iid"}`, http.StatusBadRequest)
			return
		}
		job, err := st.GetReview(r.Context(), projectID, mrIID, parts[2])
		if err != nil {
			writeJSON(w, nil, err)
			return
		}
		if job == nil {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "review not found"})
			return
		}
		writeJSON(w, job, nil)
	})
	dist, err := fs.Sub(webassets.Dist, "dist")
	if err != nil {
		panic(err)
	}
	static := http.FileServer(http.FS(dist))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != "/" {
			if _, err := fs.Stat(dist, strings.TrimPrefix(r.URL.Path, "/")); err == nil {
				static.ServeHTTP(w, r)
				return
			}
		}
		r.URL.Path = "/"
		static.ServeHTTP(w, r)
	})
	return withAdminSecurity(mux, cfg.Server.AdminToken, cfg.Server.AdminRole)
}

type adminProject struct {
	ID                int64             `json:"id"`
	Name              string            `json:"name"`
	Description       string            `json:"description"`
	PathWithNamespace string            `json:"path_with_namespace"`
	WebURL            string            `json:"web_url"`
	Reviews           []store.ReviewJob `json:"reviews"`
}

func writeJSON(w http.ResponseWriter, value any, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(value)
}

func parseLimit(r *http.Request, fallback int) int {
	value, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || value <= 0 || value > 500 {
		return fallback
	}
	return value
}
