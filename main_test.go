package main

import (
	"bytes"
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

func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "builda-test-home-")
	if err == nil {
		_ = os.Setenv("HOME", home)
	}
	code := m.Run()
	if err == nil {
		_ = os.RemoveAll(home)
	}
	os.Exit(code)
}

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

func TestParseConfigValidatesTaskInputs(t *testing.T) {
	cfg, err := parseConfig([]byte(`
tasks:
  - id: "deploy"
    command: "echo deploy"
    inputs:
      - id: "target"
        name: "Target"
        type: "choice"
        required: true
        default: "staging"
        options:
          - "staging"
          - "prod"
      - id: "message"
        type: "input"
        default: "hello"
`))
	if err != nil {
		t.Fatalf("parseConfig returned error: %v", err)
	}
	if got := cfg.Tasks[0].Inputs[1].Type; got != "string" {
		t.Fatalf("expected input alias to normalize to string, got %q", got)
	}
	if got := cfg.Tasks[0].Inputs[1].Name; got != "message" {
		t.Fatalf("expected missing input name to default to id, got %q", got)
	}
}

func TestParseConfigRejectsInvalidTaskInputs(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "invalid type",
			body: `
tasks:
  - id: "bad"
    command: "echo bad"
    inputs:
      - id: "env"
        type: "boolean"
`,
			want: "type",
		},
		{
			name: "missing choice options",
			body: `
tasks:
  - id: "bad"
    command: "echo bad"
    inputs:
      - id: "env"
        type: "choice"
`,
			want: "options",
		},
		{
			name: "choice default outside options",
			body: `
tasks:
  - id: "bad"
    command: "echo bad"
    inputs:
      - id: "env"
        type: "choice"
        default: "prod"
        options: ["staging"]
`,
			want: "default",
		},
		{
			name: "environment conflict",
			body: `
tasks:
  - id: "bad"
    command: "echo bad"
    inputs:
      - id: "target-env"
      - id: "target_env"
`,
			want: "conflicts",
		},
		{
			name: "reserved wait input",
			body: `
tasks:
  - id: "bad"
    command: "echo bad"
    inputs:
      - id: "wait"
`,
			want: "reserved",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseConfig([]byte(tt.body))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected %q validation error, got %v", tt.want, err)
			}
		})
	}
}

func TestSampleConfigIsValid(t *testing.T) {
	cfg, err := parseConfig([]byte(sampleConfig))
	if err != nil {
		t.Fatalf("sample config must be valid: %v", err)
	}
	if len(cfg.Tasks) == 0 {
		t.Fatal("sample config should include at least one task")
	}
}

func TestHelpTextDocumentsConfigAuthoring(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "builda", "config.yaml")
	help := helpText(configPath)
	for _, want := range []string{
		"Configuration guide",
		configPath,
		"Complete config.yaml example:",
		"server.address",
		"server.log_dir",
		"server.config_password",
		"tasks[].id",
		"tasks[].command",
		"Required Bash script body",
		"tasks[].inputs[].type",
		"BUILDA_INPUT_TARGET_ENV",
		"curl -X POST \"http://localhost:28088/api/tasks/hello/run?name=Builda&environment=local\"",
		"Builda is internal-only software",
		"builda sample-config",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("expected help text to include %q, got:\n%s", want, help)
		}
	}

	example := extractHelpConfigExample(t, help)
	if _, err := parseConfig([]byte(example)); err != nil {
		t.Fatalf("help config example must be valid: %v\n%s", err, example)
	}
}

func extractHelpConfigExample(t *testing.T, help string) string {
	t.Helper()
	start := strings.Index(help, "  server:\n")
	end := strings.Index(help, "\nField reference:")
	if start < 0 || end < 0 || end <= start {
		t.Fatalf("could not find config example in help text:\n%s", help)
	}
	lines := strings.Split(help[start:end], "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "  ") {
			lines[i] = strings.TrimPrefix(line, "  ")
		}
	}
	return strings.Join(lines, "\n")
}

func TestDefaultConfigPathUsesUserConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	want := filepath.Join(dir, "builda", "config.yaml")
	if got := defaultConfigPath(); got != want {
		t.Fatalf("defaultConfigPath() = %q, want %q", got, want)
	}
}

func TestEnsureDefaultConfigCreatesSample(t *testing.T) {
	path := filepath.Join(t.TempDir(), "builda", "config.yaml")
	if err := ensureDefaultConfig(path); err != nil {
		t.Fatalf("ensureDefaultConfig returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != sampleConfig {
		t.Fatalf("expected sample config, got:\n%s", data)
	}
}

func TestResolveLogDirUsesConfigDirectory(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "builda", "config.yaml")
	want := filepath.Join(filepath.Dir(configPath), "logs")
	if got := resolveLogDir(configPath, "logs"); got != want {
		t.Fatalf("resolveLogDir() = %q, want %q", got, want)
	}
}

func TestResolveLogDirKeepsAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	if got := resolveLogDir(filepath.Join(t.TempDir(), "config.yaml"), dir); got != dir {
		t.Fatalf("resolveLogDir() = %q, want %q", got, dir)
	}
}

func TestVersionInfoIncludesInjectedVersion(t *testing.T) {
	oldVersion, oldCommit, oldDate := version, commit, date
	t.Cleanup(func() {
		version, commit, date = oldVersion, oldCommit, oldDate
	})

	version = "v1.2.3"
	commit = "1234567890abcdef"
	date = "2026-05-02T00:00:00Z"

	got := versionInfo()
	for _, want := range []string{"builda v1.2.3", "commit 1234567890ab", "built 2026-05-02T00:00:00Z"} {
		if !strings.Contains(got, want) {
			t.Fatalf("versionInfo() = %q, want %q", got, want)
		}
	}
}

func TestRootCommandVersion(t *testing.T) {
	var out bytes.Buffer
	cmd := newRootCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"version"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("version command returned error: %v", err)
	}
	if !strings.Contains(out.String(), "builda ") {
		t.Fatalf("expected version output, got %q", out.String())
	}
}

func TestRootCommandSampleConfig(t *testing.T) {
	var out bytes.Buffer
	cmd := newRootCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"sample-config"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("sample-config command returned error: %v", err)
	}
	if out.String() != sampleConfig {
		t.Fatalf("expected sample config, got:\n%s", out.String())
	}
}

func TestConfigPathCommandPrintsAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	relativePath := filepath.Join(dir, "nested", "config.yaml")

	var out bytes.Buffer
	cmd := newRootCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--config", relativePath, "config", "path"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config path command returned error: %v", err)
	}
	want, err := filepath.Abs(relativePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != want {
		t.Fatalf("expected config path %q, got %q", want, out.String())
	}
}

func TestConfigGetCreatesAndPrintsDefaultConfig(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	wantPath := filepath.Join(configHome, "builda", "config.yaml")

	var out bytes.Buffer
	cmd := newRootCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"config", "get"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config get command returned error: %v", err)
	}
	if out.String() != sampleConfig {
		t.Fatalf("expected sample config output, got:\n%s", out.String())
	}
	data, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != sampleConfig {
		t.Fatalf("expected default config file to be created, got:\n%s", data)
	}
}

func TestConfigSetValidatesAndWritesFromStdin(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "builda", "config.yaml")
	valid := []byte(`
server:
  address: "127.0.0.1:9000"
tasks:
  - id: "one"
    command: "echo one"
`)

	var out bytes.Buffer
	cmd := newRootCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(bytes.NewReader(valid))
	cmd.SetArgs([]string{"--config", configPath, "config", "set"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config set command returned error: %v", err)
	}
	if !strings.Contains(out.String(), "updated "+configPath) {
		t.Fatalf("expected update message for %q, got %q", configPath, out.String())
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(valid) {
		t.Fatalf("expected config content to be written, got:\n%s", data)
	}

	invalid := []byte(`
tasks:
  - id: "bad"
`)
	out.Reset()
	cmd = newRootCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(bytes.NewReader(invalid))
	cmd.SetArgs([]string{"--config", configPath, "config", "set"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "invalid config") {
		t.Fatalf("expected invalid config error, got %v", err)
	}
	data, err = os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(valid) {
		t.Fatalf("expected invalid config not to overwrite file, got:\n%s", data)
	}

	protected := []byte(`
server:
  config_password: "secret"
tasks:
  - id: "protected"
    command: "echo protected"
`)
	if err := os.WriteFile(configPath, protected, 0644); err != nil {
		t.Fatal(err)
	}
	replacement := []byte(`
tasks:
  - id: "replaced"
    command: "echo replaced"
`)
	out.Reset()
	cmd = newRootCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(bytes.NewReader(replacement))
	cmd.SetArgs([]string{"--config", configPath, "config", "set"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config set should not require the web password, got %v", err)
	}
	data, err = os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(replacement) {
		t.Fatalf("expected CLI config set to replace protected config, got:\n%s", data)
	}
}

func TestServicePrintLinuxUnit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg"))

	var out bytes.Buffer
	cmd := newRootCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"--config", filepath.Join(t.TempDir(), "builda config.yaml"),
		"--addr", "127.0.0.1:9000",
		"--addr", "127.0.0.1:9001",
		"service", "print",
		"--target", "linux",
		"--binary", "/usr/local/bin/builda",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("service print returned error: %v", err)
	}
	body := out.String()
	for _, want := range []string{
		"[Unit]",
		`ExecStart="/usr/local/bin/builda" "serve" "--config"`,
		`"--addr" "127.0.0.1:9000" "--addr" "127.0.0.1:9001"`,
		"Restart=always",
		"WantedBy=default.target",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected service unit to include %q, got:\n%s", want, body)
		}
	}
}

func TestServicePrintLaunchdPlist(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var out bytes.Buffer
	cmd := newRootCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"--config", filepath.Join(t.TempDir(), "config.yaml"),
		"service", "print",
		"--target", "darwin",
		"--name", "builda.dev",
		"--binary", "/opt/builda/bin/builda",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("service print returned error: %v", err)
	}
	body := out.String()
	for _, want := range []string{
		`<string>com.bizdooroo.builda.builda.dev</string>`,
		`<string>/opt/builda/bin/builda</string>`,
		`<string>serve</string>`,
		`<string>--config</string>`,
		`<key>RunAtLoad</key>`,
		`<true/>`,
		filepath.Join(home, "Library", "Logs", "builda.dev.out.log"),
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected launchd plist to include %q, got:\n%s", want, body)
		}
	}
}

func TestServiceInstallDryRunDoesNotCreateConfig(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "missing", "config.yaml")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	var out bytes.Buffer
	cmd := newRootCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"--config", configPath,
		"service", "install",
		"--dry-run",
		"--target", "linux",
		"--binary", "/usr/local/bin/builda",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("service install --dry-run returned error: %v", err)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("expected dry-run not to create config, stat err=%v", err)
	}
	if !strings.Contains(out.String(), "# "+filepath.Join(home, ".config", "systemd", "user", "builda.service")) {
		t.Fatalf("expected dry-run output to include service path, got:\n%s", out.String())
	}
}

func TestServiceControlCommands(t *testing.T) {
	linux, err := serviceControlCommands("linux", "builda", "/ignored", "restart")
	if err != nil {
		t.Fatal(err)
	}
	if len(linux) != 1 || linux[0].Name != "systemctl" || strings.Join(linux[0].Args, " ") != "--user restart builda.service" {
		t.Fatalf("unexpected linux restart command: %#v", linux)
	}

	status, err := serviceControlCommands("linux", "builda", "/ignored", "status")
	if err != nil {
		t.Fatal(err)
	}
	if len(status) != 1 || !status[0].StreamOutput {
		t.Fatalf("expected linux status to stream output, got %#v", status)
	}

	path := filepath.Join(t.TempDir(), "com.bizdooroo.builda.plist")
	darwin, err := serviceControlCommands("darwin", "builda", path, "restart")
	if err != nil {
		t.Fatal(err)
	}
	if len(darwin) != 4 {
		t.Fatalf("expected four darwin restart commands, got %#v", darwin)
	}
	if darwin[0].Name != "launchctl" || strings.Join(darwin[0].Args[:2], " ") != "bootout "+launchdDomain() || !darwin[0].IgnoreError {
		t.Fatalf("unexpected darwin bootout command: %#v", darwin[0])
	}
	if strings.Join(darwin[3].Args, " ") != "kickstart -k "+launchdDomain()+"/com.bizdooroo.builda" {
		t.Fatalf("unexpected darwin kickstart command: %#v", darwin[3])
	}
}

func TestResolveListenAddressesUsesConfigByDefault(t *testing.T) {
	addrs := resolveListenAddresses("127.0.0.1:9000", nil)
	if len(addrs) != 1 || addrs[0] != "127.0.0.1:9000" {
		t.Fatalf("expected config address, got %#v", addrs)
	}

	addrs = resolveListenAddresses("", nil)
	if len(addrs) != 1 || addrs[0] != ":28088" {
		t.Fatalf("expected fallback address, got %#v", addrs)
	}
}

func TestResolveListenAddressesUsesRepeatedFlags(t *testing.T) {
	addrs := resolveListenAddresses(":28088", []string{"127.0.0.1:28088", "0.0.0.0:28088", "127.0.0.1:28088"})
	want := []string{"127.0.0.1:28088", "0.0.0.0:28088"}
	if len(addrs) != len(want) {
		t.Fatalf("expected %d addresses, got %#v", len(want), addrs)
	}
	for i := range want {
		if addrs[i] != want[i] {
			t.Fatalf("expected addresses %#v, got %#v", want, addrs)
		}
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
	cfg.Server.ConfigPassword = "secret"
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
    command: "echo two"
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
    command: "echo one"
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
		cfg:        Config{},
		pageTmpl:   template.Must(template.New("page").Parse(pageTemplate)),
		configTmpl: template.Must(template.New("config").Parse(configPageTemplate)),
		hostname:   "test-host",
		started:    time.Unix(0, 0),
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	app.handleIndex(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected index page to render, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `href="/config"`) {
		t.Fatalf("expected config button to be hidden without password, got:\n%s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/config", nil)
	app.handleConfigPage(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected config page to be hidden without password, got %d", rec.Code)
	}

	app.cfg.Server.ConfigPassword = "secret"
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	app.handleIndex(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected index page to render, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `href="/config"`) {
		t.Fatalf("expected config button with password, got:\n%s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/config", nil)
	app.handleConfigPage(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected config page with password, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `type="password"`) {
		t.Fatalf("expected password input on config page, got:\n%s", rec.Body.String())
	}
}

func TestAppReloadConfigFromDiskUpdatesTaskMap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	initial := []byte(`
tasks:
  - id: "one"
    command: "echo one"
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
    command: "echo two"
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

func TestTaskRunAPIPassesQueryInputsAsEnvironment(t *testing.T) {
	dir := t.TempDir()
	task := TaskConfig{
		ID:      "deploy",
		Name:    "Deploy",
		Command: `printf 'target=%s message=%s\n' "$BUILDA_INPUT_TARGET" "$BUILDA_INPUT_MESSAGE"`,
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
		Command: "sleep 0.1; echo waited",
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
		t.Fatalf("expected response log to include command output, got:\n%s", payload.Log)
	}
}

func TestTaskRunAPIRejectsInvalidWaitParam(t *testing.T) {
	dir := t.TempDir()
	task := TaskConfig{
		ID:      "hello",
		Name:    "Hello",
		Command: "echo hello",
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
		Command: "echo deploy",
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
	if !strings.Contains(body, `renderRunParams(run)`) || !strings.Contains(body, `formatRunParams(run.inputs)`) {
		t.Fatalf("expected runs page to render run input params in the summary header, got:\n%s", body)
	}
}

func TestRunPageRendersParamsHeaderHook(t *testing.T) {
	run := &Run{
		ID:       "run-with-inputs",
		TaskID:   "deploy",
		TaskName: "Deploy",
		Command:  "echo deploy",
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
		logTmpl:  template.Must(template.New("log").Parse(logPageTemplate)),
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
		`paramsEl.textContent = "params " + formattedParams;`,
		`function formatRunParams(inputs)`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected run page to include %q, got:\n%s", expected, body)
		}
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
		"async function copyText(text)",
		"document.execCommand(\"copy\")",
		"window.isSecureContext",
		"id=\"run-modal\"",
		"taskRunAPI(taskID, values = {})",
		"BUILDA_INPUT_",
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

func TestRunnerExecutesCommandAsBashScriptAndSourcesBashrc(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte("export BUILDA_FROM_BASHRC=loaded\n"), 0644); err != nil {
		t.Fatal(err)
	}

	runner := NewRunner(t.TempDir())
	run, err := runner.Start(TaskConfig{
		ID:      "bash",
		Name:    "Bash",
		Command: `if [[ -n "$BASH_VERSION" ]]; then printf 'shell=bash bashrc=%s\n' "$BUILDA_FROM_BASHRC"; fi`,
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
	if !strings.Contains(string(data), "shell=bash bashrc=loaded") {
		t.Fatalf("expected Bash command to source ~/.bashrc, got:\n%s", data)
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
	run, err := runner.Start(task, nil)
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
		Command: "sleep 0.2; echo first",
		Timeout: "5s",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runner.Start(TaskConfig{
		ID:      "second",
		Name:    "Second",
		Command: "echo second",
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

func newConfigSaveRequest(content, password string) *http.Request {
	body := url.Values{"content": {content}, "password": {password}}.Encode()
	req := httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}
