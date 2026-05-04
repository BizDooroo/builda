package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Server ServerConfig `yaml:"server"`
	Tasks  []TaskConfig `yaml:"tasks"`
}

type ServerConfig struct {
	Address        string   `yaml:"address"`
	Addresses      []string `yaml:"addresses"`
	LogDir         string   `yaml:"log_dir"`
	MaxHistory     int      `yaml:"max_history"`
	ConfigPassword string   `yaml:"config_password"`
	ScriptHeader   string   `yaml:"script_header"`
}

type TaskConfig struct {
	ID           string            `yaml:"id"`
	Name         string            `yaml:"name"`
	Description  string            `yaml:"description"`
	Script       string            `yaml:"script" json:"script"`
	Timeout      string            `yaml:"timeout"`
	ScriptHeader string            `yaml:"-" json:"script_header,omitempty"`
	Inputs       []TaskInputConfig `yaml:"inputs"`
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
	mu         sync.RWMutex
	logDir     string
	statePath  string
	maxHistory int
	runs       []*Run
	byID       map[string]*Run
	activeID   string
}

type Run struct {
	ID           string            `json:"id"`
	TaskID       string            `json:"task_id"`
	TaskName     string            `json:"task_name"`
	Script       string            `json:"script"`
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
	Script       string            `json:"script"`
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
