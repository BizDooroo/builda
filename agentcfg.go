package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultAgentSpoolDir       = "spool"
	defaultAgentPollTimeout    = 30 * time.Second
	defaultAgentRequestTimeout = 60 * time.Second
	defaultLogFlushInterval    = time.Second
	agentLogChunkLimit         = 512 << 10
)

// AgentConfig is the YAML document that drives the agent role. The agent token
// is never stored here; it lives in the protected credential file in the spool
// directory.
type AgentConfig struct {
	Role  string        `yaml:"role" json:"role"`
	Agent AgentSettings `yaml:"agent" json:"agent"`
}

// AgentSettings keeps every host-local detail on the host: the workspace root,
// the shell startup header, and the credential environment never leave it.
type AgentSettings struct {
	ID                string `yaml:"id" json:"id"`
	Name              string `yaml:"name,omitempty" json:"name,omitempty"`
	ControllerURL     string `yaml:"controller_url" json:"controller_url"`
	SpoolDir          string `yaml:"spool_dir,omitempty" json:"spool_dir,omitempty"`
	WorkspaceRoot     string `yaml:"workspace_root" json:"workspace_root"`
	ScriptHeader      string `yaml:"script_header,omitempty" json:"script_header,omitempty"`
	HeartbeatInterval string `yaml:"heartbeat_interval,omitempty" json:"heartbeat_interval,omitempty"`
	PollTimeout       string `yaml:"poll_timeout,omitempty" json:"poll_timeout,omitempty"`
	RequestTimeout    string `yaml:"request_timeout,omitempty" json:"request_timeout,omitempty"`
}

// AgentRuntime holds the resolved agent values used at run time.
type AgentRuntime struct {
	ConfigPath        string
	ID                string
	ControllerURL     string
	SpoolDir          string
	ExecDir           string
	CredentialsPath   string
	WorkspaceRoot     string
	ScriptHeader      string
	HeartbeatInterval time.Duration
	PollTimeout       time.Duration
	RequestTimeout    time.Duration
}

func loadAgentConfig(path string) (AgentConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return AgentConfig{}, err
	}
	if err := requireConfigRole(path, data, RoleAgent); err != nil {
		return AgentConfig{}, err
	}
	return parseAgentConfig(data)
}

func parseAgentConfig(data []byte) (AgentConfig, error) {
	var cfg AgentConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return AgentConfig{}, err
	}
	cfg.Role = RoleAgent
	if err := validateAgentConfig(&cfg); err != nil {
		return AgentConfig{}, err
	}
	return cfg, nil
}

func validateAgentConfig(cfg *AgentConfig) error {
	cfg.Agent.ID = strings.TrimSpace(cfg.Agent.ID)
	if !validIdentifier(cfg.Agent.ID) {
		return fmt.Errorf("agent.id %q must contain only letters, digits, underscores, and hyphens", cfg.Agent.ID)
	}
	cfg.Agent.ControllerURL = strings.TrimRight(strings.TrimSpace(cfg.Agent.ControllerURL), "/")
	if cfg.Agent.ControllerURL == "" {
		return errors.New("agent.controller_url is required")
	}
	parsed, err := url.Parse(cfg.Agent.ControllerURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("agent.controller_url %q must be an http or https URL", cfg.Agent.ControllerURL)
	}
	cfg.Agent.WorkspaceRoot = strings.TrimSpace(cfg.Agent.WorkspaceRoot)
	if cfg.Agent.WorkspaceRoot == "" {
		return errors.New("agent.workspace_root is required")
	}
	if !filepath.IsAbs(cfg.Agent.WorkspaceRoot) {
		return fmt.Errorf("agent.workspace_root %q must be an absolute path", cfg.Agent.WorkspaceRoot)
	}
	cfg.Agent.ScriptHeader = strings.TrimRight(cfg.Agent.ScriptHeader, "\r\n")
	if strings.TrimSpace(cfg.Agent.ScriptHeader) == "" {
		cfg.Agent.ScriptHeader = defaultScriptHeader
	}
	if _, err := parseDurationField("agent.heartbeat_interval", cfg.Agent.HeartbeatInterval, defaultHeartbeatInterval); err != nil {
		return err
	}
	if _, err := parseDurationField("agent.poll_timeout", cfg.Agent.PollTimeout, defaultAgentPollTimeout); err != nil {
		return err
	}
	if _, err := parseDurationField("agent.request_timeout", cfg.Agent.RequestTimeout, defaultAgentRequestTimeout); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.Agent.Name) == "" {
		cfg.Agent.Name = cfg.Agent.ID
	}
	return nil
}

func agentRuntime(configPath string, cfg AgentConfig) (AgentRuntime, error) {
	spool := resolveConfigRelativeDir(configPath, cfg.Agent.SpoolDir, defaultAgentSpoolDir)
	heartbeat, err := parseDurationField("agent.heartbeat_interval", cfg.Agent.HeartbeatInterval, defaultHeartbeatInterval)
	if err != nil {
		return AgentRuntime{}, err
	}
	poll, err := parseDurationField("agent.poll_timeout", cfg.Agent.PollTimeout, defaultAgentPollTimeout)
	if err != nil {
		return AgentRuntime{}, err
	}
	request, err := parseDurationField("agent.request_timeout", cfg.Agent.RequestTimeout, defaultAgentRequestTimeout)
	if err != nil {
		return AgentRuntime{}, err
	}
	return AgentRuntime{
		ConfigPath:        configPath,
		ID:                cfg.Agent.ID,
		ControllerURL:     cfg.Agent.ControllerURL,
		SpoolDir:          spool,
		ExecDir:           filepath.Join(spool, "exec"),
		CredentialsPath:   filepath.Join(spool, "credentials.json"),
		WorkspaceRoot:     cfg.Agent.WorkspaceRoot,
		ScriptHeader:      cfg.Agent.ScriptHeader,
		HeartbeatInterval: heartbeat,
		PollTimeout:       poll,
		RequestTimeout:    request,
	}, nil
}

// agentCredentials is the protected token file of one agent identity.
type agentCredentials struct {
	AgentID string `json:"agent_id"`
	Token   string `json:"token"`
}

func readAgentToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("agent is not enrolled; run \"builda agent enroll\" with the token issued by the controller")
		}
		return "", err
	}
	var creds agentCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return "", fmt.Errorf("parse agent credentials %s: %w", path, err)
	}
	if strings.TrimSpace(creds.Token) == "" {
		return "", fmt.Errorf("agent credentials %s has no token", path)
	}
	return creds.Token, nil
}

func writeAgentToken(path, agentID, token string) error {
	if strings.TrimSpace(token) == "" {
		return errors.New("token must not be empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(agentCredentials{AgentID: agentID, Token: strings.TrimSpace(token)}, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomicSync(path, data, 0o600)
}
