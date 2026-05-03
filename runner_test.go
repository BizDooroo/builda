package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunnerWritesCompletedLog(t *testing.T) {
	runner := NewRunner(t.TempDir())
	run, err := runner.Start(TaskConfig{
		ID:      "hello",
		Name:    "Hello",
		Script:  "echo test-output",
		Timeout: "5s",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	waitForRun(t, run)

	data, err := os.ReadFile(run.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "test-output") {
		t.Fatalf("expected log to contain script output, got:\n%s", data)
	}

	runs := runner.Snapshot()
	if len(runs) != 1 || runs[0].Status != StatusSuccess || runs[0].ExitCode != 0 {
		t.Fatalf("expected one successful completed run, got %#v", runs)
	}
	if runs[0].RequestedAt.IsZero() || runs[0].StartedAt.IsZero() || runs[0].FinishedAt.IsZero() {
		t.Fatalf("expected request, start, and finish times, got %#v", runs[0])
	}
}

func TestRunnerExecutesCommandAsBashScriptWithConfiguredHeader(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte("export BUILDA_FROM_BASHRC=loaded\n"), 0644); err != nil {
		t.Fatal(err)
	}

	runner := NewRunner(t.TempDir())
	run, err := runner.Start(TaskConfig{
		ID:           "bash",
		Name:         "Bash",
		Script:       `if [[ -n "$BASH_VERSION" ]]; then printf 'shell=bash bashrc=%s\n' "$BUILDA_FROM_BASHRC"; fi`,
		Timeout:      "5s",
		ScriptHeader: "#!/usr/bin/env bash\nsource \"$HOME/.bashrc\"",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	waitForRun(t, run)
	data, err := os.ReadFile(run.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "shell=bash bashrc=loaded") {
		t.Fatalf("expected Bash command to source ~/.bashrc, got:\n%s", data)
	}
}

func TestRunnerUsesConfiguredHeaderBeforeCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	toolDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(toolDir, 0755); err != nil {
		t.Fatal(err)
	}
	brewPath := filepath.Join(toolDir, "brew")
	if err := os.WriteFile(brewPath, []byte("#!/usr/bin/env bash\nprintf 'fake brew\\n'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte("export BUILDA_BREW_PATH=\"$(command -v brew)\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "/usr/bin:/bin")

	runner := NewRunner(t.TempDir())
	run, err := runner.Start(TaskConfig{
		ID:           "path",
		Name:         "Path",
		Script:       `printf 'brew=%s\n' "$BUILDA_BREW_PATH"`,
		Timeout:      "5s",
		ScriptHeader: "#!/usr/bin/env bash\nexport PATH=\"" + toolDir + ":$PATH\"\nsource \"$HOME/.bashrc\"",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	waitForRun(t, run)
	data, err := os.ReadFile(run.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "brew="+brewPath) {
		t.Fatalf("expected Bash startup to find prepended tool path, got:\n%s", data)
	}
}

func TestRunnerKeepsTaskSnapshot(t *testing.T) {
	runner := NewRunner(t.TempDir())
	task := TaskConfig{
		ID:      "snapshot",
		Name:    "Original name",
		Script:  "echo original-command",
		Timeout: "5s",
	}
	run, err := runner.Start(task, nil)
	if err != nil {
		t.Fatal(err)
	}
	task.Name = "Changed name"
	task.Script = "echo changed-command"

	waitForRun(t, run)

	runs := runner.Snapshot()
	if len(runs) != 1 {
		t.Fatalf("expected one run, got %#v", runs)
	}
	if runs[0].TaskName != "Original name" || runs[0].Script != "echo original-command" {
		t.Fatalf("expected run to preserve task snapshot, got %#v", runs[0])
	}
	if runs[0].TaskSnapshot.Name != "Original name" || runs[0].TaskSnapshot.Script != "echo original-command" {
		t.Fatalf("expected explicit task snapshot to be preserved, got %#v", runs[0].TaskSnapshot)
	}
}

func TestRunnerCancel(t *testing.T) {
	runner := NewRunner(t.TempDir())
	run, err := runner.Start(TaskConfig{
		ID:      "slow",
		Name:    "Slow",
		Script:  "sleep 10",
		Timeout: "30s",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !runner.Cancel(run.ID) {
		t.Fatal("expected cancel to find running task")
	}

	waitForRun(t, run)

	runs := runner.Snapshot()
	if len(runs) != 1 {
		t.Fatalf("expected one completed run, got %d", len(runs))
	}
	if runs[0].Status != StatusCanceled || runs[0].CanceledAt.IsZero() {
		t.Fatalf("expected run to be marked canceled, got %#v", runs[0])
	}
}

func TestRunnerQueuesOneRunAtATime(t *testing.T) {
	runner := NewRunner(t.TempDir())
	first, err := runner.Start(TaskConfig{
		ID:      "first",
		Name:    "First",
		Script:  "sleep 0.2; echo first",
		Timeout: "5s",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runner.Start(TaskConfig{
		ID:      "second",
		Name:    "Second",
		Script:  "echo second",
		Timeout: "5s",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	runs := runner.Snapshot()
	if len(runs) != 2 {
		t.Fatalf("expected two runs, got %#v", runs)
	}
	statuses := map[string]string{}
	for _, run := range runs {
		statuses[run.TaskID] = run.Status
	}
	if statuses["first"] != StatusRunning || statuses["second"] != StatusQueued {
		t.Fatalf("expected first running and second queued, got %#v", statuses)
	}

	waitForRun(t, first)
	waitForRun(t, second)

	runs = runner.Snapshot()
	statuses = map[string]string{}
	for _, run := range runs {
		statuses[run.TaskID] = run.Status
	}
	if statuses["first"] != StatusSuccess || statuses["second"] != StatusSuccess {
		t.Fatalf("expected both runs to succeed, got %#v", statuses)
	}
}

func TestRunnerPersistsStateAndAbortsInProgressOnRestart(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "runs.json")
	runs := []*Run{
		{
			ID:          "running-run",
			TaskID:      "old",
			TaskName:    "Old",
			Script:      "sleep 10",
			LogPath:     filepath.Join(dir, "running-run.log"),
			Status:      StatusRunning,
			RequestedAt: time.Now().Add(-2 * time.Minute),
			StartedAt:   time.Now().Add(-time.Minute),
			ExitCode:    -1,
		},
		{
			ID:          "queued-run",
			TaskID:      "next",
			TaskName:    "Next",
			Script:      "echo resumed",
			LogPath:     filepath.Join(dir, "queued-run.log"),
			Timeout:     5 * time.Second,
			TimeoutText: "5s",
			Status:      StatusQueued,
			RequestedAt: time.Now().Add(-time.Minute),
			ExitCode:    -1,
		},
	}
	data, err := json.Marshal(runs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, data, 0644); err != nil {
		t.Fatal(err)
	}

	runner := NewRunner(dir)
	queued := runner.byID["queued-run"]
	waitForRun(t, queued)

	snapshot := runner.Snapshot()
	statuses := map[string]string{}
	for _, run := range snapshot {
		statuses[run.ID] = run.Status
	}
	if statuses["running-run"] != StatusAborted {
		t.Fatalf("expected stale running run to be aborted, got %#v", snapshot)
	}
	if statuses["queued-run"] != StatusSuccess {
		t.Fatalf("expected queued run to resume, got %#v", snapshot)
	}

	persisted, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(persisted), StatusAborted) || !strings.Contains(string(persisted), StatusSuccess) {
		t.Fatalf("expected persisted terminal statuses, got %s", persisted)
	}
}
