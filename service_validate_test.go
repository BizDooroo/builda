package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeExecutable(t *testing.T, path string, content string, mode os.FileMode) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestValidateServiceBinaryRejectsShellScript keeps the daemon from ever being
// a shell: a #! target would make launchd run bash as the service process.
func TestValidateServiceBinaryRejectsShellScript(t *testing.T) {
	path := writeExecutable(t, filepath.Join(t.TempDir(), "home", "bin", "builda"), "#!/bin/bash\nexec builda serve\n", 0o755)
	err := validateServiceBinary(path)
	if err == nil {
		t.Fatal("expected a script target to be rejected")
	}
	if !strings.Contains(err.Error(), "#! interpreter line") {
		t.Fatalf("expected an explanation about the interpreter line, got %v", err)
	}
}

// withoutTransientRoots lets a test fixture live in a temporary directory
// while still exercising the rest of the validation.
func withoutTransientRoots(t *testing.T) {
	t.Helper()
	previous := serviceTransientRoots
	serviceTransientRoots = func() []string { return nil }
	t.Cleanup(func() { serviceTransientRoots = previous })
}

func TestValidateServiceBinaryAcceptsStableExecutable(t *testing.T) {
	withoutTransientRoots(t)
	home := t.TempDir()
	path := writeExecutable(t, filepath.Join(home, "bin", "builda"), "\x7fELF\x02\x01\x01builda", 0o755)
	if err := validateServiceBinary(path); err != nil {
		t.Fatalf("expected a stable executable to be accepted, got %v", err)
	}
}

func TestValidateServiceBinaryRejectsBuildCacheAndTemp(t *testing.T) {
	if err := rejectTransientBinaryPath("/home/me/go/pkg/mod/go-build/exe/builda"); err == nil {
		t.Fatal("expected any go-build path segment to be rejected")
	}
	cachePath := writeExecutable(t, filepath.Join(t.TempDir(), "go-build1234", "b001", "exe", "builda"), "\x7fELFbuilda", 0o755)
	err := validateServiceBinary(cachePath)
	if err == nil || !strings.Contains(err.Error(), "Go build cache") {
		t.Fatalf("expected a go-build cache target to be rejected, got %v", err)
	}

	tempPath := writeExecutable(t, filepath.Join(t.TempDir(), "builda"), "\x7fELFbuilda", 0o755)
	err = validateServiceBinary(tempPath)
	if err == nil || !strings.Contains(err.Error(), "temporary directory") {
		t.Fatalf("expected a temporary directory target to be rejected, got %v", err)
	}
}

func TestValidateServiceBinaryRejectsUnusableTargets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := validateServiceBinary(""); err == nil {
		t.Fatal("expected an empty path to be rejected")
	}
	if err := validateServiceBinary("builda"); err == nil {
		t.Fatal("expected a relative path to be rejected")
	}
	if err := validateServiceBinary(filepath.Join(home, "missing")); err == nil {
		t.Fatal("expected a missing file to be rejected")
	}
	dir := filepath.Join(home, "dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateServiceBinary(dir); err == nil {
		t.Fatal("expected a directory to be rejected")
	}
	notExec := writeExecutable(t, filepath.Join(home, "bin", "plain"), "\x7fELFbuilda", 0o644)
	if err := validateServiceBinary(notExec); err == nil || !strings.Contains(err.Error(), "not executable") {
		t.Fatalf("expected a non-executable file to be rejected, got %v", err)
	}
}

// TestValidateServiceRoleConfigRejectsWrongRole stops an install from
// producing a unit whose config the daemon would refuse to load.
func TestValidateServiceRoleConfigRejectsWrongRole(t *testing.T) {
	dir := t.TempDir()
	controllerPath := filepath.Join(dir, "controller.yaml")
	agentPath := filepath.Join(dir, "agent.yaml")
	legacyPath := filepath.Join(dir, "legacy.yaml")
	if err := os.WriteFile(controllerPath, []byte(sampleControllerConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agentPath, []byte(sampleAgentConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte(sampleConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := validateServiceRoleConfig(RoleController, controllerPath); err != nil {
		t.Fatalf("controller config should validate: %v", err)
	}
	if err := validateServiceRoleConfig(RoleAgent, agentPath); err != nil {
		t.Fatalf("agent config should validate: %v", err)
	}
	if err := validateServiceRoleConfig(RoleStandalone, legacyPath); err != nil {
		t.Fatalf("legacy config should validate: %v", err)
	}
	if err := validateServiceRoleConfig(RoleController, agentPath); err == nil {
		t.Fatal("expected an agent config to be rejected for the controller role")
	}
	if err := validateServiceRoleConfig(RoleAgent, legacyPath); err == nil {
		t.Fatal("expected a legacy config to be rejected for the agent role")
	}
	if err := validateServiceRoleConfig(RoleController, filepath.Join(dir, "missing.yaml")); err == nil {
		t.Fatal("expected a missing config to be rejected")
	}
}

func TestEnsureServiceDirectoriesCreatesLogDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	artifact := filepath.Join(home, "Library", "LaunchAgents", "com.bizdooroo.builda.controller.plist")
	spec := serviceSpec{Name: controllerServiceName, Role: RoleController, TargetOS: "darwin"}
	if err := ensureServiceDirectories(spec, artifact); err != nil {
		t.Fatalf("ensure service directories: %v", err)
	}
	for _, dir := range []string{
		filepath.Join(home, "Library", "LaunchAgents"),
		filepath.Join(home, "Library", "Logs", "builda"),
	} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Fatalf("expected %s to exist as a directory, got %v", dir, err)
		}
	}
}
