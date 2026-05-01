package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadConfigValidatesTasks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(path, []byte(`
server:
  address: ":0"
tasks:
  - id: "one"
    command: "echo one"
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}
	if len(cfg.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(cfg.Tasks))
	}
	if cfg.Tasks[0].Name != "one" {
		t.Fatalf("expected missing name to default to id, got %q", cfg.Tasks[0].Name)
	}
}

func TestRunnerWritesCompletedLog(t *testing.T) {
	runner := NewRunner(t.TempDir())
	run, err := runner.Start(TaskConfig{
		ID:      "hello",
		Name:    "Hello",
		Command: "echo test-output",
		Timeout: "5s",
	})
	if err != nil {
		t.Fatal(err)
	}

	waitForRun(t, run)

	data, err := os.ReadFile(run.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "test-output") {
		t.Fatalf("expected log to contain command output, got:\n%s", data)
	}

	runs := runner.Snapshot()
	if len(runs) != 1 || runs[0].Status != StatusSuccess || runs[0].ExitCode != 0 {
		t.Fatalf("expected one successful completed run, got %#v", runs)
	}
	if runs[0].RequestedAt.IsZero() || runs[0].StartedAt.IsZero() || runs[0].FinishedAt.IsZero() {
		t.Fatalf("expected request, start, and finish times, got %#v", runs[0])
	}
}

func TestRunnerCancel(t *testing.T) {
	runner := NewRunner(t.TempDir())
	run, err := runner.Start(TaskConfig{
		ID:      "slow",
		Name:    "Slow",
		Command: "sleep 10",
		Timeout: "30s",
	})
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
		Command: "sleep 0.2; echo first",
		Timeout: "5s",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := runner.Start(TaskConfig{
		ID:      "second",
		Name:    "Second",
		Command: "echo second",
		Timeout: "5s",
	})
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
			Command:     "sleep 10",
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
			Command:     "echo resumed",
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

func waitForRun(t *testing.T, run *Run) {
	t.Helper()
	select {
	case <-run.done:
	case <-time.After(3 * time.Second):
		t.Fatal("run did not finish")
	}
}
