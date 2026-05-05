package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func serveHTTP(addrs []string, handler http.Handler) error {
	errCh := make(chan error, len(addrs))
	for _, addr := range addrs {
		addr := addr
		go func() {
			log.Printf("listening on %s", addr)
			errCh <- http.ListenAndServe(addr, handler)
		}()
	}
	return <-errCh
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/config", a.handleConfigPage)
	mux.HandleFunc("/runs", a.handleRunsPage)
	mux.HandleFunc("/runs/", a.handleRunPage)
	mux.HandleFunc("/api/meta", a.handleMeta)
	mux.HandleFunc("/api/state", a.handleState)
	mux.HandleFunc("/api/config", a.handleConfig)
	mux.HandleFunc("/api/tasks/start", a.handleStart)
	mux.HandleFunc("/api/tasks/", a.handleTaskAPI)
	mux.HandleFunc("/api/runs/", a.handleRunAPI)
	mux.HandleFunc("/", a.handleIndex)
	return mux
}
func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		serveWebFile(w, r, "index.html")
		return
	}
	serveWebFile(w, r, strings.TrimPrefix(r.URL.Path, "/"))
}

func (a *App) handleMeta(w http.ResponseWriter, r *http.Request) {
	build := currentVersionDetails()
	commit := build.Commit
	if commit == "" {
		commit = "unknown"
	}
	buildDate := build.BuildDate
	if buildDate == "" {
		buildDate = "unknown"
	}

	a.mu.RLock()
	payload := map[string]any{
		"hostname":               a.hostname,
		"log_dir":                a.logDir,
		"started":                a.started.Format(displayTimeLayout),
		"config_path":            a.configPath,
		"config_editing_enabled": configEditingEnabled(a.cfg),
		"version":                build.Version,
		"commit":                 commit,
		"build_date":             buildDate,
		"build_modified":         build.Modified,
		"version_info":           versionInfo(),
	}
	a.mu.RUnlock()
	respondJSON(w, payload)
}

func (a *App) handleRunsPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/runs" {
		http.NotFound(w, r)
		return
	}
	serveWebFile(w, r, "runs/index.html")
}

func (a *App) handleRunPage(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/runs/"), "/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	_, ok := a.runner.Find(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	serveWebFile(w, r, "run/index.html")
}

func (a *App) handleConfigPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/config" {
		http.NotFound(w, r)
		return
	}
	if !a.configEditingEnabled() {
		http.NotFound(w, r)
		return
	}
	serveWebFile(w, r, "config/index.html")
}

func (a *App) handleState(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	tasks := append([]TaskConfig(nil), a.cfg.Tasks...)
	a.mu.RUnlock()
	taskFilter := r.URL.Query().Get("task")
	var runs []*RunSummary
	if taskFilter != "" {
		runs = a.runner.SnapshotByTask(taskFilter)
	} else {
		runs = a.runner.Snapshot()
	}
	respondJSON(w, map[string]any{
		"tasks":       tasks,
		"runs":        runs,
		"task_filter": taskFilter,
	})
}

func (a *App) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.handleGetConfig(w, r)
	case http.MethodPost:
		a.handleSaveConfig(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	if !a.authorizeConfigRequest(w, r) {
		return
	}
	a.mu.RLock()
	configPath := a.configPath
	a.mu.RUnlock()

	data, err := os.ReadFile(configPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, map[string]any{
		"content": string(data),
	})
}

func (a *App) handleSaveConfig(w http.ResponseWriter, r *http.Request) {
	if !a.authorizeConfigRequest(w, r) {
		return
	}
	content := r.FormValue("content")
	if content == "" && r.Body != nil {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		content = string(data)
	}
	cfg, err := parseConfig([]byte(content))
	if err != nil {
		respondJSONStatus(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	a.mu.RLock()
	configPath := a.configPath
	a.mu.RUnlock()
	if err := writeFileAtomic(configPath, []byte(content), 0644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	cfg = normalizeRuntimeConfig(configPath, cfg)
	stamp, _ := statFileStamp(configPath)
	a.applyConfig(cfg, stamp)

	respondJSON(w, map[string]any{
		"ok":    true,
		"tasks": cfg.Tasks,
	})
}

func (a *App) authorizeConfigRequest(w http.ResponseWriter, r *http.Request) bool {
	expected := a.currentConfigPassword()
	if expected == "" {
		respondJSONStatus(w, http.StatusForbidden, map[string]any{
			"ok":    false,
			"error": "config editing is disabled",
		})
		return false
	}
	password := r.Header.Get("X-Builda-Config-Password")
	if password == "" {
		password = r.FormValue("password")
	}
	if subtle.ConstantTimeCompare([]byte(password), []byte(expected)) != 1 {
		respondJSONStatus(w, http.StatusUnauthorized, map[string]any{
			"ok":    false,
			"error": "config password did not match",
		})
		return false
	}
	return true
}

func (a *App) watchConfig(interval time.Duration) {
	if interval <= 0 {
		interval = configReloadInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		if err := a.reloadConfigIfChanged(); err != nil {
			log.Printf("reload config: %v", err)
		}
	}
}

func (a *App) reloadConfigIfChanged() error {
	a.mu.RLock()
	configPath := a.configPath
	previous := a.configFile
	a.mu.RUnlock()
	if strings.TrimSpace(configPath) == "" {
		return nil
	}
	stamp, err := statFileStamp(configPath)
	if err != nil {
		return err
	}
	if stamp.equal(previous) {
		return nil
	}
	cfg, err := loadRuntimeConfig(configPath)
	if err != nil {
		return err
	}
	a.applyConfig(cfg, stamp)
	log.Printf("reloaded config %s", configPath)
	return nil
}

func (a *App) reloadConfigFromDisk() error {
	a.mu.RLock()
	configPath := a.configPath
	a.mu.RUnlock()
	if strings.TrimSpace(configPath) == "" {
		return errors.New("config path is empty")
	}
	cfg, err := loadRuntimeConfig(configPath)
	if err != nil {
		return err
	}
	stamp, _ := statFileStamp(configPath)
	a.applyConfig(cfg, stamp)
	return nil
}

func (a *App) applyConfig(cfg Config, stamp fileStamp) {
	a.mu.Lock()
	a.cfg = cfg
	a.tasks = buildTaskMap(cfg.Tasks)
	a.configFile = stamp
	runner := a.runner
	a.mu.Unlock()

	if runner != nil {
		runner.SetMaxHistory(cfg.Server.MaxHistory)
	}
}

func (a *App) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a.startTask(r.Context(), w, r.FormValue("task_id"), r.URL.Query())
}

func (a *App) handleTaskAPI(w http.ResponseWriter, r *http.Request) {
	escapedPath := r.URL.EscapedPath()
	if escapedPath == "" {
		escapedPath = r.URL.Path
	}
	path := strings.Trim(strings.TrimPrefix(escapedPath, "/api/tasks/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] != "run" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	taskID, err := url.PathUnescape(parts[0])
	if err != nil {
		http.Error(w, "invalid task id", http.StatusBadRequest)
		return
	}
	a.startTask(r.Context(), w, taskID, r.URL.Query())
}

func (a *App) startTask(ctx context.Context, w http.ResponseWriter, taskID string, values url.Values) {
	a.mu.RLock()
	task, ok := a.tasks[taskID]
	a.mu.RUnlock()
	if !ok {
		http.Error(w, "unknown task", http.StatusBadRequest)
		return
	}
	wait, err := taskRunWaitRequested(values)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	inputs, err := collectTaskInputs(task, values)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	run, err := a.runner.Start(task, inputs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	summary, _ := a.runner.Find(run.ID)
	if !wait {
		respondJSON(w, summary)
		return
	}
	if !a.runner.Wait(ctx, run.ID) {
		if ctx.Err() != nil {
			http.Error(w, ctx.Err().Error(), http.StatusRequestTimeout)
			return
		}
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	summary, _ = a.runner.Find(run.ID)
	logData, err := readRunLog(a.logDir, run.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, RunLogResponse{
		RunSummary: summary,
		Log:        string(logData),
	})
}

func (a *App) handleRunAPI(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/runs/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 1 && parts[0] != "" {
		if r.Method == http.MethodDelete {
			if err := a.runner.Delete(parts[0]); err != nil {
				if errors.Is(err, errRunNotFound) {
					http.NotFound(w, r)
					return
				}
				if errors.Is(err, errRunActive) {
					http.Error(w, "cannot delete active runs", http.StatusConflict)
					return
				}
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			respondJSON(w, map[string]any{"ok": true})
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		run, ok := a.runner.Find(parts[0])
		if !ok {
			http.NotFound(w, r)
			return
		}
		respondJSON(w, run)
		return
	}
	if len(parts) == 2 && parts[1] == "cancel" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !a.runner.Cancel(parts[0]) {
			http.NotFound(w, r)
			return
		}
		respondJSON(w, map[string]any{"ok": true})
		return
	}
	if len(parts) == 2 && parts[1] == "log" {
		a.handleLog(w, r, parts[0])
		return
	}
	http.NotFound(w, r)
}

func (a *App) handleLog(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	data, err := readRunLog(a.logDir, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(data)
}

func readRunLog(logDir, id string) ([]byte, error) {
	path := runLogPath(logDir, id)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []byte("Log file is not available.\n"), nil
		}
		return nil, err
	}
	return data, nil
}

func runLogPath(logDir, id string) string {
	return filepath.Join(logDir, filepath.Base(id)+".log")
}

func respondJSON(w http.ResponseWriter, value any) {
	respondJSONStatus(w, http.StatusOK, value)
}

func respondJSONStatus(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("write json: %v", err)
	}
}

func serveWebFile(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name = strings.TrimPrefix(filepath.Clean("/"+name), "/")
	if name == "." || strings.HasPrefix(name, "..") {
		http.NotFound(w, r)
		return
	}
	dist, err := fs.Sub(webDist, "web/dist")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := fs.Stat(dist, name); err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeFileFS(w, r, dist, name)
}
