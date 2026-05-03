package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
    script: "echo one"
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
    script: "echo protected"
`)
	if err := os.WriteFile(configPath, protected, 0644); err != nil {
		t.Fatal(err)
	}
	replacement := []byte(`
tasks:
  - id: "replaced"
    script: "echo replaced"
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
