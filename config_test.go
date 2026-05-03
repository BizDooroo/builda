package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
    script: "echo one"
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
    script: "echo bad"
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
    script: "echo deploy"
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

func TestParseConfigSupportsMultipleServerAddresses(t *testing.T) {
	cfg, err := parseConfig([]byte(`
server:
  address: "127.0.0.1:28088"
  addresses:
    - "192.168.0.40:28088"
    - "127.0.0.1:28088"
tasks:
  - id: "hello"
    script: "echo hello"
`))
	if err != nil {
		t.Fatalf("parseConfig returned error: %v", err)
	}
	addrs := configuredListenAddresses(cfg.Server)
	want := []string{"127.0.0.1:28088", "192.168.0.40:28088"}
	if len(addrs) != len(want) {
		t.Fatalf("expected %d addresses, got %#v", len(want), addrs)
	}
	for i := range want {
		if addrs[i] != want[i] {
			t.Fatalf("expected addresses %#v, got %#v", want, addrs)
		}
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
    script: "echo bad"
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
    script: "echo bad"
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
    script: "echo bad"
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
    script: "echo bad"
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
    script: "echo bad"
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
		"server.addresses",
		"server.log_dir",
		"server.config_password",
		"tasks[].id",
		"tasks[].script",
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

func TestLoadRuntimeConfigAppliesScriptHeaderToTasks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	err := os.WriteFile(path, []byte(`
server:
  script_header: |
    #!/usr/bin/env bash
    export BUILDA_FROM_HEADER=loaded
tasks:
  - id: one
    script: echo "$BUILDA_FROM_HEADER"
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := loadRuntimeConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Tasks) != 1 || !strings.Contains(cfg.Tasks[0].ScriptHeader, "BUILDA_FROM_HEADER=loaded") {
		t.Fatalf("expected runtime task to carry script header, got %#v", cfg.Tasks)
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
