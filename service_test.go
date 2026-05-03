package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
