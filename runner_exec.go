package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

func taskScriptContent(header, script string) string {
	header = strings.TrimRight(header, "\r\n")
	if strings.TrimSpace(header) == "" {
		header = defaultScriptHeader
	}
	return header + "\n\n" + script + "\n"
}

func writeTaskScript(dir, runID, header, script string) (string, error) {
	file, err := os.CreateTemp(dir, runID+"-*.sh")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if _, err := file.WriteString(taskScriptContent(header, script)); err != nil {
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
	script := run.Script
	task := run.TaskSnapshot
	if task.ID == "" {
		task = TaskConfig{
			ID:      run.TaskID,
			Name:    run.TaskName,
			Script:  run.Script,
			Timeout: run.TimeoutText,
		}
	}
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
	writeLog(logWriter, "script", script)
	if len(inputs) > 0 {
		writeLog(logWriter, "params", formatInputLog(inputs))
	}

	scriptPath, err := writeTaskScript(r.logDir, id, task.ScriptHeader, script)
	if err != nil {
		r.finish(id, false, -1, err.Error())
		return
	}
	defer os.Remove(scriptPath)

	cmd := exec.CommandContext(ctx, scriptPath)
	cmd.Env = taskEnvironment(inputs)
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

	// Drain both pipes to EOF before reaping: cmd.Wait closes them, so
	// waiting first would truncate the tail of the log.
	copyWG.Wait()
	waitErr := cmd.Wait()
	close(processDone)

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
	// Clean up the generated script before releasing waiters so the log
	// directory is settled by the time the run reports as finished.
	_ = os.Remove(scriptPath)
	r.finish(id, canceled, exitCode, errText)
}

// maxLogLineBytes bounds one rendered log line. A longer line is split across
// several, which keeps minified bundles and base64 blobs readable.
const maxLogLineBytes = 64 << 10

// copyPrefixed drains a pipe to EOF, writing one prefixed log line per source
// line. It must never stop early: whoever stops reading leaves the child
// blocked on a full pipe buffer, which deadlocks the run. A line longer than
// maxLogLineBytes is therefore split rather than treated as an error.
func copyPrefixed(wg *sync.WaitGroup, writer io.Writer, label string, reader io.Reader) {
	defer wg.Done()
	buffered := bufio.NewReaderSize(reader, 64<<10)
	var line []byte
	for {
		chunk, isPrefix, err := buffered.ReadLine()
		if len(chunk) > 0 {
			line = append(line, chunk...)
			for len(line) >= maxLogLineBytes {
				writeLog(writer, label, string(line[:maxLogLineBytes]))
				line = line[maxLogLineBytes:]
			}
		}
		if isPrefix {
			continue
		}
		if len(line) > 0 || err == nil {
			writeLog(writer, label, string(line))
			line = line[:0]
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				writeLog(writer, label, "read error: "+err.Error())
			}
			// Keep draining until EOF so the child never blocks on a full
			// pipe, even after a transient read error.
			if errors.Is(err, io.EOF) {
				return
			}
			if _, drainErr := io.Copy(io.Discard, buffered); drainErr != nil {
				return
			}
			return
		}
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
