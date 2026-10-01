package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestRoot builds the root command with isolated output buffers.
func newTestRoot(t *testing.T, args ...string) (*bytes.Buffer, error) {
	t.Helper()
	root := newRootCommand()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs(args)
	return out, root.Execute()
}

func TestServicePrintLinuxUnit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	configPath := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(configPath, []byte(sampleConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := newTestRoot(t, "service", "print", "--target", "linux", "--binary", "/usr/local/bin/builda", "--config", configPath)
	if err != nil {
		t.Fatalf("service print returned error: %v", err)
	}
	unit := out.String()
	for _, want := range []string{
		"Description=Builda task runner",
		`ExecStart="/usr/local/bin/builda" "serve" "--config"`,
		"Environment=PATH=/usr/local/bin:",
		"Restart=always",
		"RestartSec=10s",
		"TimeoutStopSec=30s",
		"WantedBy=default.target",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("expected systemd unit to include %q, got:\n%s", want, unit)
		}
	}
}

func TestServicePrintRoleUnits(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	controllerConfig := filepath.Join(home, "controller.yaml")
	if err := os.WriteFile(controllerConfig, []byte(sampleControllerConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := newTestRoot(t, "controller", "service", "print", "--target", "linux",
		"--binary", "/usr/local/bin/builda", "--config", controllerConfig)
	if err != nil {
		t.Fatalf("controller service print returned error: %v", err)
	}
	if !strings.Contains(out.String(), `"controller" "serve"`) {
		t.Fatalf("expected controller role exec args, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Description=Builda controller") {
		t.Fatalf("expected controller description, got:\n%s", out.String())
	}

	agentConfig := filepath.Join(home, "agent.yaml")
	if err := os.WriteFile(agentConfig, []byte(sampleAgentConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = newTestRoot(t, "agent", "service", "print", "--target", "linux",
		"--binary", "/usr/local/bin/builda", "--config", agentConfig)
	if err != nil {
		t.Fatalf("agent service print returned error: %v", err)
	}
	if !strings.Contains(out.String(), `"agent" "run"`) {
		t.Fatalf("expected agent role exec args, got:\n%s", out.String())
	}
}

// TestServicePrintLaunchdPlist pins the keys that make a background
// LaunchAgent start reliably: a direct executable, an explicit PATH that
// includes Homebrew, Background process type, a restart throttle, and
// redirected logs.
func TestServicePrintLaunchdPlist(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configPath := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(configPath, []byte(sampleConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := newTestRoot(t, "service", "print", "--target", "darwin", "--name", "builda-controller",
		"--binary", "/usr/local/bin/builda", "--config", configPath)
	if err != nil {
		t.Fatalf("service print returned error: %v", err)
	}
	plist := out.String()
	for _, want := range []string{
		"<string>com.bizdooroo.builda.controller</string>",
		"<string>/usr/local/bin/builda</string>",
		"<key>EnvironmentVariables</key>",
		"/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
		"<key>ProcessType</key>\n  <string>Background</string>",
		"<key>LimitLoadToSessionType</key>\n  <string>Aqua</string>",
		"<key>ThrottleInterval</key>\n  <integer>10</integer>",
		"<key>ExitTimeOut</key>\n  <integer>30</integer>",
		filepath.Join(home, "Library", "Logs", "builda", "builda-controller.out.log"),
		filepath.Join(home, "Library", "Logs", "builda", "builda-controller.err.log"),
	} {
		if !strings.Contains(plist, want) {
			t.Fatalf("expected launchd plist to include %q, got:\n%s", want, plist)
		}
	}
	for _, unwanted := range []string{"bash", "/bin/sh", "osascript", "open ", "Terminal", "login"} {
		if strings.Contains(plist, unwanted) {
			t.Fatalf("launchd plist must not reference %q, got:\n%s", unwanted, plist)
		}
	}
}

func TestLaunchdLabelRoleNaming(t *testing.T) {
	cases := map[string]string{
		defaultServiceName:    "com.bizdooroo.builda",
		controllerServiceName: "com.bizdooroo.builda.controller",
		agentServiceName:      "com.bizdooroo.builda.agent",
		"builda-mac":          "com.bizdooroo.builda.mac",
		"custom":              "com.bizdooroo.builda.custom",
	}
	for name, want := range cases {
		if got := launchdLabel(name); got != want {
			t.Fatalf("launchdLabel(%q) = %q, want %q", name, got, want)
		}
	}
	if got := roleDefaultServiceName(RoleController); got != controllerServiceName {
		t.Fatalf("controller service name = %q", got)
	}
	if got := roleDefaultServiceName(RoleAgent); got != agentServiceName {
		t.Fatalf("agent service name = %q", got)
	}
}

func TestServiceInstallDryRunDoesNotCreateConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	configPath := filepath.Join(home, "missing", "config.yaml")

	out, err := newTestRoot(t, "service", "install", "--dry-run", "--target", "linux",
		"--binary", "/usr/local/bin/builda", "--config", configPath)
	if err != nil {
		t.Fatalf("service install --dry-run returned error: %v", err)
	}
	if _, statErr := os.Stat(configPath); !os.IsNotExist(statErr) {
		t.Fatalf("dry run must not create the config file, stat error: %v", statErr)
	}
	if !strings.Contains(out.String(), "# planned commands") {
		t.Fatalf("expected the dry run to print the planned commands, got:\n%s", out.String())
	}
	if _, statErr := os.Stat(filepath.Join(home, ".config", "systemd", "user", "builda.service")); !os.IsNotExist(statErr) {
		t.Fatalf("dry run must not write the unit file")
	}
}
