package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestTaskRunAPIStartsTask(t *testing.T) {
	dir := t.TempDir()
	task := TaskConfig{
		ID:      "hello task",
		Name:    "Hello Task",
		Script:  "echo api-run",
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

func TestTaskRunAPIPassesQueryInputsAsEnvironment(t *testing.T) {
	dir := t.TempDir()
	task := TaskConfig{
		ID:      "deploy",
		Name:    "Deploy",
		Script:  `printf 'target=%s message=%s\n' "$BUILDA_INPUT_TARGET" "$BUILDA_INPUT_MESSAGE"`,
		Timeout: "5s",
		Inputs: []TaskInputConfig{
			{
				ID:       "target",
				Name:     "Target",
				Type:     "choice",
				Required: true,
				Options:  []string{"staging", "prod"},
			},
			{
				ID:      "message",
				Name:    "Message",
				Type:    "string",
				Default: "default-message",
			},
		},
	}
	app := &App{
		cfg:    Config{Tasks: []TaskConfig{task}},
		tasks:  buildTaskMap([]TaskConfig{task}),
		runner: NewRunner(dir),
		logDir: dir,
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/deploy/run?target=prod&message=ship", nil)
	app.handleTaskAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected task run API to succeed, got %d: %s", rec.Code, rec.Body.String())
	}

	var run RunSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if run.Inputs["target"] != "prod" || run.Inputs["message"] != "ship" {
		t.Fatalf("expected run inputs from query, got %#v", run.Inputs)
	}

	active := app.runner.byID[run.ID]
	if active == nil {
		t.Fatalf("expected run %q to exist", run.ID)
	}
	waitForRun(t, active)
	data, err := os.ReadFile(active.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "target=prod message=ship") {
		t.Fatalf("expected log to contain input environment values, got:\n%s", data)
	}
	if !strings.Contains(string(data), `params   {"message":"ship","target":"prod"}`) {
		t.Fatalf("expected log to contain run input parameters, got:\n%s", data)
	}
}

func TestTaskRunAPIWaitsAndReturnsRunLog(t *testing.T) {
	dir := t.TempDir()
	task := TaskConfig{
		ID:      "hello",
		Name:    "Hello",
		Script:  "sleep 0.1; echo waited",
		Timeout: "5s",
	}
	app := &App{
		cfg:    Config{Tasks: []TaskConfig{task}},
		tasks:  buildTaskMap([]TaskConfig{task}),
		runner: NewRunner(dir),
		logDir: dir,
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/hello/run?wait=1", nil)
	app.handleTaskAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected wait task run API to succeed, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload struct {
		RunSummary
		Log string `json:"log"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.TaskID != task.ID || payload.Status != StatusSuccess {
		t.Fatalf("expected completed run summary, got %#v", payload.RunSummary)
	}
	if !strings.Contains(payload.Log, "stdout   waited") {
		t.Fatalf("expected response log to include script output, got:\n%s", payload.Log)
	}
}

func TestRunAPIDeletesCompletedRunHistory(t *testing.T) {
	dir := t.TempDir()
	task := TaskConfig{
		ID:      "hello",
		Name:    "Hello",
		Script:  "echo delete-me",
		Timeout: "5s",
	}
	app := &App{
		cfg:    Config{Tasks: []TaskConfig{task}},
		tasks:  buildTaskMap([]TaskConfig{task}),
		runner: NewRunner(dir),
		logDir: dir,
	}
	run, err := app.runner.Start(task, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, run)
	if _, err := os.Stat(run.LogPath); err != nil {
		t.Fatalf("expected log before delete, got %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/runs/"+run.ID, nil)
	app.handleRunAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected delete run API to succeed, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(run.LogPath); !os.IsNotExist(err) {
		t.Fatalf("expected log to be removed, got %v", err)
	}
	if found, ok := app.runner.Find(run.ID); ok {
		t.Fatalf("expected run history to be removed, got %#v", found)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/runs/"+run.ID, nil)
	app.handleRunAPI(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected deleted run API to return 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRunAPIRejectsActiveRunDelete(t *testing.T) {
	dir := t.TempDir()
	task := TaskConfig{
		ID:      "slow",
		Name:    "Slow",
		Script:  "sleep 10",
		Timeout: "30s",
	}
	app := &App{
		cfg:    Config{Tasks: []TaskConfig{task}},
		tasks:  buildTaskMap([]TaskConfig{task}),
		runner: NewRunner(dir),
		logDir: dir,
	}
	run, err := app.runner.Start(task, nil)
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/runs/"+run.ID, nil)
	app.handleRunAPI(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected active run delete to conflict, got %d: %s", rec.Code, rec.Body.String())
	}
	if !app.runner.Cancel(run.ID) {
		t.Fatal("expected cancel to clean up active run")
	}
	waitForRun(t, run)
}

func TestRunLogAPIRejectsDeleteMethod(t *testing.T) {
	app := &App{runner: NewRunner(t.TempDir())}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/runs/missing/log", nil)
	app.handleRunAPI(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected run log delete API to be disabled, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestTaskRunAPIRejectsInvalidWaitParam(t *testing.T) {
	dir := t.TempDir()
	task := TaskConfig{
		ID:      "hello",
		Name:    "Hello",
		Script:  "echo hello",
		Timeout: "5s",
	}
	app := &App{
		cfg:    Config{Tasks: []TaskConfig{task}},
		tasks:  buildTaskMap([]TaskConfig{task}),
		runner: NewRunner(dir),
		logDir: dir,
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/hello/run?wait=later", nil)
	app.handleTaskAPI(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid wait to be rejected, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestTaskRunAPIRejectsInvalidQueryInput(t *testing.T) {
	dir := t.TempDir()
	task := TaskConfig{
		ID:      "deploy",
		Name:    "Deploy",
		Script:  "echo deploy",
		Timeout: "5s",
		Inputs: []TaskInputConfig{
			{
				ID:       "target",
				Type:     "choice",
				Required: true,
				Options:  []string{"staging", "prod"},
			},
		},
	}
	app := &App{
		cfg:    Config{Tasks: []TaskConfig{task}},
		tasks:  buildTaskMap([]TaskConfig{task}),
		runner: NewRunner(dir),
		logDir: dir,
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/deploy/run?target=dev", nil)
	app.handleTaskAPI(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid choice to be rejected, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/tasks/deploy/run?target=prod&extra=value", nil)
	app.handleTaskAPI(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected unknown input to be rejected, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestStateAPIFiltersRunsByTaskQuery(t *testing.T) {
	dir := t.TempDir()
	first := TaskConfig{
		ID:      "first",
		Name:    "First",
		Script:  "echo first",
		Timeout: "5s",
	}
	second := TaskConfig{
		ID:      "second task",
		Name:    "Second",
		Script:  "echo second",
		Timeout: "5s",
	}
	app := &App{
		cfg:    Config{Tasks: []TaskConfig{first, second}},
		tasks:  buildTaskMap([]TaskConfig{first, second}),
		runner: NewRunner(dir),
		logDir: dir,
	}

	firstRun, err := app.runner.Start(first, nil)
	if err != nil {
		t.Fatal(err)
	}
	secondRun, err := app.runner.Start(second, nil)
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
