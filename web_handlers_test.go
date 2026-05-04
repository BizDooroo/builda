package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRunsPageRendersWorkspace(t *testing.T) {
	app := &App{
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
	if strings.Contains(body, `data-server-meta`) {
		t.Fatalf("expected runs page not to render server meta hook, got:\n%s", body)
	}
	if !strings.Contains(body, `id="run-picker"`) {
		t.Fatalf("expected runs page to render mobile picker, got:\n%s", body)
	}
	assertEmbeddedWebContains(t, "/api/state?task=")
	assertEmbeddedWebContains(t, "param-chip")
	assertEmbeddedWebContains(t, "log-param-line")
	assertEmbeddedWebContains(t, "duration ")
}

func TestRunPageRendersParamsHeaderHook(t *testing.T) {
	run := &Run{
		ID:       "run-with-inputs",
		TaskID:   "deploy",
		TaskName: "Deploy",
		Script:   "echo deploy",
		Inputs: map[string]string{
			"target": "prod",
		},
		Status:   StatusSuccess,
		ExitCode: 0,
	}
	runner := &Runner{
		byID: map[string]*Run{
			run.ID: run,
		},
	}
	app := &App{
		runner:   runner,
		hostname: "test-host",
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/runs/"+run.ID, nil)
	app.handleRunPage(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected run page to render, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, expected := range []string{
		`id="params"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected run page to include %q, got:\n%s", expected, body)
		}
	}
	assertEmbeddedWebContains(t, "param-list")
	assertEmbeddedWebContains(t, "param-chip")
	assertEmbeddedWebContains(t, "/api/runs/")

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/runs/missing", nil)
	app.handleRunPage(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected missing run page to 404, got %d", rec.Code)
	}
}

func TestIndexPageKeepsCollapsedTaskDescriptionOnOneLine(t *testing.T) {
	app := &App{
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
		"id=\"run-modal\"",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected index page to include %q, got:\n%s", expected, body)
		}
	}
	for _, expected := range []string{
		"text-overflow:ellipsis",
		"grid-template-columns:minmax(0,1fr) auto",
		"task-description-full",
		"document.execCommand",
		"window.isSecureContext",
		"/api/tasks/",
		"BUILDA_INPUT_",
	} {
		assertEmbeddedWebContains(t, expected)
	}
}
