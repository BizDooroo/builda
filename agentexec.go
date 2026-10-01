package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// execOutcome is the local result of running one job script.
type execOutcome struct {
	Status        string
	ExitCode      int
	Error         string
	FailureReason string
}

// buildExecutionEnv assembles the process environment. Inherited BUILDA_*
// variables are dropped so a parent process cannot spoof parameters or
// execution identity, and no parameter value is ever interpolated into the
// script text, so a value cannot introduce shell substitution.
func buildExecutionEnv(runtime AgentRuntime, assignment AgentAssignment) []string {
	values := map[string]string{
		"BUILDA_AGENT_ID":       runtime.ID,
		"BUILDA_CONTROLLER_URL": runtime.ControllerURL,
		"BUILDA_EXECUTION_ID":   assignment.ExecutionID,
		"BUILDA_JOB_ID":         assignment.JobID,
		"BUILDA_JOB_NAME":       assignment.JobName,
		"BUILDA_WORKSPACE":      runtime.WorkspaceRoot,
	}
	for name, value := range assignment.Env {
		if isReservedEnvName(name) {
			continue
		}
		values[name] = value
	}
	return append(sanitizeInheritedEnv(os.Environ()), sortedEnv(values)...)
}

// resolveExecutionWorkdir validates the selected project path against the
// agent's workspace root, rejecting traversal, absolute paths, and symlinks
// that leave the root.
func resolveExecutionWorkdir(runtime AgentRuntime, assignment AgentAssignment) (string, error) {
	if assignment.WorkdirPath == "" {
		return runtime.WorkspaceRoot, nil
	}
	return resolveWorkspacePath(runtime.WorkspaceRoot, assignment.WorkdirPath)
}

// writeExecutionScript materializes the job script with the agent-local shell
// header snapshotted at execution time.
func writeExecutionScript(dir, header, script string) (string, error) {
	path := filepath.Join(dir, "script.sh")
	if err := os.WriteFile(path, []byte(taskScriptContent(header, script)), 0o700); err != nil {
		return "", err
	}
	return path, nil
}

func agentFailure(err error) execOutcome {
	return execOutcome{Status: StatusFailed, ExitCode: -1, Error: err.Error(), FailureReason: FailureReasonAgent}
}

// runExecution launches the script in its own process group, streams output to
// the agent-local log first, and enforces the job timeout. A canceled
// execution kills the whole process group.
func (a *Agent) runExecution(ctx context.Context, entry *journalEntry, cancel <-chan struct{}) execOutcome {
	assignment := entry.Assignment
	logFile, err := os.OpenFile(a.journal.LogPath(assignment.ExecutionID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return agentFailure(err)
	}
	defer logFile.Close()
	writer := &lockedWriter{w: logFile}

	workdir, err := resolveExecutionWorkdir(a.runtime, assignment)
	if err != nil {
		writeLog(writer, "error", err.Error())
		_ = logFile.Sync()
		return agentFailure(err)
	}
	scriptPath, err := writeExecutionScript(a.journal.entryDir(assignment.ExecutionID), a.runtime.ScriptHeader, assignment.Script)
	if err != nil {
		writeLog(writer, "error", err.Error())
		_ = logFile.Sync()
		return agentFailure(err)
	}

	writeLog(writer, "agent", a.runtime.ID)
	writeLog(writer, "job", assignment.JobName)
	writeLog(writer, "workdir", workdir)
	if len(assignment.Parameters) > 0 {
		writeLog(writer, "params", formatInputLog(assignment.Parameters))
	}
	writeLog(writer, "started", time.Now().Format(displayTimeLayout))

	cmd := exec.Command(scriptPath)
	cmd.Dir = workdir
	cmd.Env = buildExecutionEnv(a.runtime, assignment)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return agentFailure(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return agentFailure(err)
	}

	// Record the intent to start before the process exists. A crash in this
	// window leaves an execution whose fate cannot be proven, which is
	// reported for operator attention rather than silently retried.
	entry.Phase = journalStarted
	entry.StartedAt = time.Now()
	if err := a.journal.Save(entry); err != nil {
		return agentFailure(err)
	}
	if err := cmd.Start(); err != nil {
		writeLog(writer, "error", err.Error())
		_ = logFile.Sync()
		return agentFailure(err)
	}
	entry.PID = cmd.Process.Pid
	entry.PGID = cmd.Process.Pid
	if token, tokenErr := processStartToken(cmd.Process.Pid); tokenErr == nil {
		entry.ProcessToken = token
	}
	if err := a.journal.Save(entry); err != nil {
		_ = killProcessGroup(entry.PGID, syscall.SIGKILL)
		return agentFailure(err)
	}

	outcome := a.superviseExecution(ctx, cmd, entry, writer, stdout, stderr, cancel)
	writeLog(writer, "finished", time.Now().Format(displayTimeLayout))
	writeLog(writer, "result", fmt.Sprintf("%s exit=%d %s", outcome.Status, outcome.ExitCode, outcome.Error))
	if syncErr := logFile.Sync(); syncErr != nil {
		outcome.Error = joinMessages(outcome.Error, syncErr.Error())
	}
	return outcome
}

// superviseExecution streams output, applies the timeout, and honors a
// cancellation request by killing the whole process group.
func (a *Agent) superviseExecution(ctx context.Context, cmd *exec.Cmd, entry *journalEntry, writer *lockedWriter, stdout, stderr io.Reader, cancel <-chan struct{}) execOutcome {
	timeout := time.Duration(0)
	if entry.Assignment.TimeoutText != "" {
		if parsed, err := time.ParseDuration(entry.Assignment.TimeoutText); err == nil {
			timeout = parsed
		}
	}
	var timeoutCh <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		timeoutCh = timer.C
	}

	var timedOut, canceled atomicFlag
	done := make(chan struct{})
	go func() {
		select {
		case <-timeoutCh:
			timedOut.set()
		case <-cancel:
			canceled.set()
		case <-ctx.Done():
			// The agent is shutting down. The script keeps running in its own
			// process group and is reconciled on the next agent start.
			return
		case <-done:
			return
		}
		_ = killProcessGroup(entry.PGID, syscall.SIGKILL)
	}()

	var copyWG sync.WaitGroup
	copyWG.Add(2)
	go copyPrefixed(&copyWG, writer, "stdout", stdout)
	go copyPrefixed(&copyWG, writer, "stderr", stderr)

	// Drain both pipes to EOF before reaping: cmd.Wait closes them, so
	// waiting first would truncate the tail of the log.
	copyWG.Wait()
	waitErr := cmd.Wait()
	close(done)

	outcome := execOutcome{ExitCode: -1}
	if cmd.ProcessState != nil {
		outcome.ExitCode = cmd.ProcessState.ExitCode()
	}
	switch {
	case timedOut.get():
		outcome.Status = StatusFailed
		outcome.FailureReason = FailureReasonTimeout
		outcome.Error = "job timed out after " + entry.Assignment.TimeoutText
	case canceled.get():
		outcome.Status = StatusCanceled
		outcome.Error = "canceled by the controller"
	case waitErr == nil && outcome.ExitCode == 0:
		outcome.Status = StatusSuccess
	default:
		outcome.Status = StatusFailed
		outcome.FailureReason = FailureReasonScript
		if waitErr != nil {
			outcome.Error = waitErr.Error()
		}
	}
	return outcome
}

func joinMessages(values ...string) string {
	joined := ""
	for _, value := range values {
		if value == "" {
			continue
		}
		if joined != "" {
			joined += "; "
		}
		joined += value
	}
	return joined
}

// atomicFlag is a one-way boolean usable from several goroutines.
type atomicFlag struct {
	mu    sync.Mutex
	value bool
}

func (f *atomicFlag) set() {
	f.mu.Lock()
	f.value = true
	f.mu.Unlock()
}

func (f *atomicFlag) get() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.value
}
