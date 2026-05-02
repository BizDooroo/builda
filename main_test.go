package main

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
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
    description: "First task"
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
	if cfg.Tasks[0].Description != "First task" {
		t.Fatalf("expected description to be preserved, got %q", cfg.Tasks[0].Description)
	}
}

func TestParseConfigRejectsInvalidTimeout(t *testing.T) {
	_, err := parseConfig([]byte(`
tasks:
  - id: "bad"
    command: "echo bad"
    timeout: "later"
`))
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected timeout validation error, got %v", err)
	}
}

func TestConfigSaveValidatesBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	initial := []byte(`
tasks:
  - id: "one"
    name: "One"
    command: "echo one"
`)
	if err := os.WriteFile(path, initial, 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := parseConfig(initial)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{
		cfg:        cfg,
		tasks:      buildTaskMap(cfg.Tasks),
		configPath: path,
	}

	invalid := `
tasks:
  - id: "one"
    command: "echo one"
  - id: "one"
    command: "echo duplicate"
`
	rec := httptest.NewRecorder()
	req := newConfigSaveRequest(invalid)
	app.handleConfig(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid config to be rejected, got %d", rec.Code)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(initial) {
		t.Fatalf("expected invalid config not to be saved, got:\n%s", data)
	}

	valid := `
tasks:
  - id: "two"
    name: "Two"
    command: "echo two"
    timeout: "5s"
`
	rec = httptest.NewRecorder()
	req = newConfigSaveRequest(valid)
	app.handleConfig(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected valid config to be saved, got %d: %s", rec.Code, rec.Body.String())
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != valid {
		t.Fatalf("expected valid config to be saved, got:\n%s", data)
	}
	if _, ok := app.tasks["two"]; !ok {
		t.Fatalf("expected in-memory task map to be updated, got %#v", app.tasks)
	}
}

func TestTaskRunAPIStartsTask(t *testing.T) {
	dir := t.TempDir()
	task := TaskConfig{
		ID:      "hello task",
		Name:    "Hello Task",
		Command: "echo api-run",
		Timeout: "5s",
	}
	app := &App{
		cfg:    Config{Tasks: []TaskConfig{task}},
		tasks:  buildTaskMap([]TaskConfig{task}),
		runner: NewRunner(dir),
		logDir: dir,
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+url.PathEscape(task.ID)+"/run", nil)
	app.handleTaskAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected task run API to succeed, got %d: %s", rec.Code, rec.Body.String())
	}

	var run RunSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if run.TaskID != task.ID || run.TaskName != task.Name {
		t.Fatalf("expected task summary for %q, got %#v", task.ID, run)
	}

	active := app.runner.byID[run.ID]
	if active == nil {
		t.Fatalf("expected run %q to exist", run.ID)
	}
	waitForRun(t, active)
}

func TestStateAPIFiltersRunsByTaskQuery(t *testing.T) {
	dir := t.TempDir()
	first := TaskConfig{
		ID:      "first",
		Name:    "First",
		Command: "echo first",
		Timeout: "5s",
	}
	second := TaskConfig{
		ID:      "second task",
		Name:    "Second",
		Command: "echo second",
		Timeout: "5s",
	}
	app := &App{
		cfg:    Config{Tasks: []TaskConfig{first, second}},
		tasks:  buildTaskMap([]TaskConfig{first, second}),
		runner: NewRunner(dir),
		logDir: dir,
	}

	firstRun, err := app.runner.Start(first)
	if err != nil {
		t.Fatal(err)
	}
	secondRun, err := app.runner.Start(second)
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, firstRun)
	waitForRun(t, secondRun)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/state?task="+url.QueryEscape(second.ID), nil)
	app.handleState(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected state API to succeed, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Runs       []RunSummary `json:"runs"`
		TaskFilter string       `json:"task_filter"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.TaskFilter != second.ID {
		t.Fatalf("expected task filter %q, got %q", second.ID, payload.TaskFilter)
	}
	if len(payload.Runs) != 1 || payload.Runs[0].TaskID != second.ID {
		t.Fatalf("expected only runs for %q, got %#v", second.ID, payload.Runs)
	}
}

func TestRunsPageRendersWorkspaceWithHostname(t *testing.T) {
	app := &App{
		runsTmpl: template.Must(template.New("runs").Parse(runsPageTemplate)),
		logDir:   "logs",
		hostname: "test-host",
		started:  time.Unix(0, 0),
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/runs?task=hello", nil)
	app.handleRunsPage(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected runs page to render, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "host test-host") {
		t.Fatalf("expected hostname in header, got:\n%s", body)
	}
	if !strings.Contains(body, `const taskFilter = params.get("task") || "";`) {
		t.Fatalf("expected runs page to support task query filtering, got:\n%s", body)
	}
	if !strings.Contains(body, `id="run-picker"`) || !strings.Contains(body, `elapsed `) || !strings.Contains(body, `duration `) {
		t.Fatalf("expected runs page to render mobile picker and timing metadata, got:\n%s", body)
	}
}

func TestIndexPageKeepsCollapsedTaskDescriptionOnOneLine(t *testing.T) {
	app := &App{
		pageTmpl: template.Must(template.New("page").Parse(pageTemplate)),
		logDir:   "logs",
		hostname: "test-host",
		started:  time.Unix(0, 0),
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	app.handleIndex(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected index page to render, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, expected := range []string{
		"text-overflow: ellipsis",
		"grid-template-columns: minmax(0, 1fr) auto",
		"task-description-full",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected index page to include %q, got:\n%s", expected, body)
		}
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

func TestRunnerKeepsTaskSnapshot(t *testing.T) {
	runner := NewRunner(t.TempDir())
	task := TaskConfig{
		ID:      "snapshot",
		Name:    "Original name",
		Command: "echo original-command",
		Timeout: "5s",
	}
	run, err := runner.Start(task)
	if err != nil {
		t.Fatal(err)
	}
	task.Name = "Changed name"
	task.Command = "echo changed-command"

	waitForRun(t, run)

	runs := runner.Snapshot()
	if len(runs) != 1 {
		t.Fatalf("expected one run, got %#v", runs)
	}
	if runs[0].TaskName != "Original name" || runs[0].Command != "echo original-command" {
		t.Fatalf("expected run to preserve task snapshot, got %#v", runs[0])
	}
	if runs[0].TaskSnapshot.Name != "Original name" || runs[0].TaskSnapshot.Command != "echo original-command" {
		t.Fatalf("expected explicit task snapshot to be preserved, got %#v", runs[0].TaskSnapshot)
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

func newConfigSaveRequest(content string) *http.Request {
	body := url.Values{"content": {content}}.Encode()
	req := httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}
