package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Controller runtime defaults. Operators may override each value in the
// controller config file.
const (
	defaultControllerAddress  = "127.0.0.1:28080"
	defaultHeartbeatInterval  = 5 * time.Second
	defaultOfflineAfter       = 30 * time.Second
	defaultLongPollTimeout    = 25 * time.Second
	defaultControllerStateDir = "state"
	maxQueueLabelCount        = 32
	// defaultMaxLogBytes bounds one execution log. A real build is far below
	// it, while an agent that never stops writing cannot fill the disk and
	// take the whole controller down with it.
	defaultMaxLogBytes = 64 << 20
)

// ControllerConfig is the YAML document that drives the controller role. Agent
// tokens and admin credentials are never stored here; they live in the
// separate protected credential file under the controller state directory.
type ControllerConfig struct {
	Role     string            `yaml:"role" json:"role"`
	Server   ControllerServer  `yaml:"server" json:"server"`
	Catalogs []CatalogConfig   `yaml:"catalogs" json:"catalogs"`
	Jobs     []JobConfig       `yaml:"jobs" json:"jobs"`
	Agents   []AgentDefinition `yaml:"agents" json:"agents"`
}

type ControllerServer struct {
	Address           string   `yaml:"address,omitempty" json:"address,omitempty"`
	Addresses         []string `yaml:"addresses,omitempty" json:"addresses,omitempty"`
	StateDir          string   `yaml:"state_dir,omitempty" json:"state_dir,omitempty"`
	MaxHistory        int      `yaml:"max_history,omitempty" json:"max_history,omitempty"`
	HeartbeatInterval string   `yaml:"heartbeat_interval,omitempty" json:"heartbeat_interval,omitempty"`
	OfflineAfter      string   `yaml:"offline_after,omitempty" json:"offline_after,omitempty"`
	LongPollTimeout   string   `yaml:"long_poll_timeout,omitempty" json:"long_poll_timeout,omitempty"`
	MaxLogBytes       int64    `yaml:"max_log_bytes,omitempty" json:"max_log_bytes,omitempty"`
	PublicURL         string   `yaml:"public_url,omitempty" json:"public_url,omitempty"`
}

// OptionConfig is one selectable item in a catalog or in an inline choice
// parameter. Values carry structured metadata exported to the job script.
type OptionConfig struct {
	Value  string            `yaml:"value" json:"value"`
	Label  string            `yaml:"label,omitempty" json:"label,omitempty"`
	Labels []string          `yaml:"labels,omitempty" json:"labels,omitempty"`
	Values map[string]string `yaml:"values,omitempty" json:"values,omitempty"`
}

// CatalogConfig is a reusable option list shared across jobs.
type CatalogConfig struct {
	ID          string         `yaml:"id" json:"id"`
	Name        string         `yaml:"name,omitempty" json:"name,omitempty"`
	Description string         `yaml:"description,omitempty" json:"description,omitempty"`
	Options     []OptionConfig `yaml:"options" json:"options"`
}

// ParameterConfig declares one run-time parameter of a job.
type ParameterConfig struct {
	ID            string         `yaml:"id" json:"id"`
	Name          string         `yaml:"name,omitempty" json:"name,omitempty"`
	Description   string         `yaml:"description,omitempty" json:"description,omitempty"`
	Type          string         `yaml:"type,omitempty" json:"type,omitempty"`
	Default       string         `yaml:"default,omitempty" json:"default,omitempty"`
	Required      bool           `yaml:"required,omitempty" json:"required,omitempty"`
	Options       []OptionConfig `yaml:"options,omitempty" json:"options,omitempty"`
	Catalog       string         `yaml:"catalog,omitempty" json:"catalog,omitempty"`
	CatalogLabels []string       `yaml:"catalog_labels,omitempty" json:"catalog_labels,omitempty"`
}

// JobConfig is one admin-configured script with declared parameters. Scripts
// are privileged shell execution on the agent host.
type JobConfig struct {
	ID           string            `yaml:"id" json:"id"`
	Name         string            `yaml:"name,omitempty" json:"name,omitempty"`
	Description  string            `yaml:"description,omitempty" json:"description,omitempty"`
	Enabled      *bool             `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Labels       []string          `yaml:"labels,omitempty" json:"labels,omitempty"`
	Timeout      string            `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Script       string            `yaml:"script" json:"script"`
	WorkdirParam string            `yaml:"workdir_param,omitempty" json:"workdir_param,omitempty"`
	Parameters   []ParameterConfig `yaml:"parameters,omitempty" json:"parameters,omitempty"`
}

// AgentDefinition is the centrally managed record of one agent. Labels are
// owned by the controller; the agent cannot change them.
type AgentDefinition struct {
	ID          string   `yaml:"id" json:"id"`
	Name        string   `yaml:"name,omitempty" json:"name,omitempty"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Labels      []string `yaml:"labels,omitempty" json:"labels,omitempty"`
	Enabled     *bool    `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Paused      bool     `yaml:"paused,omitempty" json:"paused,omitempty"`
}

func (j JobConfig) IsEnabled() bool       { return boolValue(j.Enabled, true) }
func (a AgentDefinition) IsEnabled() bool { return boolValue(a.Enabled, true) }

// ControllerRuntime holds the resolved values the controller needs at run time.
type ControllerRuntime struct {
	ConfigPath        string
	StateDir          string
	LogDir            string
	StatePath         string
	CredentialsPath   string
	MaxHistory        int
	MaxLogBytes       int64
	HeartbeatInterval time.Duration
	OfflineAfter      time.Duration
	LongPollTimeout   time.Duration
	ListenAddresses   []string
}

func loadControllerConfig(path string) (ControllerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ControllerConfig{}, err
	}
	if err := requireConfigRole(path, data, RoleController); err != nil {
		return ControllerConfig{}, err
	}
	return parseControllerConfig(data)
}

func parseControllerConfig(data []byte) (ControllerConfig, error) {
	var cfg ControllerConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return ControllerConfig{}, err
	}
	cfg.Role = RoleController
	if err := validateControllerConfig(&cfg); err != nil {
		return ControllerConfig{}, err
	}
	return cfg, nil
}

func marshalControllerConfig(cfg ControllerConfig) ([]byte, error) {
	cfg.Role = RoleController
	return yaml.Marshal(cfg)
}

func controllerRuntime(configPath string, cfg ControllerConfig) (ControllerRuntime, error) {
	stateDir := resolveConfigRelativeDir(configPath, cfg.Server.StateDir, defaultControllerStateDir)
	heartbeat, err := parseDurationField("server.heartbeat_interval", cfg.Server.HeartbeatInterval, defaultHeartbeatInterval)
	if err != nil {
		return ControllerRuntime{}, err
	}
	offline, err := parseDurationField("server.offline_after", cfg.Server.OfflineAfter, defaultOfflineAfter)
	if err != nil {
		return ControllerRuntime{}, err
	}
	longPoll, err := parseDurationField("server.long_poll_timeout", cfg.Server.LongPollTimeout, defaultLongPollTimeout)
	if err != nil {
		return ControllerRuntime{}, err
	}
	maxHistory := cfg.Server.MaxHistory
	if maxHistory <= 0 {
		maxHistory = defaultMaxHistory
	}
	maxLogBytes := cfg.Server.MaxLogBytes
	if maxLogBytes <= 0 {
		maxLogBytes = defaultMaxLogBytes
	}
	return ControllerRuntime{
		ConfigPath:        configPath,
		StateDir:          stateDir,
		LogDir:            filepath.Join(stateDir, "logs"),
		StatePath:         filepath.Join(stateDir, "state.json"),
		CredentialsPath:   filepath.Join(stateDir, "credentials.json"),
		MaxHistory:        maxHistory,
		MaxLogBytes:       maxLogBytes,
		HeartbeatInterval: heartbeat,
		OfflineAfter:      offline,
		LongPollTimeout:   longPoll,
		ListenAddresses:   controllerListenAddresses(cfg.Server),
	}, nil
}

func controllerListenAddresses(server ControllerServer) []string {
	addrs := make([]string, 0, 1+len(server.Addresses))
	if strings.TrimSpace(server.Address) != "" {
		addrs = append(addrs, server.Address)
	}
	addrs = append(addrs, server.Addresses...)
	normalized := make([]string, 0, len(addrs))
	seen := map[string]bool{}
	for _, addr := range addrs {
		addr = strings.TrimSpace(addr)
		if addr == "" || seen[addr] {
			continue
		}
		seen[addr] = true
		normalized = append(normalized, addr)
	}
	if len(normalized) == 0 {
		return []string{defaultControllerAddress}
	}
	return normalized
}

func findJob(cfg ControllerConfig, id string) (JobConfig, bool) {
	for _, job := range cfg.Jobs {
		if job.ID == id {
			return job, true
		}
	}
	return JobConfig{}, false
}

func findCatalog(cfg ControllerConfig, id string) (CatalogConfig, bool) {
	for _, catalog := range cfg.Catalogs {
		if catalog.ID == id {
			return catalog, true
		}
	}
	return CatalogConfig{}, false
}

func findAgentDefinition(cfg ControllerConfig, id string) (AgentDefinition, bool) {
	for _, agent := range cfg.Agents {
		if agent.ID == id {
			return agent, true
		}
	}
	return AgentDefinition{}, false
}
