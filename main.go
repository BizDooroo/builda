package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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

//go:embed all:web/dist
var webDist embed.FS

const (
	StatusQueued   = "QUEUED"
	StatusRunning  = "RUNNING"
	StatusSuccess  = "SUCCESS"
	StatusFailed   = "FAILED"
	StatusCanceled = "CANCELED"
	StatusAborted  = "ABORTED"

	defaultListenAddress = ":28088"
	taskRunWaitParam     = "wait"
	configReloadInterval = time.Second

	displayTimeLayout = "06-01-02 15:04:05"

	sampleConfig = `server:
  address: "127.0.0.1:28088"
  log_dir: "logs"

tasks:
  - id: "hello"
    name: "Hello world"
    description: "Print a greeting"
    command: "echo hello $BUILDA_INPUT_NAME"
    timeout: "30s"
    inputs:
      - id: "name"
        name: "Name"
        type: "string"
        default: "world"
`

	configHelp = `
Configuration guide

Builda reads a YAML config file. If --config is omitted, Builda creates this
file when it does not exist:

  %s

Complete config.yaml example:

  server:
    # Keep Builda on loopback for local use. Binding to ":28088" or
    # "0.0.0.0:28088" exposes it broadly and is only appropriate on a trusted
    # private network with additional security controls.
    address: "127.0.0.1:28088"

    # Relative paths are resolved from the directory containing config.yaml.
    log_dir: "logs"

    # Set this to enable and protect the Web UI config editor.
    # config_password: "change-me"

  tasks:
    - id: "hello"
      name: "Hello world"
      description: "Print a greeting with run-time inputs"
      command: "echo \"Hello ${BUILDA_INPUT_NAME} from ${BUILDA_INPUT_ENVIRONMENT}\""
      timeout: "30s"
      inputs:
        - id: "name"
          name: "Name"
          description: "Name to print"
          type: "string"
          default: "world"
          required: true
        - id: "environment"
          name: "Environment"
          description: "Deployment-style target selector"
          type: "choice"
          default: "local"
          options:
            - "local"
            - "staging"
            - "prod"

    - id: "list-files"
      name: "List files"
      description: "Show repository files"
      command: "find . -maxdepth 2 -type f | sort"
      timeout: "10s"

Field reference:

  server.address
    HTTP listen address. Default is ":28088" when omitted. The --addr flag may
    be repeated and overrides server.address.

  server.log_dir
    Directory for run logs and persisted run state. Default is "logs".

  server.config_password
    Optional password for the Web UI config editor and /api/config. When
    omitted or empty, the home page hides the config button and the HTTP
    config editor is disabled. CLI config get/set does not require this
    password.

  tasks[].id
    Required unique task ID. Use URL-safe IDs such as "deploy-staging".

  tasks[].name
    Optional display name. Defaults to tasks[].id.

  tasks[].description
    Optional short text shown in the Web UI.

  tasks[].command
    Required Bash script body. Builda wraps it with #!/usr/bin/env bash,
    sources ~/.bashrc when present, then runs the configured command. Treat
    every task as privileged shell execution on the host.

  tasks[].timeout
    Optional Go duration such as "30s", "5m", or "1h".

  tasks[].inputs
    Optional run-time inputs. Inputs must be declared before callers can pass
    them as query parameters. Values are persisted in run state, written to run
    logs, and may appear in command output; do not use inputs for secrets.

  tasks[].inputs[].id
    Required input ID. Use only letters, digits, underscores, and hyphens.
    "wait" is reserved for the task run API and cannot be used as an input ID.
    The command receives the value as BUILDA_INPUT_<ID>, with hyphens converted
    to underscores and letters uppercased. For example, "target-env" becomes
    BUILDA_INPUT_TARGET_ENV.

  tasks[].inputs[].name
    Optional display name. Defaults to the input ID.

  tasks[].inputs[].description
    Optional help text shown next to the input.

  tasks[].inputs[].type
    "string", "input", or "choice". "input" is accepted as an alias for
    "string". Omitted type defaults to "string".

  tasks[].inputs[].default
    Optional default value. For choice inputs, it must be one of options.

  tasks[].inputs[].required
    Optional boolean. When true, the run request must provide a non-empty value
    or a non-empty default.

  tasks[].inputs[].options
    Required for choice inputs and invalid for string inputs. Options must be
    non-empty and unique.

Run API examples:

  curl -X POST http://localhost:28088/api/tasks/hello/run
  curl -X POST "http://localhost:28088/api/tasks/hello/run?name=Builda&environment=local"
  curl -X POST "http://localhost:28088/api/tasks/hello/run?wait=1"

Security note:

  Builda is internal-only software and is not hardened for untrusted networks.
  Do not expose it without adding authentication, authorization, CSRF
  protection, and transport security.

Print only the minimal sample config:

  builda sample-config
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
	Address        string `yaml:"address"`
	LogDir         string `yaml:"log_dir"`
	ConfigPassword string `yaml:"config_password"`
}

type TaskConfig struct {
	ID          string            `yaml:"id"`
	Name        string            `yaml:"name"`
	Description string            `yaml:"description"`
	Command     string            `yaml:"command"`
	Timeout     string            `yaml:"timeout"`
	Inputs      []TaskInputConfig `yaml:"inputs"`
}

type TaskInputConfig struct {
	ID          string   `yaml:"id"`
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Type        string   `yaml:"type"`
	Default     string   `yaml:"default"`
	Required    bool     `yaml:"required"`
	Options     []string `yaml:"options"`
}

type App struct {
	mu         sync.RWMutex
	cfg        Config
	tasks      map[string]TaskConfig
	configPath string
	runner     *Runner
	logDir     string
	hostname   string
	started    time.Time
	configFile fileStamp
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
	ID           string            `json:"id"`
	TaskID       string            `json:"task_id"`
	TaskName     string            `json:"task_name"`
	Command      string            `json:"command"`
	Inputs       map[string]string `json:"inputs,omitempty"`
	TaskSnapshot TaskConfig        `json:"task_snapshot"`
	LogPath      string            `json:"log_path"`
	Timeout      time.Duration     `json:"timeout"`
	TimeoutText  string            `json:"timeout_text"`
	Status       string            `json:"status"`
	RequestedAt  time.Time         `json:"requested_at"`
	StartedAt    time.Time         `json:"started_at"`
	FinishedAt   time.Time         `json:"finished_at"`
	CanceledAt   time.Time         `json:"canceled_at"`
	ExitCode     int               `json:"exit_code"`
	Error        string            `json:"error,omitempty"`

	cancel    context.CancelFunc `json:"-"`
	done      chan struct{}      `json:"-"`
	doneClose sync.Once          `json:"-"`
}

type RunSummary struct {
	ID           string            `json:"id"`
	TaskID       string            `json:"task_id"`
	TaskName     string            `json:"task_name"`
	Command      string            `json:"command"`
	Inputs       map[string]string `json:"inputs,omitempty"`
	TaskSnapshot TaskConfig        `json:"task_snapshot"`
	LogPath      string            `json:"log_path"`
	TimeoutText  string            `json:"timeout_text"`
	Status       string            `json:"status"`
	RequestedAt  time.Time         `json:"requested_at"`
	StartedAt    time.Time         `json:"started_at"`
	FinishedAt   time.Time         `json:"finished_at"`
	CanceledAt   time.Time         `json:"canceled_at"`
	ExitCode     int               `json:"exit_code"`
	Error        string            `json:"error,omitempty"`
}

type RunLogResponse struct {
	*RunSummary
	Log string `json:"log"`
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

type fileStamp struct {
	modTime time.Time
	size    int64
}

type addressFlags []string

func (a *addressFlags) String() string {
	return strings.Join(*a, ",")
}

func (a *addressFlags) Type() string {
	return "address"
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
	if err := newRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
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

func statFileStamp(path string) (fileStamp, error) {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}, err
	}
	return fileStamp{modTime: info.ModTime(), size: info.Size()}, nil
}

func (s fileStamp) equal(other fileStamp) bool {
	return s.size == other.size && s.modTime.Equal(other.modTime)
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
			return []string{defaultListenAddress}
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
		return []string{defaultListenAddress}
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

func loadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	return parseConfig(data)
}

func loadRuntimeConfig(path string) (Config, error) {
	cfg, err := loadConfig(path)
	if err != nil {
		return Config{}, err
	}
	return normalizeRuntimeConfig(path, cfg), nil
}

func normalizeRuntimeConfig(configPath string, cfg Config) Config {
	if cfg.Server.Address == "" {
		cfg.Server.Address = defaultListenAddress
	}
	if cfg.Server.LogDir == "" {
		cfg.Server.LogDir = "logs"
	}
	cfg.Server.LogDir = resolveLogDir(configPath, cfg.Server.LogDir)
	return cfg
}

func configEditingEnabled(cfg Config) bool {
	return strings.TrimSpace(cfg.Server.ConfigPassword) != ""
}

func (a *App) currentConfigPassword() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return strings.TrimSpace(a.cfg.Server.ConfigPassword)
}

func (a *App) configEditingEnabled() bool {
	return a.currentConfigPassword() != ""
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
		inputIDs := map[string]bool{}
		inputEnvs := map[string]string{}
		for j, input := range task.Inputs {
			inputID := strings.TrimSpace(input.ID)
			if inputID == "" {
				return Config{}, fmt.Errorf("tasks[%d].inputs[%d].id is required", i, j)
			}
			if inputID == taskRunWaitParam {
				return Config{}, fmt.Errorf("tasks[%d].inputs[%d].id %q is reserved for the task run API", i, j, inputID)
			}
			if !validInputID(inputID) {
				return Config{}, fmt.Errorf("tasks[%d].inputs[%d].id %q must contain only letters, digits, underscores, and hyphens", i, j, inputID)
			}
			if inputIDs[inputID] {
				return Config{}, fmt.Errorf("tasks[%d].inputs[%d].id duplicates %q", i, j, inputID)
			}
			envName := taskInputEnvName(inputID)
			if previous, ok := inputEnvs[envName]; ok {
				return Config{}, fmt.Errorf("tasks[%d].inputs[%d].id %q conflicts with %q as %s", i, j, inputID, previous, envName)
			}
			inputType, err := normalizeInputType(input.Type)
			if err != nil {
				return Config{}, fmt.Errorf("tasks[%d].inputs[%d].type is invalid: %w", i, j, err)
			}
			cfg.Tasks[i].Inputs[j].ID = inputID
			cfg.Tasks[i].Inputs[j].Type = inputType
			if strings.TrimSpace(input.Name) == "" {
				cfg.Tasks[i].Inputs[j].Name = inputID
			}
			if inputType == "choice" {
				options, err := normalizeChoiceOptions(input.Options)
				if err != nil {
					return Config{}, fmt.Errorf("tasks[%d].inputs[%d].options are invalid: %w", i, j, err)
				}
				cfg.Tasks[i].Inputs[j].Options = options
				if input.Default != "" && !containsString(options, input.Default) {
					return Config{}, fmt.Errorf("tasks[%d].inputs[%d].default must be one of options", i, j)
				}
			} else if len(input.Options) > 0 {
				return Config{}, fmt.Errorf("tasks[%d].inputs[%d].options are only valid for choice inputs", i, j)
			}
			inputIDs[inputID] = true
			inputEnvs[envName] = inputID
		}
		seen[task.ID] = true
		if task.Name == "" {
			cfg.Tasks[i].Name = task.ID
		}
	}
	return cfg, nil
}

func normalizeInputType(inputType string) (string, error) {
	inputType = strings.ToLower(strings.TrimSpace(inputType))
	switch inputType {
	case "", "string", "input":
		return "string", nil
	case "choice":
		return "choice", nil
	default:
		return "", fmt.Errorf("must be string, input, or choice")
	}
}

func normalizeChoiceOptions(options []string) ([]string, error) {
	if len(options) == 0 {
		return nil, errors.New("choice inputs require at least one option")
	}
	normalized := make([]string, 0, len(options))
	seen := map[string]bool{}
	for _, option := range options {
		option = strings.TrimSpace(option)
		if option == "" {
			return nil, errors.New("choice options must not be empty")
		}
		if seen[option] {
			return nil, fmt.Errorf("duplicate option %q", option)
		}
		seen[option] = true
		normalized = append(normalized, option)
	}
	return normalized, nil
}

func validInputID(inputID string) bool {
	if inputID == "" {
		return false
	}
	for _, r := range inputID {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func taskInputEnvName(inputID string) string {
	var b strings.Builder
	b.WriteString("BUILDA_INPUT_")
	for _, r := range inputID {
		if r == '-' {
			b.WriteByte('_')
			continue
		}
		if r >= 'a' && r <= 'z' {
			r -= 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func buildTaskMap(tasks []TaskConfig) map[string]TaskConfig {
	taskMap := make(map[string]TaskConfig, len(tasks))
	for _, task := range tasks {
		taskMap[task.ID] = task
	}
	return taskMap
}

func collectTaskInputs(task TaskConfig, values url.Values) (map[string]string, error) {
	allowed := map[string]TaskInputConfig{}
	for _, input := range task.Inputs {
		allowed[input.ID] = input
	}
	for key := range values {
		if key == "task_id" || key == taskRunWaitParam {
			continue
		}
		if _, ok := allowed[key]; !ok {
			return nil, fmt.Errorf("unknown input %q for task %q", key, task.ID)
		}
	}

	inputs := make(map[string]string, len(task.Inputs))
	for _, input := range task.Inputs {
		value := values.Get(input.ID)
		if value == "" {
			value = input.Default
		}
		if input.Required && value == "" {
			return nil, fmt.Errorf("input %q is required", input.ID)
		}
		if input.Type == "choice" && value != "" && !containsString(input.Options, value) {
			return nil, fmt.Errorf("input %q must be one of: %s", input.ID, strings.Join(input.Options, ", "))
		}
		inputs[input.ID] = value
	}
	return inputs, nil
}

func taskRunWaitRequested(values url.Values) (bool, error) {
	value := strings.ToLower(strings.TrimSpace(values.Get(taskRunWaitParam)))
	switch value {
	case "":
		return false, nil
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be 1 or 0", taskRunWaitParam)
	}
}

func cloneInputs(inputs map[string]string) map[string]string {
	if len(inputs) == 0 {
		return nil
	}
	clone := make(map[string]string, len(inputs))
	for key, value := range inputs {
		clone[key] = value
	}
	return clone
}

func inputEnv(inputs map[string]string) []string {
	env := make([]string, 0, len(inputs))
	for key, value := range inputs {
		env = append(env, taskInputEnvName(key)+"="+value)
	}
	sort.Strings(env)
	return env
}

func taskCommandScript(command string) string {
	return "#!/usr/bin/env bash\n" +
		"if [[ -n \"${HOME:-}\" && -f \"$HOME/.bashrc\" ]]; then\n" +
		"  source \"$HOME/.bashrc\"\n" +
		"fi\n\n" +
		command + "\n"
}

func writeTaskCommandScript(dir, runID, command string) (string, error) {
	file, err := os.CreateTemp(dir, runID+"-*.sh")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if _, err := file.WriteString(taskCommandScript(command)); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if err := os.Chmod(path, 0700); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
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

func (r *Runner) Start(task TaskConfig, inputs map[string]string) (*Run, error) {
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
		Inputs:       cloneInputs(inputs),
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
	inputs := cloneInputs(run.Inputs)
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
	if len(inputs) > 0 {
		writeLog(logWriter, "params", formatInputLog(inputs))
	}

	scriptPath, err := writeTaskCommandScript(r.logDir, id, command)
	if err != nil {
		r.finish(id, false, -1, err.Error())
		return
	}
	defer os.Remove(scriptPath)

	cmd := exec.CommandContext(ctx, scriptPath)
	cmd.Env = append(os.Environ(), inputEnv(inputs)...)
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

func formatInputLog(inputs map[string]string) string {
	data, err := json.Marshal(inputs)
	if err != nil {
		return fmt.Sprintf("%v", inputs)
	}
	return string(data)
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

func (r *Runner) Wait(ctx context.Context, id string) bool {
	r.mu.RLock()
	run := r.byID[id]
	if run == nil {
		r.mu.RUnlock()
		return false
	}
	done := run.done
	r.mu.RUnlock()

	if done == nil {
		return true
	}
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

func (r *Run) summaryLocked() *RunSummary {
	return &RunSummary{
		ID:           r.ID,
		TaskID:       r.TaskID,
		TaskName:     r.TaskName,
		Command:      r.Command,
		Inputs:       cloneInputs(r.Inputs),
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
	if r.URL.Path == "/" {
		serveWebFile(w, r, "index.html")
		return
	}
	serveWebFile(w, r, strings.TrimPrefix(r.URL.Path, "/"))
}

func (a *App) handleMeta(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	payload := map[string]any{
		"hostname":               a.hostname,
		"log_dir":                a.logDir,
		"started":                a.started.Format(displayTimeLayout),
		"config_path":            a.configPath,
		"config_editing_enabled": configEditingEnabled(a.cfg),
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
	a.mu.Unlock()
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
	data, err := readRunLog(a.logDir, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(data)
}

func readRunLog(logDir, id string) ([]byte, error) {
	path := filepath.Join(logDir, filepath.Base(id)+".log")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []byte("Log file has not been created yet.\n"), nil
		}
		return nil, err
	}
	return data, nil
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
