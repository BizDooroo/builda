package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	StatusQueued   = "QUEUED"
	StatusRunning  = "RUNNING"
	StatusSuccess  = "SUCCESS"
	StatusFailed   = "FAILED"
	StatusCanceled = "CANCELED"
	StatusAborted  = "ABORTED"
)

type Config struct {
	Server ServerConfig `yaml:"server"`
	Tasks  []TaskConfig `yaml:"tasks"`
}

type ServerConfig struct {
	Address string `yaml:"address"`
	LogDir  string `yaml:"log_dir"`
}

type TaskConfig struct {
	ID      string `yaml:"id"`
	Name    string `yaml:"name"`
	Command string `yaml:"command"`
	Timeout string `yaml:"timeout"`
}

type App struct {
	mu         sync.RWMutex
	cfg        Config
	tasks      map[string]TaskConfig
	configPath string
	runner     *Runner
	pageTmpl   *template.Template
	configTmpl *template.Template
	logTmpl    *template.Template
	logDir     string
	started    time.Time
}

type Runner struct {
	mu        sync.RWMutex
	logDir    string
	statePath string
	runs      []*Run
	byID      map[string]*Run
	activeID  string
}

type Run struct {
	ID           string        `json:"id"`
	TaskID       string        `json:"task_id"`
	TaskName     string        `json:"task_name"`
	Command      string        `json:"command"`
	TaskSnapshot TaskConfig    `json:"task_snapshot"`
	LogPath      string        `json:"log_path"`
	Timeout      time.Duration `json:"timeout"`
	TimeoutText  string        `json:"timeout_text"`
	Status       string        `json:"status"`
	RequestedAt  time.Time     `json:"requested_at"`
	StartedAt    time.Time     `json:"started_at"`
	FinishedAt   time.Time     `json:"finished_at"`
	CanceledAt   time.Time     `json:"canceled_at"`
	ExitCode     int           `json:"exit_code"`
	Error        string        `json:"error,omitempty"`

	cancel    context.CancelFunc `json:"-"`
	done      chan struct{}      `json:"-"`
	doneClose sync.Once          `json:"-"`
}

type RunSummary struct {
	ID           string     `json:"id"`
	TaskID       string     `json:"task_id"`
	TaskName     string     `json:"task_name"`
	Command      string     `json:"command"`
	TaskSnapshot TaskConfig `json:"task_snapshot"`
	LogPath      string     `json:"log_path"`
	TimeoutText  string     `json:"timeout_text"`
	Status       string     `json:"status"`
	RequestedAt  time.Time  `json:"requested_at"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   time.Time  `json:"finished_at"`
	CanceledAt   time.Time  `json:"canceled_at"`
	ExitCode     int        `json:"exit_code"`
	Error        string     `json:"error,omitempty"`
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

func main() {
	configPath := flag.String("config", "config.yaml", "YAML configuration file")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if cfg.Server.Address == "" {
		cfg.Server.Address = ":8080"
	}
	if cfg.Server.LogDir == "" {
		cfg.Server.LogDir = "logs"
	}
	if err := os.MkdirAll(cfg.Server.LogDir, 0755); err != nil {
		log.Fatalf("create log dir: %v", err)
	}

	taskMap := buildTaskMap(cfg.Tasks)

	runner := NewRunner(cfg.Server.LogDir)
	app := &App{
		cfg:        cfg,
		tasks:      taskMap,
		configPath: *configPath,
		runner:     runner,
		pageTmpl:   template.Must(template.New("page").Parse(pageTemplate)),
		configTmpl: template.Must(template.New("config").Parse(configPageTemplate)),
		logTmpl:    template.Must(template.New("log").Parse(logPageTemplate)),
		logDir:     cfg.Server.LogDir,
		started:    time.Now(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", app.handleIndex)
	mux.HandleFunc("/config", app.handleConfigPage)
	mux.HandleFunc("/runs/", app.handleRunPage)
	mux.HandleFunc("/api/state", app.handleState)
	mux.HandleFunc("/api/config", app.handleConfig)
	mux.HandleFunc("/api/tasks/start", app.handleStart)
	mux.HandleFunc("/api/runs/", app.handleRunAPI)

	log.Printf("listening on http://localhost%s", cfg.Server.Address)
	log.Fatal(http.ListenAndServe(cfg.Server.Address, mux))
}

func loadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	return parseConfig(data)
}

func parseConfig(data []byte) (Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	seen := map[string]bool{}
	for i, task := range cfg.Tasks {
		if strings.TrimSpace(task.ID) == "" {
			return Config{}, fmt.Errorf("tasks[%d].id is required", i)
		}
		if seen[task.ID] {
			return Config{}, fmt.Errorf("duplicate task id %q", task.ID)
		}
		if strings.TrimSpace(task.Command) == "" {
			return Config{}, fmt.Errorf("tasks[%d].command is required", i)
		}
		if strings.TrimSpace(task.Timeout) != "" {
			if _, err := time.ParseDuration(task.Timeout); err != nil {
				return Config{}, fmt.Errorf("tasks[%d].timeout is invalid: %w", i, err)
			}
		}
		seen[task.ID] = true
		if task.Name == "" {
			cfg.Tasks[i].Name = task.ID
		}
	}
	return cfg, nil
}

func buildTaskMap(tasks []TaskConfig) map[string]TaskConfig {
	taskMap := make(map[string]TaskConfig, len(tasks))
	for _, task := range tasks {
		taskMap[task.ID] = task
	}
	return taskMap
}

func NewRunner(logDir string) *Runner {
	r := &Runner{
		logDir:    logDir,
		statePath: filepath.Join(logDir, "runs.json"),
		byID:      map[string]*Run{},
	}
	if err := r.loadState(); err != nil {
		log.Printf("load run state: %v", err)
	}
	r.mu.Lock()
	r.dispatchLocked()
	r.mu.Unlock()
	return r
}

func (r *Runner) Start(task TaskConfig) (*Run, error) {
	timeout := time.Duration(0)
	if task.Timeout != "" {
		parsed, err := time.ParseDuration(task.Timeout)
		if err != nil {
			return nil, fmt.Errorf("invalid timeout for %s: %w", task.ID, err)
		}
		timeout = parsed
	}

	id := newRunID()
	run := &Run{
		ID:           id,
		TaskID:       task.ID,
		TaskName:     task.Name,
		Command:      task.Command,
		TaskSnapshot: task,
		LogPath:      filepath.Join(r.logDir, id+".log"),
		Timeout:      timeout,
		TimeoutText:  task.Timeout,
		Status:       StatusQueued,
		RequestedAt:  time.Now(),
		ExitCode:     -1,
		done:         make(chan struct{}),
	}

	r.mu.Lock()
	r.runs = append(r.runs, run)
	r.byID[run.ID] = run
	r.saveLocked()
	r.dispatchLocked()
	r.mu.Unlock()
	return run, nil
}

func (r *Runner) loadState() error {
	data, err := os.ReadFile(r.statePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var runs []*Run
	if err := json.Unmarshal(data, &runs); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, run := range runs {
		if run == nil || run.ID == "" {
			continue
		}
		if run.TaskSnapshot.ID == "" {
			run.TaskSnapshot = TaskConfig{
				ID:      run.TaskID,
				Name:    run.TaskName,
				Command: run.Command,
				Timeout: run.TimeoutText,
			}
		}
		run.cancel = nil
		run.done = make(chan struct{})
		run.doneClose = sync.Once{}
		if run.ExitCode == 0 && !isTerminal(run.Status) {
			run.ExitCode = -1
		}
		if run.Status == StatusRunning {
			run.Status = StatusAborted
			run.Error = "program restarted while run was in progress"
			run.FinishedAt = time.Now()
			run.closeDone()
		} else if isTerminal(run.Status) {
			run.closeDone()
		}
		r.runs = append(r.runs, run)
		r.byID[run.ID] = run
	}
	r.saveLocked()
	return nil
}

func (r *Runner) saveLocked() {
	data, err := json.MarshalIndent(r.runs, "", "  ")
	if err != nil {
		log.Printf("marshal run state: %v", err)
		return
	}
	if err := writeFileAtomic(r.statePath, data, 0644); err != nil {
		log.Printf("write run state: %v", err)
	}
}

func (r *Runner) dispatchLocked() {
	if r.activeID != "" {
		return
	}
	var next *Run
	for _, run := range r.runs {
		if run.Status == StatusQueued {
			next = run
			break
		}
	}
	if next == nil {
		return
	}

	ctx := context.Background()
	var cancel context.CancelFunc
	if next.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, next.Timeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	next.cancel = cancel
	next.Status = StatusRunning
	next.StartedAt = time.Now()
	r.activeID = next.ID
	r.saveLocked()
	go r.execute(ctx, next.ID)
}

func (r *Runner) execute(ctx context.Context, id string) {
	r.mu.RLock()
	run := r.byID[id]
	if run == nil {
		r.mu.RUnlock()
		return
	}
	logPath := run.LogPath
	taskName := run.TaskName
	command := run.Command
	startedAt := run.StartedAt
	r.mu.RUnlock()

	file, err := os.Create(logPath)
	if err != nil {
		r.finish(id, false, -1, err.Error())
		return
	}
	defer file.Close()
	logWriter := &lockedWriter{w: file}

	writeLog(logWriter, "started", startedAt.Format(time.RFC3339))
	writeLog(logWriter, "task", taskName)
	writeLog(logWriter, "command", command)

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		r.finish(id, false, -1, err.Error())
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		r.finish(id, false, -1, err.Error())
		return
	}
	if err := cmd.Start(); err != nil {
		r.finish(id, ctx.Err() != nil, -1, err.Error())
		return
	}
	processDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
		case <-processDone:
		}
	}()

	var copyWG sync.WaitGroup
	copyWG.Add(2)
	go copyPrefixed(&copyWG, logWriter, "stdout", stdout)
	go copyPrefixed(&copyWG, logWriter, "stderr", stderr)

	waitErr := cmd.Wait()
	close(processDone)
	copyWG.Wait()

	canceled := errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded)
	exitCode := cmd.ProcessState.ExitCode()
	errText := ""
	if waitErr != nil {
		errText = waitErr.Error()
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		errText = "task timed out"
	}
	writeLog(logWriter, "finished", time.Now().Format(time.RFC3339))
	if errText != "" {
		writeLog(logWriter, "result", errText)
	}
	r.finish(id, canceled, exitCode, errText)
}

func copyPrefixed(wg *sync.WaitGroup, writer io.Writer, label string, reader io.Reader) {
	defer wg.Done()
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		writeLog(writer, label, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		writeLog(writer, label, "read error: "+err.Error())
	}
}

func writeLog(writer io.Writer, label, message string) {
	fmt.Fprintf(writer, "[%s] %-8s %s\n", time.Now().Format(time.RFC3339), label, message)
}

func (r *Runner) finish(id string, canceled bool, exitCode int, errText string) {
	r.mu.Lock()
	run := r.byID[id]
	if run == nil {
		r.mu.Unlock()
		return
	}
	run.ExitCode = exitCode
	run.Error = errText
	run.FinishedAt = time.Now()
	if canceled || !run.CanceledAt.IsZero() {
		run.Status = StatusCanceled
		if run.CanceledAt.IsZero() {
			run.CanceledAt = run.FinishedAt
		}
	} else if exitCode == 0 {
		run.Status = StatusSuccess
	} else {
		run.Status = StatusFailed
	}
	if r.activeID == id {
		r.activeID = ""
	}
	run.cancel = nil
	run.closeDone()
	r.saveLocked()
	r.dispatchLocked()
	r.mu.Unlock()
}

func (r *Runner) Cancel(id string) bool {
	var cancel context.CancelFunc
	r.mu.Lock()
	run := r.byID[id]
	if run == nil {
		r.mu.Unlock()
		return false
	}
	switch run.Status {
	case StatusQueued:
		now := time.Now()
		run.Status = StatusCanceled
		run.CanceledAt = now
		run.FinishedAt = now
		run.Error = "canceled before start"
		run.closeDone()
	case StatusRunning:
		if run.CanceledAt.IsZero() {
			run.CanceledAt = time.Now()
		}
		cancel = run.cancel
	default:
		r.mu.Unlock()
		return false
	}
	r.saveLocked()
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return true
}

func (r *Runner) Snapshot() []*RunSummary {
	r.mu.RLock()
	defer r.mu.RUnlock()

	runs := make([]*RunSummary, 0, len(r.runs))
	for _, run := range r.runs {
		runs = append(runs, run.summaryLocked())
	}
	sort.SliceStable(runs, func(i, j int) bool {
		return runs[i].RequestedAt.After(runs[j].RequestedAt)
	})
	return runs
}

func (r *Runner) Find(id string) (*RunSummary, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	run := r.byID[id]
	if run == nil {
		return nil, false
	}
	return run.summaryLocked(), true
}

func (r *Run) summaryLocked() *RunSummary {
	return &RunSummary{
		ID:           r.ID,
		TaskID:       r.TaskID,
		TaskName:     r.TaskName,
		Command:      r.Command,
		TaskSnapshot: r.TaskSnapshot,
		LogPath:      r.LogPath,
		TimeoutText:  r.TimeoutText,
		Status:       r.Status,
		RequestedAt:  r.RequestedAt,
		StartedAt:    r.StartedAt,
		FinishedAt:   r.FinishedAt,
		CanceledAt:   r.CanceledAt,
		ExitCode:     r.ExitCode,
		Error:        r.Error,
	}
}

func (r *Run) closeDone() {
	r.doneClose.Do(func() {
		if r.done != nil {
			close(r.done)
		}
	})
}

func isTerminal(status string) bool {
	switch status {
	case StatusSuccess, StatusFailed, StatusCanceled, StatusAborted:
		return true
	default:
		return false
	}
}

func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	a.mu.RLock()
	logDir := a.logDir
	started := a.started.Format(time.RFC3339)
	a.mu.RUnlock()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.pageTmpl.Execute(w, map[string]any{
		"LogDir":  logDir,
		"Started": started,
	}); err != nil {
		log.Printf("render page: %v", err)
	}
}

func (a *App) handleRunPage(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/runs/"), "/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	run, ok := a.runner.Find(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.logTmpl.Execute(w, map[string]any{
		"Run": run,
	}); err != nil {
		log.Printf("render log page: %v", err)
	}
}

func (a *App) handleConfigPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/config" {
		http.NotFound(w, r)
		return
	}
	a.mu.RLock()
	started := a.started.Format(time.RFC3339)
	configPath := a.configPath
	a.mu.RUnlock()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.configTmpl.Execute(w, map[string]any{
		"ConfigPath": configPath,
		"Started":    started,
	}); err != nil {
		log.Printf("render config page: %v", err)
	}
}

func (a *App) handleState(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	tasks := append([]TaskConfig(nil), a.cfg.Tasks...)
	a.mu.RUnlock()
	respondJSON(w, map[string]any{
		"tasks": tasks,
		"runs":  a.runner.Snapshot(),
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

	a.mu.Lock()
	a.cfg = cfg
	a.tasks = buildTaskMap(cfg.Tasks)
	a.mu.Unlock()

	respondJSON(w, map[string]any{
		"ok":    true,
		"tasks": cfg.Tasks,
	})
}

func (a *App) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	taskID := r.FormValue("task_id")
	a.mu.RLock()
	task, ok := a.tasks[taskID]
	a.mu.RUnlock()
	if !ok {
		http.Error(w, "unknown task", http.StatusBadRequest)
		return
	}
	run, err := a.runner.Start(task)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	summary, _ := a.runner.Find(run.ID)
	respondJSON(w, summary)
}

func (a *App) handleRunAPI(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/runs/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 1 && parts[0] != "" {
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
	path := filepath.Join(a.logDir, filepath.Base(id)+".log")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("Log file has not been created yet.\n"))
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(data)
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

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func newRunID() string {
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(buf[:])
}

const pageTemplate = `<!doctype html>
<html lang="ko">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Builda</title>
  <style>
    :root {
      color-scheme: light;
      --bg: #ffffff;
      --text: #171717;
      --muted: #4d4d4d;
      --faint: #808080;
      --line: rgba(0, 0, 0, 0.08);
      --ring: rgba(0, 0, 0, 0.08) 0px 0px 0px 1px;
      --card: rgba(0,0,0,0.08) 0px 0px 0px 1px, rgba(0,0,0,0.04) 0px 2px 2px, rgba(0,0,0,0.04) 0px 8px 8px -8px, #fafafa 0px 0px 0px 1px;
      --focus: hsla(212, 100%, 48%, 1);
      --blue: #0a72ef;
      --pink: #de1d8d;
      --red: #ff5b4f;
      --green: #007a55;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      font-family: Geist, Arial, "Apple Color Emoji", "Segoe UI Emoji", "Segoe UI Symbol", sans-serif;
      font-feature-settings: "liga";
      color: var(--text);
      background: var(--bg);
    }
    a { color: inherit; text-decoration: none; }
    header {
      height: 64px;
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 16px;
      padding: 0 32px;
      box-shadow: var(--ring);
      position: sticky;
      top: 0;
      z-index: 2;
      background: rgba(255,255,255,.92);
      backdrop-filter: blur(12px);
    }
	h1 {
		margin: 0;
		font-size: 24px;
		line-height: 1.33;
		font-weight: 600;
		letter-spacing: 0;
	}
    .server-meta {
      color: var(--muted);
      font-family: "Geist Mono", ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
      font-size: 12px;
      line-height: 1.5;
    }
    main {
      display: grid;
      grid-template-columns: minmax(320px, 430px) minmax(0, 1fr);
      gap: 32px;
      max-width: 1240px;
      margin: 0 auto;
      padding: 32px;
      align-items: start;
    }
    section {
      min-width: 0;
    }
    .panel-head {
      display: flex;
      align-items: end;
      justify-content: space-between;
      gap: 16px;
      padding: 0 0 16px;
    }
	h2 {
		margin: 0;
		font-size: 32px;
		line-height: 1.25;
		font-weight: 600;
		letter-spacing: 0;
	}
    .count {
      color: var(--muted);
      font-family: "Geist Mono", ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
      font-size: 12px;
      line-height: 1.5;
      white-space: nowrap;
    }
	.task-list, .run-list {
		display: grid;
		gap: 10px;
	}
    .task, .run {
      background: #fff;
      border-radius: 8px;
      box-shadow: var(--card);
      padding: 14px;
      transition: transform .16s ease, box-shadow .16s ease;
    }
    .task:hover, .run:hover {
      transform: translateY(-1px);
      box-shadow: rgba(0,0,0,0.12) 0px 0px 0px 1px, rgba(0,0,0,0.05) 0px 4px 8px, rgba(0,0,0,0.04) 0px 12px 12px -10px, #fafafa 0px 0px 0px 1px;
    }
    .row {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 12px;
    }
	strong {
		display: block;
		min-width: 0;
		overflow-wrap: anywhere;
		font-size: 16px;
		line-height: 1.5;
		font-weight: 600;
		letter-spacing: 0;
	}
    .meta {
      margin-top: 4px;
      color: var(--muted);
      font-size: 13px;
      line-height: 1.45;
    }
    code {
      display: block;
      margin-top: 10px;
      padding: 10px;
      border-radius: 6px;
      box-shadow: rgb(235,235,235) 0px 0px 0px 1px;
      background: #fafafa;
      color: #171717;
      font-family: "Geist Mono", ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
      font-size: 12px;
      line-height: 1.5;
      white-space: pre-wrap;
      word-break: break-word;
    }
    button, .button {
      appearance: none;
      display: inline-flex;
      align-items: center;
      justify-content: center;
      min-height: 34px;
      padding: 8px 12px;
      border: 0;
      border-radius: 6px;
      background: #171717;
      color: #fff;
      font: inherit;
      font-size: 14px;
      line-height: 1;
      font-weight: 500;
      cursor: pointer;
      box-shadow: var(--ring);
    }
    .button.secondary, button.secondary {
      background: #fff;
      color: #171717;
    }
    button.danger {
      background: #fff;
      color: #b42318;
    }
    button:focus-visible, .button:focus-visible {
      outline: 2px solid var(--focus);
      outline-offset: 2px;
    }
    button:disabled {
      color: #808080;
      background: #fafafa;
      cursor: wait;
    }
    .badge {
      display: inline-flex;
      align-items: center;
      min-height: 22px;
      padding: 0 10px;
      border-radius: 9999px;
      font-size: 12px;
      line-height: 1;
      font-weight: 500;
      background: #ebf5ff;
      color: #0068d6;
      white-space: nowrap;
    }
    .status-RUNNING { color: var(--blue); }
    .status-QUEUED { color: #666666; background: #fafafa; }
    .status-SUCCESS { color: var(--green); background: #ecfdf3; }
    .status-FAILED, .status-CANCELED, .status-ABORTED { color: #b42318; background: #fff1f0; }
    .times {
      display: grid;
      grid-template-columns: repeat(2, minmax(0, 1fr));
      gap: 6px 12px;
      margin-top: 12px;
      color: var(--muted);
      font-family: "Geist Mono", ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
      font-size: 12px;
      line-height: 1.5;
    }
    .times span {
      min-width: 0;
      overflow-wrap: anywhere;
    }
	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: 8px;
		margin-top: 12px;
	}
	.pager {
		display: flex;
		align-items: center;
		justify-content: flex-end;
		gap: 8px;
		margin-top: 12px;
	}
	.page-button {
		min-width: 34px;
		padding: 8px 10px;
		background: #fff;
		color: #171717;
	}
	.page-button.active {
		background: #171717;
		color: #fff;
	}
	.ellipsis {
		color: var(--muted);
		font-family: "Geist Mono", ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
		font-size: 12px;
	}
	.icon-button {
		width: 34px;
		height: 34px;
		padding: 0;
		background: #fff;
		color: #171717;
	}
	.icon-button svg {
		width: 16px;
		height: 16px;
	}
    .empty {
      min-height: 112px;
      display: grid;
      place-items: center;
      color: var(--faint);
      border-radius: 8px;
      box-shadow: rgb(235,235,235) 0px 0px 0px 1px;
      font-size: 14px;
    }
    @media (max-width: 860px) {
      header {
        height: auto;
        min-height: 64px;
        align-items: start;
        flex-direction: column;
        padding: 16px;
      }
      main {
        grid-template-columns: 1fr;
        padding: 16px;
        gap: 32px;
      }
	h2 {
		font-size: 28px;
		letter-spacing: 0;
	}
      .row {
        align-items: start;
      }
      .times {
        grid-template-columns: 1fr;
      }
    }
  </style>
</head>
<body>
  <header>
    <h1>Builda</h1>
    <div class="row">
      <div class="server-meta">logs {{.LogDir}} · started {{.Started}}</div>
      <a class="button icon-button" href="/config" aria-label="Edit config" title="Edit config">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
          <path d="M12 20h9"/>
          <path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z"/>
        </svg>
      </a>
    </div>
  </header>

  <main>
    <section>
      <div class="panel-head">
        <h2>Task list</h2>
        <div id="task-count" class="count">0 tasks</div>
      </div>
      <div id="tasks" class="task-list"></div>
    </section>

    <section>
      <div class="panel-head">
        <h2>Run list</h2>
        <div id="run-count" class="count">0 runs</div>
      </div>
      <div id="runs" class="run-list"></div>
      <div id="pager" class="pager"></div>
    </section>
  </main>

  <script>
    const tasksEl = document.querySelector("#tasks");
    const taskCountEl = document.querySelector("#task-count");
    const runsEl = document.querySelector("#runs");
    const runCountEl = document.querySelector("#run-count");
    const pagerEl = document.querySelector("#pager");
    const pageSize = 10;
    let currentPage = 1;
    let runTotal = 0;

    document.addEventListener("click", async (event) => {
      const start = event.target.closest("[data-start]");
      if (start) {
        event.preventDefault();
        start.disabled = true;
        try {
          const body = new URLSearchParams({task_id: start.dataset.start});
          const response = await fetch("/api/tasks/start", {method: "POST", body});
          if (!response.ok) throw new Error(await response.text());
          await refresh();
        } catch (error) {
          alert(error.message);
        } finally {
          start.disabled = false;
        }
      }

      const cancel = event.target.closest("[data-cancel]");
      if (cancel) {
        event.preventDefault();
        await fetch("/api/runs/" + encodeURIComponent(cancel.dataset.cancel) + "/cancel", {method: "POST"});
        await refresh();
      }

      const page = event.target.closest("[data-page]");
      if (page) {
        event.preventDefault();
        currentPage = Number(page.dataset.page);
        await refresh();
      }
    });

    async function refresh() {
      const response = await fetch("/api/state");
      const state = await response.json();
      renderTasks(state.tasks || []);
      renderRuns(state.runs || []);
    }

    function renderTasks(tasks) {
      taskCountEl.textContent = tasks.length + (tasks.length === 1 ? " task" : " tasks");
      if (!tasks.length) {
        tasksEl.innerHTML = '<div class="empty">No tasks configured.</div>';
        return;
      }
      tasksEl.innerHTML = tasks.map((task) => {
        const timeout = task.Timeout ? " · timeout " + escapeHTML(task.Timeout) : "";
        return '<article class="task">' +
          '<div class="row"><div><strong>' + escapeHTML(task.Name || task.ID) + '</strong>' +
          '<div class="meta">' + escapeHTML(task.ID) + timeout + '</div></div>' +
          '<button data-start="' + escapeHTML(task.ID) + '">Run</button></div>' +
          '<code>' + escapeHTML(task.Command) + '</code>' +
          '</article>';
      }).join("");
    }

    function renderRuns(runs) {
      runTotal = runs.length;
      const pageCount = Math.max(1, Math.ceil(runs.length / pageSize));
      currentPage = Math.min(Math.max(1, currentPage), pageCount);
      const start = (currentPage - 1) * pageSize;
      const pageRuns = runs.slice(start, start + pageSize);
      runCountEl.textContent = runs.length + (runs.length === 1 ? " run" : " runs");
      renderPager(pageCount);
      if (!runs.length) {
        runsEl.innerHTML = '<div class="empty">No runs yet.</div>';
        return;
      }
      runsEl.innerHTML = pageRuns.map((run) => {
        const canCancel = run.status === "QUEUED" || run.status === "RUNNING";
        const cancel = canCancel ? '<button class="danger" data-cancel="' + escapeHTML(run.id) + '">Cancel</button>' : "";
        return '<article class="run">' +
          '<div class="row"><div><strong>' + escapeHTML(run.task_name) + '</strong><div class="meta">' + escapeHTML(run.id) + '</div></div>' +
          '<span class="badge status-' + escapeHTML(run.status) + '">' + escapeHTML(run.status.toLowerCase()) + '</span></div>' +
          '<code>' + escapeHTML(run.command) + '</code>' +
          renderTimes(run) +
          '<div class="actions"><a class="button secondary" href="/runs/' + encodeURIComponent(run.id) + '">View log</a>' + cancel + '</div>' +
          '</article>';
      }).join("");
    }

    function renderPager(pageCount) {
      if (pageCount <= 1) {
        pagerEl.innerHTML = "";
        return;
      }
      const pages = paginationItems(currentPage, pageCount);
      const prev = '<button class="page-button" data-page="' + Math.max(1, currentPage - 1) + '" ' + (currentPage === 1 ? "disabled" : "") + '>&lt;</button>';
      const next = '<button class="page-button" data-page="' + Math.min(pageCount, currentPage + 1) + '" ' + (currentPage === pageCount ? "disabled" : "") + '>&gt;</button>';
      pagerEl.innerHTML = prev + pages.map((page) => {
        if (page === "...") return '<span class="ellipsis">...</span>';
        return '<button class="page-button' + (page === currentPage ? " active" : "") + '" data-page="' + page + '">' + page + '</button>';
      }).join("") + next;
    }

    function paginationItems(page, total) {
      if (total <= 7) return Array.from({length: total}, (_, index) => index + 1);
      const set = new Set([1, 2, total - 1, total, page - 1, page, page + 1]);
      const numbers = Array.from(set)
        .filter((value) => value >= 1 && value <= total)
        .sort((a, b) => a - b);
      const items = [];
      for (const value of numbers) {
        if (items.length && value - items[items.length - 1] > 1) items.push("...");
        items.push(value);
      }
      return items;
    }

    function renderTimes(run) {
      return '<div class="times">' +
        '<span>request ' + formatTime(run.requested_at) + '</span>' +
        '<span>start ' + formatTime(run.started_at) + '</span>' +
        '<span>finished ' + formatTime(run.finished_at) + '</span>' +
        '<span>cancelled ' + formatTime(run.canceled_at) + '</span>' +
        '</div>';
    }

    function formatTime(value) {
      if (!value || String(value).startsWith("0001-")) return "-";
      return new Date(value).toLocaleString();
    }

    function escapeHTML(value) {
      return String(value ?? "")
        .replaceAll("&", "&amp;")
        .replaceAll("<", "&lt;")
        .replaceAll(">", "&gt;")
        .replaceAll('"', "&quot;")
        .replaceAll("'", "&#39;");
    }

    refresh();
    setInterval(refresh, 1500);
  </script>
</body>
</html>`

const configPageTemplate = `<!doctype html>
<html lang="ko">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Config · Builda</title>
  <style>
    :root {
      color-scheme: light;
      --text: #171717;
      --muted: #4d4d4d;
      --ring: rgba(0, 0, 0, 0.08) 0px 0px 0px 1px;
      --card: rgba(0,0,0,0.08) 0px 0px 0px 1px, rgba(0,0,0,0.04) 0px 2px 2px, rgba(0,0,0,0.04) 0px 8px 8px -8px, #fafafa 0px 0px 0px 1px;
      --focus: hsla(212, 100%, 48%, 1);
      --green: #007a55;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      background: #fff;
      color: var(--text);
      font-family: Geist, Arial, "Apple Color Emoji", "Segoe UI Emoji", "Segoe UI Symbol", sans-serif;
      font-feature-settings: "liga";
    }
    a { color: inherit; text-decoration: none; }
    header {
      height: 64px;
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 16px;
      padding: 0 32px;
      box-shadow: var(--ring);
      position: sticky;
      top: 0;
      background: rgba(255,255,255,.92);
      backdrop-filter: blur(12px);
    }
    main {
      max-width: 1180px;
      margin: 0 auto;
      padding: 32px;
      display: grid;
      gap: 16px;
    }
    h1 {
      margin: 0;
      font-size: 32px;
      line-height: 1.25;
      font-weight: 600;
      letter-spacing: 0;
    }
    .button, button {
      appearance: none;
      display: inline-flex;
      align-items: center;
      justify-content: center;
      min-height: 34px;
      padding: 8px 12px;
      border: 0;
      border-radius: 6px;
      background: #171717;
      color: #fff;
      box-shadow: var(--ring);
      font: inherit;
      font-size: 14px;
      line-height: 1;
      font-weight: 500;
      cursor: pointer;
    }
    .button.secondary {
      background: #fff;
      color: #171717;
    }
    .button:focus-visible, button:focus-visible, textarea:focus-visible {
      outline: 2px solid var(--focus);
      outline-offset: 2px;
    }
    .meta {
      color: var(--muted);
      font-family: "Geist Mono", ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
      font-size: 12px;
      line-height: 1.5;
    }
    .panel {
      display: grid;
      gap: 12px;
    }
    .panel-head {
      display: flex;
      align-items: end;
      justify-content: space-between;
      gap: 16px;
    }
    textarea {
      width: 100%;
      min-height: 620px;
      resize: vertical;
      padding: 14px;
      border: 0;
      border-radius: 8px;
      box-shadow: var(--card);
      background: #fff;
      color: #171717;
      font-family: "Geist Mono", ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
      font-size: 13px;
      line-height: 1.54;
    }
    .editor-status {
      min-height: 20px;
      color: var(--muted);
      font-size: 13px;
      line-height: 1.45;
    }
    .editor-status.error {
      color: #b42318;
    }
    .editor-status.ok {
      color: var(--green);
    }
    @media (max-width: 760px) {
      header {
        height: auto;
        min-height: 64px;
        align-items: start;
        flex-direction: column;
        padding: 16px;
      }
      main {
        padding: 16px;
      }
      .panel-head {
        align-items: start;
        flex-direction: column;
      }
    }
  </style>
</head>
<body>
  <header>
    <a class="button secondary" href="/">Back</a>
    <div class="meta">config {{.ConfigPath}} · started {{.Started}}</div>
  </header>
  <main>
    <section class="panel">
      <div class="panel-head">
        <div>
          <h1>Config editor</h1>
          <div class="meta">{{.ConfigPath}}</div>
        </div>
        <button id="save-config">Save</button>
      </div>
      <textarea id="config-editor" spellcheck="false"></textarea>
      <div id="config-status" class="editor-status"></div>
    </section>
  </main>

  <script>
    const configEditorEl = document.querySelector("#config-editor");
    const configStatusEl = document.querySelector("#config-status");
    const saveConfigEl = document.querySelector("#save-config");

    saveConfigEl.addEventListener("click", async (event) => {
      event.preventDefault();
      await saveConfig();
    });

    async function loadConfig() {
      const response = await fetch("/api/config");
      if (!response.ok) {
        configStatus("Failed to load config: " + await response.text(), "error");
        return;
      }
      const payload = await response.json();
      configEditorEl.value = payload.content || "";
      configStatus("", "");
    }

    async function saveConfig() {
      saveConfigEl.disabled = true;
      configStatus("Validating...", "");
      try {
        const body = new URLSearchParams({content: configEditorEl.value});
        const response = await fetch("/api/config", {method: "POST", body});
        const payload = await response.json().catch(() => ({}));
        if (!response.ok || !payload.ok) {
          throw new Error(payload.error || "Config save failed.");
        }
        configStatus("Saved.", "ok");
      } catch (error) {
        configStatus(error.message, "error");
      } finally {
        saveConfigEl.disabled = false;
      }
    }

    function configStatus(message, type) {
      configStatusEl.textContent = message;
      configStatusEl.className = "editor-status" + (type ? " " + type : "");
    }

    loadConfig();
  </script>
</body>
</html>`

const logPageTemplate = `<!doctype html>
<html lang="ko">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{.Run.TaskName}} · Builda</title>
  <style>
    :root {
      color-scheme: light;
      --text: #171717;
      --muted: #4d4d4d;
      --ring: rgba(0, 0, 0, 0.08) 0px 0px 0px 1px;
      --card: rgba(0,0,0,0.08) 0px 0px 0px 1px, rgba(0,0,0,0.04) 0px 2px 2px, rgba(0,0,0,0.04) 0px 8px 8px -8px, #fafafa 0px 0px 0px 1px;
      --focus: hsla(212, 100%, 48%, 1);
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      background: #fff;
      color: var(--text);
      font-family: Geist, Arial, "Apple Color Emoji", "Segoe UI Emoji", "Segoe UI Symbol", sans-serif;
      font-feature-settings: "liga";
    }
    a { color: inherit; text-decoration: none; }
    header {
      height: 64px;
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 16px;
      padding: 0 32px;
      box-shadow: var(--ring);
      position: sticky;
      top: 0;
      background: rgba(255,255,255,.92);
      backdrop-filter: blur(12px);
    }
    main {
      max-width: 1180px;
      margin: 0 auto;
      padding: 32px;
      display: grid;
      gap: 16px;
    }
	h1 {
		margin: 0;
		font-size: 32px;
		line-height: 1.25;
		font-weight: 600;
		letter-spacing: 0;
	}
    .button {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      min-height: 34px;
      padding: 8px 12px;
      border-radius: 6px;
      background: #fff;
      color: #171717;
      box-shadow: var(--ring);
      font-size: 14px;
      line-height: 1;
      font-weight: 500;
    }
    .button:focus-visible {
      outline: 2px solid var(--focus);
      outline-offset: 2px;
    }
    .summary {
      display: grid;
      gap: 10px;
      padding: 16px;
      border-radius: 8px;
      box-shadow: var(--card);
    }
    .row {
      display: flex;
      justify-content: space-between;
      gap: 16px;
      flex-wrap: wrap;
    }
    .meta, .times {
      color: var(--muted);
      font-family: "Geist Mono", ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
      font-size: 12px;
      line-height: 1.5;
    }
    .times {
      display: grid;
      grid-template-columns: repeat(4, minmax(0, 1fr));
      gap: 8px 16px;
    }
    .times span {
      overflow-wrap: anywhere;
    }
    .badge {
      display: inline-flex;
      align-items: center;
      min-height: 22px;
      padding: 0 10px;
      border-radius: 9999px;
      font-size: 12px;
      line-height: 1;
      font-weight: 500;
      background: #ebf5ff;
      color: #0068d6;
    }
    pre {
      margin: 0;
      min-height: 520px;
      overflow: auto;
      padding: 16px;
      border-radius: 8px;
      box-shadow: var(--card);
      background: #171717;
      color: #fafafa;
      font-family: "Geist Mono", ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
      font-size: 13px;
      line-height: 1.54;
      white-space: pre-wrap;
      word-break: break-word;
    }
    @media (max-width: 760px) {
      header {
        padding: 16px;
      }
      main {
        padding: 16px;
      }
      .times {
        grid-template-columns: 1fr;
      }
    }
  </style>
</head>
<body>
  <header>
    <a class="button" href="/">Back</a>
    <div class="meta" id="status">{{.Run.Status}}</div>
  </header>
  <main>
    <section class="summary">
      <div class="row">
        <div>
          <h1>{{.Run.TaskName}}</h1>
          <div class="meta">{{.Run.ID}}</div>
        </div>
        <span class="badge" id="badge">{{.Run.Status}}</span>
      </div>
      <div class="meta">{{.Run.Command}}</div>
      <div class="times">
        <span id="requested">request -</span>
        <span id="started">start -</span>
        <span id="finished">finished -</span>
        <span id="canceled">cancelled -</span>
      </div>
    </section>
    <pre id="log">Loading log...</pre>
  </main>

  <script>
	const runID = "{{.Run.ID}}";
    const logEl = document.querySelector("#log");
    const statusEl = document.querySelector("#status");
    const badgeEl = document.querySelector("#badge");
    const requestedEl = document.querySelector("#requested");
    const startedEl = document.querySelector("#started");
    const finishedEl = document.querySelector("#finished");
    const canceledEl = document.querySelector("#canceled");
    let timer = 0;

    async function refresh() {
      const [runResponse, logResponse] = await Promise.all([
        fetch("/api/runs/" + encodeURIComponent(runID)),
        fetch("/api/runs/" + encodeURIComponent(runID) + "/log")
      ]);
      if (runResponse.ok) {
        const run = await runResponse.json();
        statusEl.textContent = run.status;
        badgeEl.textContent = run.status;
        requestedEl.textContent = "request " + formatTime(run.requested_at);
        startedEl.textContent = "start " + formatTime(run.started_at);
        finishedEl.textContent = "finished " + formatTime(run.finished_at);
        canceledEl.textContent = "cancelled " + formatTime(run.canceled_at);
        if (run.status !== "QUEUED" && run.status !== "RUNNING" && timer) {
          clearInterval(timer);
          timer = 0;
        }
      }
      if (logResponse.ok) {
        const atBottom = logEl.scrollTop + logEl.clientHeight >= logEl.scrollHeight - 8;
        logEl.textContent = await logResponse.text();
        if (atBottom) logEl.scrollTop = logEl.scrollHeight;
      }
    }

    function formatTime(value) {
      if (!value || String(value).startsWith("0001-")) return "-";
      return new Date(value).toLocaleString();
    }

    refresh();
    timer = setInterval(refresh, 1000);
  </script>
</body>
</html>`
