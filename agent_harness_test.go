package main

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// agentHarness is a live agent wired to a live controller over real HTTP.
type agentHarness struct {
	agent     *Agent
	runtime   AgentRuntime
	workspace string
	cancel    context.CancelFunc
	done      chan struct{}
}

// newTestFleet starts a controller HTTP server plus the requested agents.
func newTestFleet(t *testing.T, document string, agentIDs ...string) (*Controller, *httptest.Server, map[string]*agentHarness) {
	t.Helper()
	controller := newTestController(t, document)
	server := httptest.NewServer(newControllerAPI(controller).routes())
	t.Cleanup(server.Close)
	go controller.schedulerLoop(20 * time.Millisecond)

	harnesses := map[string]*agentHarness{}
	for _, id := range agentIDs {
		harnesses[id] = newAgentHarness(t, controller, server.URL, id)
	}
	return controller, server, harnesses
}

// newAgentHarness enrols one agent and prepares its workspace and spool.
func newAgentHarness(t *testing.T, controller *Controller, controllerURL, agentID string) *agentHarness {
	t.Helper()
	dir := t.TempDir()
	workspace := filepath.Join(dir, "workspace")
	for _, project := range []string{"alpha", filepath.Join("nested", "beta")} {
		if err := os.MkdirAll(filepath.Join(workspace, project), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	runtime := AgentRuntime{
		ConfigPath:        filepath.Join(dir, agentConfigName),
		ID:                agentID,
		ControllerURL:     controllerURL,
		SpoolDir:          filepath.Join(dir, "spool"),
		ExecDir:           filepath.Join(dir, "spool", "exec"),
		CredentialsPath:   filepath.Join(dir, "spool", "credentials.json"),
		WorkspaceRoot:     workspace,
		ScriptHeader:      "#!/usr/bin/env bash\nset -euo pipefail",
		HeartbeatInterval: 20 * time.Millisecond,
		PollTimeout:       200 * time.Millisecond,
		RequestTimeout:    5 * time.Second,
	}
	token := agentCredential(t, controller, agentID)
	if err := writeAgentToken(runtime.CredentialsPath, agentID, token); err != nil {
		t.Fatal(err)
	}
	agent, err := newAgent(runtime, token)
	if err != nil {
		t.Fatal(err)
	}
	return &agentHarness{agent: agent, runtime: runtime, workspace: workspace}
}

func (h *agentHarness) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.done = make(chan struct{})
	go func() {
		defer close(h.done)
		_ = h.agent.Run(ctx)
	}()
	t.Cleanup(h.stop)
}

func (h *agentHarness) stop() {
	if h.cancel == nil {
		return
	}
	h.cancel()
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
	}
	h.cancel = nil
}

// rebuild recreates the agent from the current runtime, keeping its spool.
func (h *agentHarness) rebuild(t *testing.T) {
	t.Helper()
	h.stop()
	token, err := readAgentToken(h.runtime.CredentialsPath)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := newAgent(h.runtime, token)
	if err != nil {
		t.Fatal(err)
	}
	h.agent = agent
}

// restart simulates the agent process being replaced while keeping its spool.
func (h *agentHarness) restart(t *testing.T) {
	t.Helper()
	h.rebuild(t)
	h.start(t)
}

// scriptedConfig builds a controller config whose jobs run a synthetic script.
func scriptedConfig(script string, timeout string) string {
	return `role: controller
server:
  addresses: ["127.0.0.1:0"]
  state_dir: "state"
  max_history: 50
  heartbeat_interval: "20ms"
  offline_after: "3s"
  long_poll_timeout: "200ms"
catalogs:
  - id: "projects"
    options:
      - value: "alpha"
        labels: ["android"]
        values:
          path: "alpha"
      - value: "beta"
        labels: ["android"]
        values:
          path: "nested/beta"
jobs:
  - id: "android-build"
    labels: ["linux", "android"]
    timeout: "` + timeout + `"
    workdir_param: "project"
    script: |
` + indentScript(script) + `    parameters:
      - id: "project"
        type: "choice"
        required: true
        catalog: "projects"
        catalog_labels: ["android"]
agents:
  - id: "linux-one"
    labels: ["linux", "android"]
  - id: "linux-two"
    labels: ["linux", "android"]
`
}

func indentScript(script string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(script, "\n"), "\n") {
		b.WriteString("      ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

func readControllerLog(t *testing.T, controller *Controller, id string) string {
	t.Helper()
	data, err := os.ReadFile(executionLogPath(controller.Runtime().LogDir, id))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatal(err)
	}
	return string(data)
}
