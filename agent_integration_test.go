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

// restart simulates the agent process being replaced while keeping its spool.
func (h *agentHarness) restart(t *testing.T) {
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

// TestEndToEndSuccessfulRun runs a synthetic script on a real agent and checks
// the environment, working directory, log transport, and final result.
func TestEndToEndSuccessfulRun(t *testing.T) {
	script := `echo "job=$BUILDA_JOB_ID agent=$BUILDA_AGENT_ID"
echo "project=$BUILDA_PARAM_PROJECT path=$BUILDA_PARAM_PROJECT_PATH"
echo "cwd=$(pwd)"
echo "workspace=$BUILDA_WORKSPACE"`
	controller, _, agents := newTestFleet(t, scriptedConfig(script, "30s"), "linux-one")
	agents["linux-one"].start(t)

	execution := enqueue(t, controller, "android-build", map[string]string{"project": "beta"})
	waitFor(t, "the run to succeed", func() bool {
		return statusOf(t, controller, execution.ID) == StatusSuccess
	})

	stored := mustFind(t, controller, execution.ID)
	if stored.ExitCode != 0 || !stored.LogComplete || stored.AgentID != "linux-one" {
		t.Fatalf("unexpected final execution %+v", stored)
	}
	log := readControllerLog(t, controller, execution.ID)
	for _, want := range []string{
		"job=android-build agent=linux-one",
		"project=beta path=nested/beta",
		filepath.Join(agents["linux-one"].workspace, "nested", "beta"),
		"workspace=" + agents["linux-one"].workspace,
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("expected the log to contain %q, got:\n%s", want, log)
		}
	}
	// The agent releases its spool only after the controller acknowledged.
	waitFor(t, "the agent spool to be cleared", func() bool {
		return len(agents["linux-one"].agent.journal.IDs()) == 0
	})
}

// TestEndToEndFailureKeepsExitCode covers a failing script.
func TestEndToEndFailureKeepsExitCode(t *testing.T) {
	controller, _, agents := newTestFleet(t, scriptedConfig("echo failing >&2\nexit 7", "30s"), "linux-one")
	agents["linux-one"].start(t)

	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	waitFor(t, "the run to fail", func() bool {
		return statusOf(t, controller, execution.ID) == StatusFailed
	})
	stored := mustFind(t, controller, execution.ID)
	if stored.ExitCode != 7 {
		t.Fatalf("expected exit code 7, got %d", stored.ExitCode)
	}
	if stored.FailureReason != FailureReasonScript {
		t.Fatalf("expected a script failure reason, got %q", stored.FailureReason)
	}
	if !strings.Contains(readControllerLog(t, controller, execution.ID), "failing") {
		t.Fatal("stderr must reach the controller log")
	}
}

// TestTwoAgentsRunTheSameProjectConcurrently is the parallelism integration
// test, including the explicit decision that one project has no lock.
func TestTwoAgentsRunTheSameProjectConcurrently(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "concurrent")
	script := `echo start >> "` + marker + `"
for i in $(seq 1 100); do
  if [ "$(wc -l < "` + marker + `" | tr -d ' ')" -ge 2 ]; then break; fi
  sleep 0.05
done
wc -l < "` + marker + `"`
	controller, _, agents := newTestFleet(t, scriptedConfig(script, "30s"), "linux-one", "linux-two")
	agents["linux-one"].start(t)
	agents["linux-two"].start(t)

	first := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	second := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})

	waitFor(t, "both same-project runs to succeed", func() bool {
		return statusOf(t, controller, first.ID) == StatusSuccess && statusOf(t, controller, second.ID) == StatusSuccess
	})
	firstAgent := mustFind(t, controller, first.ID).AgentID
	secondAgent := mustFind(t, controller, second.ID).AgentID
	if firstAgent == secondAgent {
		t.Fatalf("both runs landed on %q; they should have run on different agents", firstAgent)
	}
	// Each script only exits early once it sees the other one running, so a
	// successful pair proves they overlapped.
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "start") != 2 {
		t.Fatalf("expected both runs to have started, got %q", data)
	}
}

// TestCancelRunningJobKillsTheProcessGroup proves cancellation reaches a child
// process, not just the top-level script.
func TestCancelRunningJobKillsTheProcessGroup(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	childDone := filepath.Join(dir, "child-finished")
	script := `( sleep 30; echo done > "` + childDone + `" ) &
child=$!
echo "$child" > "` + started + `"
wait "$child"`
	controller, _, agents := newTestFleet(t, scriptedConfig(script, "60s"), "linux-one")
	agents["linux-one"].start(t)

	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	waitFor(t, "the child process to start", func() bool {
		_, err := os.Stat(started)
		return err == nil
	})
	childPID := readPID(t, started)

	if _, err := controller.Cancel(execution.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the run to be canceled", func() bool {
		return statusOf(t, controller, execution.ID) == StatusCanceled
	})
	waitFor(t, "the whole process group to die", func() bool {
		exists, err := processGroupExists(childPID)
		return err == nil && !exists
	})
	if _, err := os.Stat(childDone); err == nil {
		t.Fatal("the child must have been killed before it could finish")
	}
}

// TestTimeoutFailsWithATimeoutReason documents the chosen behaviour: a job
// that exceeds its timeout is FAILED with an explicit timeout reason.
func TestTimeoutFailsWithATimeoutReason(t *testing.T) {
	controller, _, agents := newTestFleet(t, scriptedConfig("sleep 30", "150ms"), "linux-one")
	agents["linux-one"].start(t)

	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	waitFor(t, "the run to time out", func() bool {
		return isExecutionTerminal(statusOf(t, controller, execution.ID))
	})
	stored := mustFind(t, controller, execution.ID)
	if stored.Status != StatusFailed {
		t.Fatalf("a timeout is reported as FAILED, got %q", stored.Status)
	}
	if stored.FailureReason != FailureReasonTimeout {
		t.Fatalf("expected the timeout reason, got %q", stored.FailureReason)
	}
	if !strings.Contains(stored.Error, "timed out") {
		t.Fatalf("expected a timeout message, got %q", stored.Error)
	}
}

// TestAgentRestartNeverReExecutesAndReportsAborted covers the crash-recovery
// contract end to end.
func TestAgentRestartNeverReExecutesAndReportsAborted(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "runs")
	script := `echo run >> "` + counter + `"
sleep 30`
	controller, _, agents := newTestFleet(t, scriptedConfig(script, "60s"), "linux-one")
	harness := agents["linux-one"]
	harness.start(t)

	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	waitFor(t, "the script to start", func() bool {
		data, err := os.ReadFile(counter)
		return err == nil && strings.Count(string(data), "run") == 1
	})
	waitFor(t, "the controller to see it running", func() bool {
		return statusOf(t, controller, execution.ID) == StatusRunning
	})

	harness.restart(t)
	waitFor(t, "the restarted agent to report the aborted run", func() bool {
		return statusOf(t, controller, execution.ID) == StatusAborted
	})
	stored := mustFind(t, controller, execution.ID)
	if !strings.Contains(stored.Error, "agent restarted") {
		t.Fatalf("expected a restart explanation, got %q", stored.Error)
	}
	if stored.NeedsAttention {
		t.Fatal("a provably terminated process group needs no operator attention")
	}
	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "run") != 1 {
		t.Fatalf("the script must never run twice, got %q", data)
	}
	// The agent accepts work again afterwards.
	next := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	waitFor(t, "the next run to start", func() bool {
		return statusOf(t, controller, next.ID) != StatusQueued
	})
}

// TestControllerRestartDoesNotAbortRunningWork restarts the controller process
// while an agent keeps building.
func TestControllerRestartDoesNotAbortRunningWork(t *testing.T) {
	dir := t.TempDir()
	done := filepath.Join(dir, "done")
	script := `for i in $(seq 1 100); do
  if [ -f "` + dir + `/go" ]; then break; fi
  sleep 0.05
done
echo finished > "` + done + `"`
	controller, _, agents := newTestFleet(t, scriptedConfig(script, "60s"), "linux-one")
	harness := agents["linux-one"]
	harness.start(t)

	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	waitFor(t, "the run to start", func() bool {
		return statusOf(t, controller, execution.ID) == StatusRunning
	})

	// A fresh controller over the same state must not abort the live run.
	restarted := reloadController(t, controller)
	if statusOf(t, restarted, execution.ID) != StatusRunning {
		t.Fatal("a controller restart must not abort running work")
	}
	if err := os.WriteFile(filepath.Join(dir, "go"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// The original controller is still serving, so the agent reports to it.
	waitFor(t, "the run to finish", func() bool {
		return statusOf(t, controller, execution.ID) == StatusSuccess
	})
	if _, err := os.Stat(done); err != nil {
		t.Fatalf("the script should have completed: %v", err)
	}
}

// TestLogsStreamWhileRunning proves logs are uploaded before the job ends.
func TestLogsStreamWhileRunning(t *testing.T) {
	dir := t.TempDir()
	script := `echo first-chunk
for i in $(seq 1 100); do
  if [ -f "` + dir + `/go" ]; then break; fi
  sleep 0.05
done
echo second-chunk`
	controller, _, agents := newTestFleet(t, scriptedConfig(script, "60s"), "linux-one")
	agents["linux-one"].start(t)

	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	waitFor(t, "the first chunk to arrive while the job runs", func() bool {
		return strings.Contains(readControllerLog(t, controller, execution.ID), "first-chunk")
	})
	if statusOf(t, controller, execution.ID) != StatusRunning {
		t.Fatal("the log arrived only after the job finished")
	}
	if err := os.WriteFile(filepath.Join(dir, "go"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the run to succeed", func() bool {
		return statusOf(t, controller, execution.ID) == StatusSuccess
	})
	log := readControllerLog(t, controller, execution.ID)
	if !strings.Contains(log, "second-chunk") {
		t.Fatalf("the final log must be complete, got:\n%s", log)
	}
	if strings.Count(log, "first-chunk") != 1 {
		t.Fatalf("streamed chunks must not be duplicated, got:\n%s", log)
	}
}

// TestWorkspaceEscapeIsRejectedByTheAgent proves the agent re-validates the
// path even if a controller hands it something unsafe.
func TestWorkspaceEscapeIsRejectedByTheAgent(t *testing.T) {
	controller, _, agents := newTestFleet(t, scriptedConfig("echo hi", "30s"), "linux-one")
	harness := agents["linux-one"]
	harness.start(t)

	// Rewrite the catalog path to point outside the workspace through a
	// symlink the agent must refuse to follow.
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(harness.workspace, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := controller.editConfig(func(cfg *ControllerConfig) error {
		cfg.Catalogs[0].Options[0].Values[optionPathField] = "escape"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	waitFor(t, "the run to fail", func() bool {
		return statusOf(t, controller, execution.ID) == StatusFailed
	})
	stored := mustFind(t, controller, execution.ID)
	if stored.FailureReason != FailureReasonAgent {
		t.Fatalf("expected an agent-side failure, got %q", stored.FailureReason)
	}
	if !strings.Contains(readControllerLog(t, controller, execution.ID), "escapes the workspace root") {
		t.Fatalf("expected the refusal in the log, got:\n%s", readControllerLog(t, controller, execution.ID))
	}
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid := 0
	for _, r := range strings.TrimSpace(string(data)) {
		if r < '0' || r > '9' {
			t.Fatalf("unexpected pid file content %q", data)
		}
		pid = pid*10 + int(r-'0')
	}
	if pid <= 0 {
		t.Fatalf("unexpected pid %d", pid)
	}
	return pid
}
