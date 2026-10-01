package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

// TestAgentScriptHeaderIsAppliedAtExecution proves the shell header is taken
// from the agent host at execution time, so platform startup such as a PATH
// export or a profile stays local to the machine that runs the build.
func TestAgentScriptHeaderIsAppliedAtExecution(t *testing.T) {
	controller, _, agents := newTestFleet(t, scriptedConfig("echo \"header=$AGENT_LOCAL_MARKER\"", "30s"), "linux-one")
	harness := agents["linux-one"]
	harness.runtime.ScriptHeader = "#!/usr/bin/env bash\nset -euo pipefail\nexport AGENT_LOCAL_MARKER=from-this-host"
	harness.rebuild(t)
	harness.start(t)

	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	waitFor(t, "the run to succeed", func() bool {
		return statusOf(t, controller, execution.ID) == StatusSuccess
	})
	log := readControllerLog(t, controller, execution.ID)
	if !strings.Contains(log, "header=from-this-host") {
		t.Fatalf("the agent-local header must be prepended at execution, got:\n%s", log)
	}
	// The header itself is a host detail: the controller stores the job script
	// it sent, never the agent's shell startup.
	stored := mustFind(t, controller, execution.ID)
	if strings.Contains(stored.Script, "from-this-host") || strings.Contains(stored.JobSnapshot.Script, "from-this-host") {
		t.Fatal("the agent shell header must never reach the controller")
	}
}

// TestPausingAnAgentDoesNotDisturbItsRunningJob pins the pause semantics:
// pause only stops new assignments.
func TestPausingAnAgentDoesNotDisturbItsRunningJob(t *testing.T) {
	dir := t.TempDir()
	script := `for i in $(seq 1 200); do
  if [ -f "` + dir + `/go" ]; then break; fi
  sleep 0.05
done
echo released`
	controller, _, agents := newTestFleet(t, scriptedConfig(script, "60s"), "linux-one")
	agents["linux-one"].start(t)

	running := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	waitFor(t, "the run to start", func() bool {
		return statusOf(t, controller, running.ID) == StatusRunning
	})

	if err := controller.editConfig(func(cfg *ControllerConfig) error {
		for index := range cfg.Agents {
			if cfg.Agents[index].ID == "linux-one" {
				cfg.Agents[index].Paused = true
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// A new execution waits, with the pause named as the reason.
	queued := enqueue(t, controller, "android-build", map[string]string{"project": "beta"})
	if statusOf(t, controller, queued.ID) != StatusQueued {
		t.Fatal("a paused agent must not receive new work")
	}
	if reason := queueReason(controller.AgentViews(), queued.Labels); reason != QueueReasonPaused {
		t.Fatalf("expected the paused reason, got %q", reason)
	}
	// The job already running is untouched and completes normally.
	if statusOf(t, controller, running.ID) != StatusRunning {
		t.Fatal("pausing an agent must not disturb the job it is already running")
	}
	if err := os.WriteFile(filepath.Join(dir, "go"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the running job to finish", func() bool {
		return statusOf(t, controller, running.ID) == StatusSuccess
	})
	if !strings.Contains(readControllerLog(t, controller, running.ID), "released") {
		t.Fatal("the paused agent must still have completed its job")
	}
	if statusOf(t, controller, queued.ID) != StatusQueued {
		t.Fatal("the queued execution must still be waiting on the pause")
	}
}

// TestAgentShutdownDoesNotWaitForALongBuild proves a stopping agent returns
// promptly instead of blocking until the script finishes. The run is left for
// the next start to reconcile, which is what the recovery path expects.
func TestAgentShutdownDoesNotWaitForALongBuild(t *testing.T) {
	controller, _, agents := newTestFleet(t, scriptedConfig("sleep 120", "10m"), "linux-one")
	harness := agents["linux-one"]
	harness.start(t)

	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	waitFor(t, "the agent to launch the script", func() bool {
		if statusOf(t, controller, execution.ID) != StatusRunning {
			return false
		}
		entry, err := harness.agent.journal.Load(execution.ID)
		return err == nil && entry.Phase == journalStarted && entry.PGID > 0
	})

	started := time.Now()
	harness.stop()
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("the agent took %s to stop; it must not wait for the build", elapsed)
	}
	// The controller still sees the run as live: nothing was invented.
	if statusOf(t, controller, execution.ID) != StatusRunning {
		t.Fatal("a stopping agent must not report an outcome it does not know")
	}
	// The journal still records the started phase, so the next start can
	// reconcile it.
	entries, err := harness.agent.journal.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Phase != journalStarted {
		phases := make([]string, 0, len(entries))
		for _, entry := range entries {
			phases = append(phases, entry.ExecutionID+"="+entry.Phase)
		}
		t.Fatalf("expected one started journal entry, got %v", phases)
	}

	// Restarting proves the process group is cleaned up and reported once.
	harness.restart(t)
	waitFor(t, "the restarted agent to report the aborted run", func() bool {
		return statusOf(t, controller, execution.ID) == StatusAborted
	})
	waitFor(t, "the process group to be gone", func() bool {
		exists, err := processGroupExists(entries[0].PGID)
		return err == nil && !exists
	})
}
