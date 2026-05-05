package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigSaveValidatesBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	initial := []byte(`
tasks:
  - id: "one"
    name: "One"
    script: "echo one"
`)
	if err := os.WriteFile(path, initial, 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := parseConfig(initial)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Server.ConfigPassword = "secret"
	app := &App{
		cfg:        cfg,
		tasks:      buildTaskMap(cfg.Tasks),
		configPath: path,
	}

	invalid := `
tasks:
  - id: "one"
    script: "echo one"
  - id: "one"
    script: "echo duplicate"
`
	rec := httptest.NewRecorder()
	req := newConfigSaveRequest(invalid, "secret")
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
server:
  config_password: "secret"
tasks:
  - id: "two"
    name: "Two"
    script: "echo two"
    timeout: "5s"
`
	rec = httptest.NewRecorder()
	req = newConfigSaveRequest(valid, "wrong")
	app.handleConfig(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected wrong password to be rejected, got %d: %s", rec.Code, rec.Body.String())
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(initial) {
		t.Fatalf("expected rejected config not to be saved, got:\n%s", data)
	}

	rec = httptest.NewRecorder()
	req = newConfigSaveRequest(valid, "secret")
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

func TestConfigEndpointRequiresWebPassword(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := []byte(`
server:
  config_password: "secret"
tasks:
  - id: "one"
    script: "echo one"
`)
	if err := os.WriteFile(path, body, 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := parseConfig(body)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{
		cfg:        cfg,
		configPath: path,
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	app.handleConfig(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected missing password to be rejected, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/config", nil)
	req.Header.Set("X-Builda-Config-Password", "secret")
	app.handleConfig(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected correct password to load config, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestConfigPageHiddenWhenWebPasswordMissing(t *testing.T) {
	app := &App{
		cfg:      Config{},
		hostname: "test-host",
		started:  time.Unix(0, 0),
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	app.handleIndex(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected index page to render, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `data-config-link hidden`) {
		t.Fatalf("expected static config link to start hidden, got:\n%s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/config", nil)
	app.handleConfigPage(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected config page to be hidden without password, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/meta", nil)
	app.handleMeta(rec, req)
	var meta struct {
		ConfigEditingEnabled bool `json:"config_editing_enabled"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.ConfigEditingEnabled {
		t.Fatalf("expected meta to report disabled config editing")
	}

	app.cfg.Server.ConfigPassword = "secret"
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/config", nil)
	app.handleConfigPage(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected config page with password, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `type="password"`) {
		t.Fatalf("expected password input on config page, got:\n%s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/meta", nil)
	app.handleMeta(rec, req)
	meta = struct {
		ConfigEditingEnabled bool `json:"config_editing_enabled"`
	}{}
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	if !meta.ConfigEditingEnabled {
		t.Fatalf("expected meta to report enabled config editing")
	}
}

func TestMetaIncludesBuildVersionDetails(t *testing.T) {
	oldVersion, oldCommit, oldDate := version, commit, date
	t.Cleanup(func() {
		version, commit, date = oldVersion, oldCommit, oldDate
	})
	version = "v9.8.7"
	commit = "abcdef1234567890"
	date = "2026-05-05T00:00:00Z"

	app := &App{
		cfg:      Config{},
		hostname: "test-host",
		started:  time.Unix(0, 0),
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/meta", nil)
	app.handleMeta(rec, req)

	var meta struct {
		Version     string `json:"version"`
		Commit      string `json:"commit"`
		BuildDate   string `json:"build_date"`
		VersionInfo string `json:"version_info"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"v9.8.7", "abcdef1234567890", "2026-05-05T00:00:00Z"} {
		if !strings.Contains(meta.VersionInfo, want) {
			t.Fatalf("expected version_info to contain %q, got %#v", want, meta)
		}
	}
	if meta.Version != "v9.8.7" || meta.Commit != "abcdef1234567890" || meta.BuildDate != "2026-05-05T00:00:00Z" {
		t.Fatalf("unexpected build metadata: %#v", meta)
	}
}

func TestAppReloadConfigFromDiskUpdatesTaskMap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	initial := []byte(`
tasks:
  - id: "one"
    script: "echo one"
`)
	if err := os.WriteFile(path, initial, 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadRuntimeConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{
		cfg:        cfg,
		tasks:      buildTaskMap(cfg.Tasks),
		configPath: path,
	}
	if stamp, err := statFileStamp(path); err == nil {
		app.configFile = stamp
	}

	updated := []byte(`
tasks:
  - id: "two"
    name: "Two"
    script: "echo two"
`)
	time.Sleep(time.Millisecond)
	if err := os.WriteFile(path, updated, 0644); err != nil {
		t.Fatal(err)
	}
	if err := app.reloadConfigIfChanged(); err != nil {
		t.Fatalf("reloadConfigIfChanged returned error: %v", err)
	}
	if _, ok := app.tasks["two"]; !ok {
		t.Fatalf("expected reloaded task map to include updated task, got %#v", app.tasks)
	}
	if _, ok := app.tasks["one"]; ok {
		t.Fatalf("expected old task to be removed after reload, got %#v", app.tasks)
	}
}
