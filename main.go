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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
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

	displayTimeLayout = "06-01-02 15:04:05"

	sampleConfig = `server:
  address: "127.0.0.1:8080"
  log_dir: "logs"

tasks:
  - id: "hello"
    name: "Hello world"
    description: "Print a greeting"
    command: "echo hello"
    timeout: "30s"
`
)

var (
	version = "dev"
	commit  = ""
	date    = ""
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
	ID          string `yaml:"id"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Command     string `yaml:"command"`
	Timeout     string `yaml:"timeout"`
}

type App struct {
	mu         sync.RWMutex
	cfg        Config
	tasks      map[string]TaskConfig
	configPath string
	runner     *Runner
	pageTmpl   *template.Template
	runsTmpl   *template.Template
	configTmpl *template.Template
	logTmpl    *template.Template
	logDir     string
	hostname   string
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

type addressFlags []string

func (a *addressFlags) String() string {
	return strings.Join(*a, ",")
}

func (a *addressFlags) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("address must not be empty")
	}
	*a = append(*a, value)
	return nil
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

func main() {
	configPath := flag.String("config", defaultConfigPath(), "YAML configuration file")
	versionFlag := flag.Bool("version", false, "print version information and exit")
	sampleConfigFlag := flag.Bool("print-sample-config", false, "print a sample configuration file and exit")
	var addrs addressFlags
	flag.Var(&addrs, "addr", "HTTP listen address; repeat to bind multiple interfaces and override server.address")
	flag.Parse()
	configPathProvided := flagPassed("config")

	if *versionFlag {
		fmt.Println(versionInfo())
		return
	}
	if *sampleConfigFlag {
		fmt.Print(sampleConfig)
		return
	}

	if !configPathProvided {
		if err := ensureDefaultConfig(*configPath); err != nil {
			log.Fatalf("initialize default config: %v", err)
		}
	}
	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if cfg.Server.Address == "" {
		cfg.Server.Address = ":8080"
	}
	listenAddrs := resolveListenAddresses(cfg.Server.Address, addrs)
	if cfg.Server.LogDir == "" {
		cfg.Server.LogDir = "logs"
	}
	cfg.Server.LogDir = resolveLogDir(*configPath, cfg.Server.LogDir)
	if err := os.MkdirAll(cfg.Server.LogDir, 0755); err != nil {
		log.Fatalf("create log dir: %v", err)
	}

	taskMap := buildTaskMap(cfg.Tasks)
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "unknown-host"
	}

	runner := NewRunner(cfg.Server.LogDir)
	app := &App{
		cfg:        cfg,
		tasks:      taskMap,
		configPath: *configPath,
		runner:     runner,
		pageTmpl:   template.Must(template.New("page").Parse(pageTemplate)),
		runsTmpl:   template.Must(template.New("runs").Parse(runsPageTemplate)),
		configTmpl: template.Must(template.New("config").Parse(configPageTemplate)),
		logTmpl:    template.Must(template.New("log").Parse(logPageTemplate)),
		logDir:     cfg.Server.LogDir,
		hostname:   hostname,
		started:    time.Now(),
	}

	log.Fatal(serveHTTP(listenAddrs, app.routes()))
}

func flagPassed(name string) bool {
	found := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

func defaultConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return "config.yaml"
	}
	return filepath.Join(dir, "builda", "config.yaml")
}

func ensureDefaultConfig(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return writeFileAtomic(path, []byte(sampleConfig), 0644)
}

func resolveLogDir(configPath, logDir string) string {
	logDir = strings.TrimSpace(logDir)
	if logDir == "" {
		logDir = "logs"
	}
	if filepath.IsAbs(logDir) {
		return filepath.Clean(logDir)
	}
	base := filepath.Dir(configPath)
	if strings.TrimSpace(base) == "" || base == "." {
		return filepath.Clean(logDir)
	}
	return filepath.Clean(filepath.Join(base, logDir))
}

func versionInfo() string {
	v := strings.TrimSpace(version)
	rev := strings.TrimSpace(commit)
	built := strings.TrimSpace(date)
	modified := ""

	if info, ok := debug.ReadBuildInfo(); ok {
		if (v == "" || v == "dev") && info.Main.Version != "" && info.Main.Version != "(devel)" {
			v = info.Main.Version
		}
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				if rev == "" {
					rev = setting.Value
				}
			case "vcs.time":
				if built == "" {
					built = setting.Value
				}
			case "vcs.modified":
				if setting.Value == "true" {
					modified = " dirty"
				}
			}
		}
	}

	if v == "" {
		v = "dev"
	}
	parts := []string{"builda " + v}
	if rev != "" {
		if len(rev) > 12 {
			rev = rev[:12]
		}
		parts = append(parts, "commit "+rev+modified)
	}
	if built != "" {
		parts = append(parts, "built "+built)
	}
	return strings.Join(parts, ", ")
}

func resolveListenAddresses(configAddress string, flagAddresses []string) []string {
	if len(flagAddresses) == 0 {
		configAddress = strings.TrimSpace(configAddress)
		if configAddress == "" {
			return []string{":8080"}
		}
		return []string{configAddress}
	}
	addrs := make([]string, 0, len(flagAddresses))
	seen := map[string]bool{}
	for _, addr := range flagAddresses {
		addr = strings.TrimSpace(addr)
		if addr == "" || seen[addr] {
			continue
		}
		addrs = append(addrs, addr)
		seen[addr] = true
	}
	if len(addrs) == 0 {
		return []string{":8080"}
	}
	return addrs
}

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
	mux.HandleFunc("/", a.handleIndex)
	mux.HandleFunc("/config", a.handleConfigPage)
	mux.HandleFunc("/runs", a.handleRunsPage)
	mux.HandleFunc("/runs/", a.handleRunPage)
	mux.HandleFunc("/api/state", a.handleState)
	mux.HandleFunc("/api/config", a.handleConfig)
	mux.HandleFunc("/api/tasks/start", a.handleStart)
	mux.HandleFunc("/api/tasks/", a.handleTaskAPI)
	mux.HandleFunc("/api/runs/", a.handleRunAPI)
	return mux
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

	writeLog(logWriter, "started", startedAt.Format(displayTimeLayout))
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
	writeLog(logWriter, "finished", time.Now().Format(displayTimeLayout))
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
	fmt.Fprintf(writer, "[%s] %-8s %s\n", time.Now().Format(displayTimeLayout), label, message)
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

	return r.snapshotLocked("")
}

func (r *Runner) SnapshotByTask(taskID string) []*RunSummary {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.snapshotLocked(taskID)
}

func (r *Runner) snapshotLocked(taskID string) []*RunSummary {
	runs := make([]*RunSummary, 0, len(r.runs))
	for _, run := range r.runs {
		if taskID != "" && run.TaskID != taskID {
			continue
		}
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
	hostname := a.hostname
	started := a.started.Format(displayTimeLayout)
	a.mu.RUnlock()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.pageTmpl.Execute(w, map[string]any{
		"Hostname": hostname,
		"LogDir":   logDir,
		"Started":  started,
	}); err != nil {
		log.Printf("render page: %v", err)
	}
}

func (a *App) handleRunsPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/runs" {
		http.NotFound(w, r)
		return
	}
	a.mu.RLock()
	logDir := a.logDir
	hostname := a.hostname
	started := a.started.Format(displayTimeLayout)
	a.mu.RUnlock()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.runsTmpl.Execute(w, map[string]any{
		"Hostname": hostname,
		"LogDir":   logDir,
		"Started":  started,
	}); err != nil {
		log.Printf("render runs page: %v", err)
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
	a.mu.RLock()
	hostname := a.hostname
	a.mu.RUnlock()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.logTmpl.Execute(w, map[string]any{
		"Hostname": hostname,
		"Run":      run,
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
	started := a.started.Format(displayTimeLayout)
	configPath := a.configPath
	hostname := a.hostname
	a.mu.RUnlock()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.configTmpl.Execute(w, map[string]any{
		"Hostname":   hostname,
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
	a.startTask(w, r.FormValue("task_id"))
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
	a.startTask(w, taskID)
}

func (a *App) startTask(w http.ResponseWriter, taskID string) {
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
    .api-row {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 8px;
      margin-top: 10px;
      padding: 8px 10px;
      border-radius: 6px;
      box-shadow: rgb(235,235,235) 0px 0px 0px 1px;
      background: #fafafa;
      color: var(--muted);
      font-family: "Geist Mono", ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
      font-size: 12px;
      line-height: 1.5;
      min-width: 0;
    }
    .api-row span {
      min-width: 0;
      overflow-wrap: anywhere;
    }
	.task-copy {
		min-width: 0;
	}
	.task-description {
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.task-summary {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto;
		align-items: center;
	}
	.task-description-full {
		display: grid;
		gap: 4px;
		color: var(--muted);
		font-size: 13px;
		line-height: 1.45;
	}
	.task-description-full span:first-child {
		font-family: "Geist Mono", ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
		font-size: 12px;
		line-height: 1.5;
	}
	.task-description-full span:last-child {
		color: #171717;
		overflow-wrap: anywhere;
	}
	.task-actions, .run-head-actions {
		display: inline-flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
		justify-content: flex-end;
	}
	.task-actions {
		flex-wrap: nowrap;
		white-space: nowrap;
	}
	.detail-toggle svg {
		transition: transform .16s ease;
	}
	.detail-toggle[aria-expanded="true"] svg {
		transform: rotate(180deg);
	}
	.task-details {
		display: grid;
		gap: 10px;
		margin-top: 12px;
	}
	.detail-line {
		display: flex;
		justify-content: space-between;
		gap: 12px;
		color: var(--muted);
		font-family: "Geist Mono", ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
		font-size: 12px;
		line-height: 1.5;
	}
	.detail-line span:last-child {
		min-width: 0;
		overflow-wrap: anywhere;
		text-align: right;
		color: #171717;
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
      .task-summary {
        align-items: center;
      }
      .times {
        grid-template-columns: 1fr;
      }
      .detail-line {
        display: grid;
        gap: 2px;
      }
      .detail-line span:last-child {
        text-align: left;
      }
    }
  </style>
</head>
<body>
  <header>
    <h1>Builda</h1>
    <div class="row">
      <div class="server-meta">host {{.Hostname}} · logs {{.LogDir}} · started {{.Started}}</div>
      <a class="button secondary" href="/runs">Runs</a>
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
        <h2>All tasks</h2>
        <div id="task-count" class="count">0 tasks</div>
      </div>
      <div id="tasks" class="task-list"></div>
    </section>

    <section>
      <div class="panel-head">
        <h2>Latest runs</h2>
        <div class="run-head-actions">
          <a class="button secondary" href="/runs">All runs</a>
          <div id="run-count" class="count">0 runs</div>
        </div>
      </div>
      <div id="runs" class="run-list"></div>
    </section>
  </main>

  <script>
    const tasksEl = document.querySelector("#tasks");
    const taskCountEl = document.querySelector("#task-count");
    const runsEl = document.querySelector("#runs");
    const runCountEl = document.querySelector("#run-count");
    const latestRunLimit = 10;
    const expandedTasks = new Set();
    let latestTasks = [];

    document.addEventListener("click", async (event) => {
      const toggle = event.target.closest("[data-toggle-task]");
      if (toggle) {
        event.preventDefault();
        const taskID = toggle.dataset.toggleTask;
        if (expandedTasks.has(taskID)) {
          expandedTasks.delete(taskID);
        } else {
          expandedTasks.add(taskID);
        }
        renderTasks(latestTasks);
      }

      const start = event.target.closest("[data-start]");
      if (start) {
        event.preventDefault();
        start.disabled = true;
        try {
          const response = await fetch(taskRunAPI(start.dataset.start), {method: "POST"});
          if (!response.ok) throw new Error(await response.text());
          await refresh();
        } catch (error) {
          alert(error.message);
        } finally {
          start.disabled = false;
        }
      }

      const copy = event.target.closest("[data-copy-api]");
      if (copy) {
        event.preventDefault();
        copy.disabled = true;
        try {
          await copyText(window.location.origin + copy.dataset.copyApi);
          flashButtonText(copy, "Copied");
        } catch (error) {
          flashButtonText(copy, "Failed");
          console.error("copy failed", error);
        } finally {
          copy.disabled = false;
        }
      }

      const cancel = event.target.closest("[data-cancel]");
      if (cancel) {
        event.preventDefault();
        await fetch("/api/runs/" + encodeURIComponent(cancel.dataset.cancel) + "/cancel", {method: "POST"});
        await refresh();
      }

    });

    async function refresh() {
      const response = await fetch("/api/state");
      const state = await response.json();
      latestTasks = state.tasks || [];
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
        const description = task.Description || task.ID;
        const timeout = task.Timeout ? '<div class="detail-line"><span>Timeout</span><span>' + escapeHTML(task.Timeout) + '</span></div>' : "";
        const api = taskRunAPI(task.ID);
        const isExpanded = expandedTasks.has(task.ID);
        const details = isExpanded ? '<div class="task-details">' +
          '<div class="task-description-full"><span>Description</span><span>' + escapeHTML(description) + '</span></div>' +
          '<div class="detail-line"><span>Task ID</span><span>' + escapeHTML(task.ID) + '</span></div>' +
          timeout +
          '<code>' + escapeHTML(task.Command) + '</code>' +
          '<div class="api-row"><span>POST ' + escapeHTML(api) + '</span><button class="secondary" data-copy-api="' + escapeHTML(api) + '">Copy</button></div>' +
          '<div class="actions"><a class="button secondary" href="/runs?task=' + encodeURIComponent(task.ID) + '">View runs</a></div>' +
          '</div>' : "";
        return '<article class="task">' +
          '<div class="row task-summary"><div class="task-copy"><strong>' + escapeHTML(task.Name || task.ID) + '</strong>' +
          '<div class="meta task-description">' + escapeHTML(description) + '</div></div>' +
          '<div class="task-actions"><button class="secondary icon-button detail-toggle" data-toggle-task="' + escapeHTML(task.ID) + '" aria-label="' + (isExpanded ? "Hide details" : "Show details") + '" title="' + (isExpanded ? "Hide details" : "Show details") + '" aria-expanded="' + String(isExpanded) + '">' +
          '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m6 9 6 6 6-6"/></svg>' +
          '</button><button data-start="' + escapeHTML(task.ID) + '">Run</button></div></div>' +
          details +
          '</article>';
      }).join("");
    }

    function taskRunAPI(taskID) {
      return "/api/tasks/" + encodeURIComponent(taskID) + "/run";
    }

    async function copyText(text) {
      if (navigator.clipboard && window.isSecureContext) {
        await navigator.clipboard.writeText(text);
        return;
      }

      const textarea = document.createElement("textarea");
      textarea.value = text;
      textarea.setAttribute("readonly", "");
      textarea.style.position = "fixed";
      textarea.style.top = "0";
      textarea.style.left = "0";
      textarea.style.width = "1px";
      textarea.style.height = "1px";
      textarea.style.opacity = "0";
      document.body.appendChild(textarea);
      textarea.focus();
      textarea.select();
      try {
        if (!document.execCommand("copy")) {
          throw new Error("copy command was rejected");
        }
      } finally {
        textarea.remove();
      }
    }

    function flashButtonText(button, text) {
      const previous = button.textContent;
      button.textContent = text;
      setTimeout(() => {
        if (button.isConnected) {
          button.textContent = previous;
        }
      }, 1200);
    }

    function renderRuns(runs) {
      const latestRuns = runs.slice(0, latestRunLimit);
      runCountEl.textContent = latestRuns.length + " latest";
      if (!latestRuns.length) {
        runsEl.innerHTML = '<div class="empty">No runs yet.</div>';
        return;
      }
      runsEl.innerHTML = latestRuns.map((run) => {
        const canCancel = run.status === "QUEUED" || run.status === "RUNNING";
        const cancel = canCancel ? '<button class="danger" data-cancel="' + escapeHTML(run.id) + '">Cancel</button>' : "";
        return '<article class="run">' +
          '<div class="row"><div><strong>' + escapeHTML(run.task_name) + '</strong><div class="meta">' + escapeHTML(run.id) + '</div></div>' +
          '<span class="badge status-' + escapeHTML(run.status) + '">' + escapeHTML(run.status.toLowerCase()) + '</span></div>' +
          '<code>' + escapeHTML(run.command) + '</code>' +
          renderTimes(run) +
          '<div class="actions"><a class="button secondary" href="/runs?run=' + encodeURIComponent(run.id) + '">View log</a>' + cancel + '</div>' +
          '</article>';
      }).join("");
    }

    function renderTimes(run) {
      return '<div class="times">' +
        '<span>request ' + formatTime(run.requested_at) + '</span>' +
        '<span>start ' + formatTime(run.started_at) + '</span>' +
        '<span>elapsed ' + formatElapsed(run) + '</span>' +
        '<span>duration ' + formatDuration(run) + '</span>' +
        '</div>';
    }

    function formatElapsed(run) {
      if (!hasTime(run.started_at)) return "-";
      const end = hasTime(run.finished_at) ? new Date(run.finished_at) : new Date();
      return formatDurationMs(end - new Date(run.started_at));
    }

    function formatDuration(run) {
      if (!hasTime(run.started_at) || !hasTime(run.finished_at)) return "-";
      return formatDurationMs(new Date(run.finished_at) - new Date(run.started_at));
    }

    function hasTime(value) {
      return value && !String(value).startsWith("0001-");
    }

    function formatDurationMs(value) {
      if (!Number.isFinite(value) || value < 0) return "-";
      const totalSeconds = Math.floor(value / 1000);
      const minutes = Math.floor(totalSeconds / 60);
      const seconds = totalSeconds % 60;
      if (minutes >= 60) {
        const hours = Math.floor(minutes / 60);
        return hours + "h " + (minutes % 60) + "m";
      }
      if (minutes > 0) return minutes + "m " + seconds + "s";
      return seconds + "s";
    }

    function formatTime(value) {
      if (!hasTime(value)) return "-";
      const date = new Date(value);
      const year = String(date.getFullYear()).slice(-2);
      const month = String(date.getMonth() + 1).padStart(2, "0");
      const day = String(date.getDate()).padStart(2, "0");
      const hour = String(date.getHours()).padStart(2, "0");
      const minute = String(date.getMinutes()).padStart(2, "0");
      const second = String(date.getSeconds()).padStart(2, "0");
      return year + "-" + month + "-" + day + " " + hour + ":" + minute + ":" + second;
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

const runsPageTemplate = `<!doctype html>
<html lang="ko">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Runs · Builda</title>
  <style>
    :root {
      color-scheme: light;
      --bg: #ffffff;
      --text: #171717;
      --muted: #4d4d4d;
      --faint: #808080;
      --ring: rgba(0, 0, 0, 0.08) 0px 0px 0px 1px;
      --card: rgba(0,0,0,0.08) 0px 0px 0px 1px, rgba(0,0,0,0.04) 0px 2px 2px, rgba(0,0,0,0.04) 0px 8px 8px -8px, #fafafa 0px 0px 0px 1px;
      --focus: hsla(212, 100%, 48%, 1);
      --blue: #0a72ef;
      --green: #007a55;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      background: var(--bg);
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
      z-index: 2;
      background: rgba(255,255,255,.92);
      backdrop-filter: blur(12px);
    }
    h1, h2 {
      margin: 0;
      font-weight: 600;
      letter-spacing: 0;
    }
    h1 {
      font-size: 24px;
      line-height: 1.33;
    }
    h2 {
      font-size: 32px;
      line-height: 1.25;
    }
    .server-meta, .meta, .times, .kv, pre {
      font-family: "Geist Mono", ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
    }
    .server-meta, .meta {
      color: var(--muted);
      font-size: 12px;
      line-height: 1.5;
    }
    main {
      display: grid;
      grid-template-columns: minmax(320px, 450px) minmax(0, 1fr);
      gap: 32px;
      max-width: 1360px;
      margin: 0 auto;
      padding: 32px;
      align-items: start;
    }
    .panel-head {
      display: flex;
      align-items: end;
      justify-content: space-between;
      gap: 16px;
      padding-bottom: 16px;
    }
    .toolbar {
      display: inline-flex;
      align-items: center;
      gap: 8px;
      flex-wrap: wrap;
      justify-content: flex-end;
    }
    .run-list {
      display: grid;
      gap: 8px;
    }
    .run-item {
      width: 100%;
      min-height: 74px;
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      gap: 12px;
      align-items: center;
      padding: 12px;
      border: 0;
      border-radius: 8px;
      background: #fff;
      color: inherit;
      text-align: left;
      box-shadow: var(--ring);
      cursor: pointer;
      transition: transform .16s ease, box-shadow .16s ease;
    }
    .run-item:hover, .run-item.active {
      transform: translateY(-1px);
      box-shadow: var(--card);
    }
    .run-title {
      min-width: 0;
    }
    .run-time-grid {
      grid-column: 1 / -1;
      display: grid;
      grid-template-columns: repeat(2, minmax(0, 1fr));
      gap: 6px 12px;
      color: var(--muted);
      font-family: "Geist Mono", ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
      font-size: 12px;
      line-height: 1.5;
    }
    .run-time-grid span {
      min-width: 0;
      overflow-wrap: anywhere;
    }
    .run-picker {
      display: none;
      margin-bottom: 12px;
    }
    .run-picker select {
      width: 100%;
      min-height: 40px;
      padding: 8px 36px 8px 10px;
      border: 0;
      border-radius: 8px;
      background: #fff;
      color: #171717;
      box-shadow: var(--card);
      font: inherit;
      font-size: 14px;
      line-height: 1.4;
    }
    .run-picker select:focus-visible {
      outline: 2px solid var(--focus);
      outline-offset: 2px;
    }
    strong {
      display: block;
      min-width: 0;
      overflow-wrap: anywhere;
      font-size: 15px;
      line-height: 1.45;
      font-weight: 600;
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
    .button.secondary, button.secondary {
      background: #fff;
      color: #171717;
    }
    button.danger {
      background: #fff;
      color: #b42318;
    }
    button:focus-visible, .button:focus-visible, .run-item:focus-visible {
      outline: 2px solid var(--focus);
      outline-offset: 2px;
    }
    button:disabled {
      color: #808080;
      background: #fafafa;
      cursor: wait;
    }
    .inspector {
      display: grid;
      gap: 16px;
      min-width: 0;
      position: sticky;
      top: 96px;
    }
    .summary {
      display: grid;
      gap: 12px;
      padding: 16px;
      border-radius: 8px;
      box-shadow: var(--card);
      background: #fff;
      min-width: 0;
    }
    .summary-head {
      display: flex;
      justify-content: space-between;
      gap: 16px;
      align-items: start;
    }
    .summary-title {
      min-width: 0;
    }
    .summary-actions {
      display: flex;
      gap: 8px;
      flex-wrap: wrap;
      justify-content: flex-end;
    }
    code {
      display: block;
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
    .times, .kv {
      display: grid;
      grid-template-columns: repeat(2, minmax(0, 1fr));
      gap: 8px 16px;
      color: var(--muted);
      font-size: 12px;
      line-height: 1.5;
    }
    .times span, .kv span {
      min-width: 0;
      overflow-wrap: anywhere;
    }
    .kv b {
      color: #171717;
      font-weight: 500;
    }
    pre {
      margin: 0;
      min-height: 520px;
      max-height: calc(100vh - 380px);
      overflow: auto;
      padding: 16px;
      border-radius: 8px;
      box-shadow: var(--card);
      background: #171717;
      color: #fafafa;
      font-size: 13px;
      line-height: 1.54;
      white-space: pre-wrap;
      word-break: break-word;
    }
    .empty {
      min-height: 160px;
      display: grid;
      place-items: center;
      color: var(--faint);
      border-radius: 8px;
      box-shadow: rgb(235,235,235) 0px 0px 0px 1px;
      font-size: 14px;
    }
    @media (max-width: 960px) {
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
      }
      h2 {
        font-size: 28px;
      }
      .inspector {
        position: static;
      }
      .summary-head, .panel-head {
        align-items: start;
        flex-direction: column;
      }
      .times, .kv {
        grid-template-columns: 1fr;
      }
      .run-list {
        display: none;
      }
      .run-picker:not([hidden]) {
        display: block;
      }
      pre {
        max-height: none;
      }
    }
  </style>
</head>
<body>
  <header>
    <h1>Builda</h1>
    <div class="toolbar">
      <div class="server-meta">host {{.Hostname}} · logs {{.LogDir}} · started {{.Started}}</div>
      <a class="button secondary" href="/">Tasks</a>
      <a class="button secondary" href="/config">Config</a>
    </div>
  </header>

  <main>
    <section>
      <div class="panel-head">
        <div>
          <h2>Run list</h2>
          <div id="run-filter" class="meta" hidden></div>
        </div>
        <div class="toolbar">
          <a id="clear-run-filter" class="button secondary" href="/runs" hidden>All runs</a>
          <div id="run-count" class="meta">0 runs</div>
        </div>
      </div>
      <div id="run-picker" class="run-picker" hidden>
        <select id="run-select" aria-label="Select run"></select>
      </div>
      <div id="runs" class="run-list"></div>
    </section>

    <section class="inspector">
      <div id="summary" class="summary">
        <div class="empty">Select a run.</div>
      </div>
      <pre id="log">Select a run to load its log.</pre>
    </section>
  </main>

  <script>
    const runsEl = document.querySelector("#runs");
    const runCountEl = document.querySelector("#run-count");
    const runFilterEl = document.querySelector("#run-filter");
    const clearRunFilterEl = document.querySelector("#clear-run-filter");
    const runPickerEl = document.querySelector("#run-picker");
    const runSelectEl = document.querySelector("#run-select");
    const summaryEl = document.querySelector("#summary");
    const logEl = document.querySelector("#log");
    const params = new URLSearchParams(window.location.search);
    const taskFilter = params.get("task") || "";
    let selectedRunID = params.get("run") || "";
    let latestRuns = [];

    document.addEventListener("click", async (event) => {
      const runButton = event.target.closest("[data-run-id]");
      if (runButton) {
        event.preventDefault();
        selectRun(runButton.dataset.runId, true);
      }

      const cancel = event.target.closest("[data-cancel]");
      if (cancel) {
        event.preventDefault();
        cancel.disabled = true;
        await fetch("/api/runs/" + encodeURIComponent(cancel.dataset.cancel) + "/cancel", {method: "POST"});
        await refresh();
      }
    });

    document.addEventListener("change", async (event) => {
      if (event.target === runSelectEl && runSelectEl.value) {
        await selectRun(runSelectEl.value, true);
      }
    });

    async function refresh() {
      const stateURL = taskFilter ? "/api/state?task=" + encodeURIComponent(taskFilter) : "/api/state";
      const response = await fetch(stateURL);
      const state = await response.json();
      latestRuns = state.runs || [];
      if (!latestRuns.length) {
        renderRuns(latestRuns);
        summaryEl.innerHTML = '<div class="empty">' + (taskFilter ? "No runs for this task yet." : "No runs yet.") + '</div>';
        logEl.textContent = taskFilter ? "No runs for this task yet." : "No runs yet.";
        return;
      }
      if (!selectedRunID || !latestRuns.some((run) => run.id === selectedRunID)) {
        selectedRunID = latestRuns[0].id;
        updateURL(false);
      }
      renderRuns(latestRuns);
      await renderSelectedRun();
    }

    function renderRuns(runs) {
      if (taskFilter) {
        runFilterEl.hidden = false;
        runFilterEl.textContent = "task " + taskFilter;
        clearRunFilterEl.hidden = false;
      } else {
        runFilterEl.hidden = true;
        runFilterEl.textContent = "";
        clearRunFilterEl.hidden = true;
      }
      runCountEl.textContent = runs.length + (runs.length === 1 ? " run" : " runs");
      if (!runs.length) {
        runPickerEl.hidden = true;
        runsEl.innerHTML = '<div class="empty">' + (taskFilter ? "No runs for this task yet." : "No runs yet.") + '</div>';
        return;
      }
      runPickerEl.hidden = false;
      runSelectEl.innerHTML = runs.map((run) => {
        return '<option value="' + escapeHTML(run.id) + '"' + (run.id === selectedRunID ? " selected" : "") + '>' +
          escapeHTML(run.task_name + " · " + run.status.toLowerCase() + " · request " + formatTime(run.requested_at) + " · elapsed " + formatElapsed(run) + " · duration " + formatDuration(run)) +
          '</option>';
      }).join("");
      runsEl.innerHTML = runs.map((run) => {
        const active = run.id === selectedRunID ? " active" : "";
        return '<button class="run-item' + active + '" data-run-id="' + escapeHTML(run.id) + '">' +
          '<span class="run-title"><strong>' + escapeHTML(run.task_name) + '</strong><span class="meta">' + escapeHTML(run.id) + '</span></span>' +
          '<span class="badge status-' + escapeHTML(run.status) + '">' + escapeHTML(run.status.toLowerCase()) + '</span>' +
          renderRunListTimes(run) +
          '</button>';
      }).join("");
    }

    async function selectRun(runID, push) {
      selectedRunID = runID;
      renderRuns(latestRuns);
      updateURL(push);
      await renderSelectedRun();
    }

    async function renderSelectedRun() {
      if (!selectedRunID) return;
      const [runResponse, logResponse] = await Promise.all([
        fetch("/api/runs/" + encodeURIComponent(selectedRunID)),
        fetch("/api/runs/" + encodeURIComponent(selectedRunID) + "/log")
      ]);
      if (runResponse.ok) {
        const run = await runResponse.json();
        const canCancel = run.status === "QUEUED" || run.status === "RUNNING";
        const cancel = canCancel ? '<button class="danger" data-cancel="' + escapeHTML(run.id) + '">Cancel</button>' : "";
        summaryEl.innerHTML = '<div class="summary-head"><div class="summary-title"><h2>' + escapeHTML(run.task_name) + '</h2>' +
          '<div class="meta">' + escapeHTML(run.id) + '</div></div>' +
          '<div class="summary-actions"><span class="badge status-' + escapeHTML(run.status) + '">' + escapeHTML(run.status.toLowerCase()) + '</span>' + cancel + '</div></div>' +
          '<div class="kv"><span>Task <b>' + escapeHTML(run.task_id) + '</b></span><span>Exit <b>' + escapeHTML(run.exit_code) + '</b></span></div>' +
          '<code>' + escapeHTML(run.command) + '</code>' +
          renderTimes(run);
      }
      if (logResponse.ok) {
        const atBottom = logEl.scrollTop + logEl.clientHeight >= logEl.scrollHeight - 8;
        logEl.textContent = await logResponse.text();
        if (atBottom) logEl.scrollTop = logEl.scrollHeight;
      }
    }

    function updateURL(push) {
      const next = new URLSearchParams();
      if (taskFilter) next.set("task", taskFilter);
      if (selectedRunID) next.set("run", selectedRunID);
      const url = "/runs" + (next.toString() ? "?" + next.toString() : "");
      if (push) {
        history.pushState(null, "", url);
      } else {
        history.replaceState(null, "", url);
      }
    }

    function renderTimes(run) {
      return '<div class="times">' +
        '<span>request ' + formatTime(run.requested_at) + '</span>' +
        '<span>start ' + formatTime(run.started_at) + '</span>' +
        '<span>elapsed ' + formatElapsed(run) + '</span>' +
        '<span>duration ' + formatDuration(run) + '</span>' +
        '</div>';
    }

    function renderRunListTimes(run) {
      return '<span class="run-time-grid">' +
        '<span>request ' + formatTime(run.requested_at) + '</span>' +
        '<span>start ' + formatTime(run.started_at) + '</span>' +
        '<span>elapsed ' + formatElapsed(run) + '</span>' +
        '<span>duration ' + formatDuration(run) + '</span>' +
        '</span>';
    }

    function formatElapsed(run) {
      if (!hasTime(run.started_at)) return "-";
      const end = hasTime(run.finished_at) ? new Date(run.finished_at) : new Date();
      return formatDurationMs(end - new Date(run.started_at));
    }

    function formatDuration(run) {
      if (!hasTime(run.started_at) || !hasTime(run.finished_at)) return "-";
      return formatDurationMs(new Date(run.finished_at) - new Date(run.started_at));
    }

    function hasTime(value) {
      return value && !String(value).startsWith("0001-");
    }

    function formatDurationMs(value) {
      if (!Number.isFinite(value) || value < 0) return "-";
      const totalSeconds = Math.floor(value / 1000);
      const minutes = Math.floor(totalSeconds / 60);
      const seconds = totalSeconds % 60;
      if (minutes >= 60) {
        const hours = Math.floor(minutes / 60);
        return hours + "h " + (minutes % 60) + "m";
      }
      if (minutes > 0) return minutes + "m " + seconds + "s";
      return seconds + "s";
    }

    function formatTime(value) {
      if (!hasTime(value)) return "-";
      const date = new Date(value);
      const year = String(date.getFullYear()).slice(-2);
      const month = String(date.getMonth() + 1).padStart(2, "0");
      const day = String(date.getDate()).padStart(2, "0");
      const hour = String(date.getHours()).padStart(2, "0");
      const minute = String(date.getMinutes()).padStart(2, "0");
      const second = String(date.getSeconds()).padStart(2, "0");
      return year + "-" + month + "-" + day + " " + hour + ":" + minute + ":" + second;
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
    <div class="meta">host {{.Hostname}} · config {{.ConfigPath}} · started {{.Started}}</div>
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
    <a class="button" href="/runs?run={{.Run.ID}}">Back</a>
    <div class="meta">host {{.Hostname}} · <span id="status">{{.Run.Status}}</span></div>
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
      const date = new Date(value);
      const year = String(date.getFullYear()).slice(-2);
      const month = String(date.getMonth() + 1).padStart(2, "0");
      const day = String(date.getDate()).padStart(2, "0");
      const hour = String(date.getHours()).padStart(2, "0");
      const minute = String(date.getMinutes()).padStart(2, "0");
      const second = String(date.getSeconds()).padStart(2, "0");
      return year + "-" + month + "-" + day + " " + hour + ":" + minute + ":" + second;
    }

    refresh();
    timer = setInterval(refresh, 1000);
  </script>
</body>
</html>`
