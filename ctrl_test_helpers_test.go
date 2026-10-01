package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testControllerConfig is the fixture used by most controller tests: two
// agents on different platforms, a shared project catalog, and two jobs.
const testControllerConfig = `role: controller
server:
  addresses: ["127.0.0.1:0"]
  state_dir: "state"
  max_history: 50
  heartbeat_interval: "20ms"
  offline_after: "2s"
  long_poll_timeout: "200ms"
catalogs:
  - id: "projects"
    options:
      - value: "alpha"
        label: "Alpha"
        labels: ["android", "ios"]
        values:
          path: "alpha"
      - value: "beta"
        label: "Beta"
        labels: ["android"]
        values:
          path: "nested/beta"
jobs:
  - id: "android-build"
    name: "Android build"
    labels: ["linux", "android"]
    timeout: "45m"
    workdir_param: "project"
    script: "echo android"
    parameters:
      - id: "project"
        type: "choice"
        required: true
        catalog: "projects"
        catalog_labels: ["android"]
      - id: "action"
        type: "choice"
        default: "debug"
        options:
          - value: "debug"
          - value: "release"
  - id: "ios-build"
    name: "iOS build"
    labels: ["macos", "ios"]
    timeout: "2h"
    workdir_param: "project"
    script: "echo ios"
    parameters:
      - id: "project"
        type: "choice"
        required: true
        catalog: "projects"
        catalog_labels: ["ios"]
      - id: "action"
        type: "choice"
        default: "ad-hoc"
        options:
          - value: "ad-hoc"
          - value: "app-store"
agents:
  - id: "linux-one"
    labels: ["linux", "android"]
  - id: "linux-two"
    labels: ["linux", "android"]
  - id: "mac-one"
    labels: ["macos", "ios"]
`

// newTestController writes a controller config into a temp directory and
// returns a live controller without starting any HTTP listener.
func newTestController(t *testing.T, document string) *Controller {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, controllerConfigName)
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadControllerConfig(path)
	if err != nil {
		t.Fatalf("load controller config: %v", err)
	}
	controller, err := newController(path, cfg, nil)
	if err != nil {
		t.Fatalf("new controller: %v", err)
	}
	t.Cleanup(controller.Close)
	return controller
}

// markOnline makes an agent look like it just polled so the scheduler
// considers it available.
func markOnline(controller *Controller, ids ...string) {
	for _, id := range ids {
		controller.markAgentSeen(id, "test")
	}
}

// newTestAPI returns a controller plus its HTTP surface.
func newTestAPI(t *testing.T, document string) (*ControllerAPI, *Controller) {
	t.Helper()
	controller := newTestController(t, document)
	return newControllerAPI(controller), controller
}

// adminSession creates a browser session without paying the cost of the
// password KDF. The real login path has its own tests.
func adminSession(t *testing.T, api *ControllerAPI) *Session {
	t.Helper()
	session, err := api.sessions.Create(adminUserName)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

// request issues an authenticated browser request with CSRF applied.
func request(t *testing.T, api *ControllerAPI, session *Session, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.ContentLength = int64(reader.Len())
	}
	if session != nil {
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.ID})
		req.Header.Set(csrfHeaderName, session.CSRF)
	}
	recorder := httptest.NewRecorder()
	api.routes().ServeHTTP(recorder, req)
	return recorder
}

// tokenRequest issues a request authenticated by a bearer token.
func tokenRequest(t *testing.T, api *ControllerAPI, token, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.ContentLength = int64(reader.Len())
	}
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	api.routes().ServeHTTP(recorder, req)
	return recorder
}

func decodeBody(t *testing.T, recorder *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(recorder.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
	}
}

func requireStatus(t *testing.T, recorder *httptest.ResponseRecorder, want int) {
	t.Helper()
	if recorder.Code != want {
		t.Fatalf("expected status %d, got %d: %s", want, recorder.Code, recorder.Body.String())
	}
}

// enqueue is a shorthand that fails the test when the enqueue is rejected.
func enqueue(t *testing.T, controller *Controller, jobID string, values map[string]string) *Execution {
	t.Helper()
	execution, err := controller.Enqueue(jobID, parameterValuesToQuery(values), "test")
	if err != nil {
		t.Fatalf("enqueue %s: %v", jobID, err)
	}
	return execution
}

// waitFor polls a condition with a short deadline.
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// statusOf reads the current status of an execution.
func statusOf(t *testing.T, controller *Controller, id string) string {
	t.Helper()
	execution, ok := controller.store.Find(id)
	if !ok {
		t.Fatalf("execution %s not found", id)
	}
	return execution.Status
}

// configDocumentWith replaces a substring of the fixture config.
func configDocumentWith(old, new string) string {
	return strings.Replace(testControllerConfig, old, new, 1)
}

// environMap turns a NAME=value slice into a map, keeping the last value so it
// reflects what the operating system would hand the process.
func environMap(environ []string) map[string]string {
	values := map[string]string{}
	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			values[name] = value
		}
	}
	return values
}

// finishExecution drives an assigned execution to a terminal status the way an
// agent would: obtain the start permit, then submit a result.
func finishExecution(t *testing.T, controller *Controller, id, status string) {
	t.Helper()
	execution, ok := controller.store.Find(id)
	if !ok {
		t.Fatalf("execution %s not found", id)
	}
	if execution.AgentID == "" {
		t.Fatalf("execution %s is not assigned", id)
	}
	if execution.Status == StatusAssigned {
		permit, err := controller.GrantPermit(execution.AgentID, id)
		if err != nil {
			t.Fatalf("grant permit: %v", err)
		}
		if !permit.Granted {
			t.Fatalf("permit was refused: %s", permit.Reason)
		}
	}
	response, err := controller.SubmitResult(execution.AgentID, AgentResultRequest{
		ExecutionID: id,
		Status:      status,
		ExitCode:    0,
		LogLength:   0,
	})
	if err != nil {
		t.Fatalf("submit result: %v", err)
	}
	if !response.Accepted {
		t.Fatalf("result was not accepted: %s", response.Reason)
	}
}

// mustFind returns an execution or fails the test.
func mustFind(t *testing.T, controller *Controller, id string) *Execution {
	t.Helper()
	execution, ok := controller.store.Find(id)
	if !ok {
		t.Fatalf("execution %s not found", id)
	}
	return execution
}

// reloadController builds a second controller over the same config and state
// directory, which is what a controller restart looks like.
func reloadController(t *testing.T, controller *Controller) *Controller {
	t.Helper()
	path := controller.Runtime().ConfigPath
	cfg, err := loadControllerConfig(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	next, err := newController(path, cfg, nil)
	if err != nil {
		t.Fatalf("reload controller: %v", err)
	}
	t.Cleanup(next.Close)
	return next
}

// requestWithCanceledContext issues an authenticated request whose client has
// already gone away, which is what an abandoned wait looks like.
func requestWithCanceledContext(t *testing.T, api *ControllerAPI, session *Session, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(method, path, nil).WithContext(ctx)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.ID})
	req.Header.Set(csrfHeaderName, session.CSRF)
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		api.routes().ServeHTTP(recorder, req)
		close(done)
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler did not return after the client disconnected")
	}
	return recorder
}
