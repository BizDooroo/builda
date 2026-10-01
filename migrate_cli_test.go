package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateCLIDefaultsToDryRun(t *testing.T) {
	fixture := newLegacyFixture(t, "linux", "android", []string{"alpha"}, nil)
	outDir := filepath.Join(t.TempDir(), "bundle")

	out, err := newTestRoot(t, "migrate", "export", "--machine", "linux", "--config", fixture.ConfigPath, "--out-dir", outDir)
	if err != nil {
		t.Fatalf("migrate export: %v", err)
	}
	if !strings.Contains(out.String(), "machine linux") {
		t.Fatalf("unexpected output %s", out.String())
	}
	if _, statErr := os.Stat(outDir); !os.IsNotExist(statErr) {
		t.Fatal("migrate export must default to a dry run")
	}

	if _, err := newTestRoot(t, "migrate", "export", "--config", fixture.ConfigPath, "--out-dir", outDir); err == nil {
		t.Fatal("expected --machine to be required")
	}
	if _, err := newTestRoot(t, "migrate", "export", "--machine", "linux", "--config", fixture.ConfigPath); err == nil {
		t.Fatal("expected --out-dir to be required")
	}

	if _, err := newTestRoot(t, "migrate", "export", "--machine", "linux", "--config", fixture.ConfigPath, "--out-dir", outDir, "--apply"); err != nil {
		t.Fatalf("migrate export --apply: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, bundleFileName)); err != nil {
		t.Fatalf("--apply must write the bundle: %v", err)
	}

	// plan prints the mapping without writing it.
	out, err = newTestRoot(t, "migrate", "plan", "--bundle", outDir, "--workspace-root", "/home/someone/git/dooroo")
	if err != nil {
		t.Fatalf("migrate plan: %v", err)
	}
	if !strings.Contains(out.String(), "machine: linux") {
		t.Fatalf("expected the drafted mapping on stdout, got %s", out.String())
	}
	if _, statErr := os.Stat(filepath.Join(outDir, mappingFileName)); !os.IsNotExist(statErr) {
		t.Fatal("migrate plan must default to a dry run")
	}
	if _, err := newTestRoot(t, "migrate", "plan", "--bundle", outDir, "--workspace-root", "/home/someone/git/dooroo", "--apply"); err != nil {
		t.Fatalf("migrate plan --apply: %v", err)
	}
	mappingPath := filepath.Join(outDir, mappingFileName)
	if _, err := os.Stat(mappingPath); err != nil {
		t.Fatalf("--apply must write the mapping: %v", err)
	}

	// config prints both documents without writing them.
	out, err = newTestRoot(t, "migrate", "config", "--bundle", outDir, "--map", mappingPath)
	if err != nil {
		t.Fatalf("migrate config: %v", err)
	}
	if !strings.Contains(out.String(), "role: controller") || !strings.Contains(out.String(), "role: agent") {
		t.Fatalf("expected both role documents, got %s", out.String())
	}
	if _, err := newTestRoot(t, "migrate", "config", "--bundle", outDir, "--map", mappingPath, "--apply"); err == nil {
		t.Fatal("expected --out-dir to be required with --apply")
	}
	if _, err := newTestRoot(t, "migrate", "config", "--bundle", outDir); err == nil {
		t.Fatal("expected --map to be required")
	}
	if _, err := newTestRoot(t, "migrate", "config", "--map", mappingPath); err == nil {
		t.Fatal("expected --bundle to be required")
	}

	configOut := filepath.Join(t.TempDir(), "new")
	if _, err := newTestRoot(t, "migrate", "config", "--bundle", outDir, "--map", mappingPath, "--out-dir", configOut, "--apply"); err != nil {
		t.Fatalf("migrate config --apply: %v", err)
	}
	controllerPath := filepath.Join(configOut, controllerConfigName)
	if _, err := loadControllerConfig(controllerPath); err != nil {
		t.Fatalf("the generated controller config must load: %v", err)
	}

	// import is a dry run by default.
	if _, err := newTestRoot(t, "migrate", "import", "--bundle", outDir, "--map", mappingPath, "--config", controllerPath); err != nil {
		t.Fatalf("migrate import: %v", err)
	}
	cfg, err := loadControllerConfig(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := controllerRuntime(controllerPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(runtime.StatePath); !os.IsNotExist(statErr) {
		t.Fatal("migrate import must default to a dry run")
	}
	if _, err := newTestRoot(t, "migrate", "import", "--bundle", outDir, "--map", mappingPath, "--config", controllerPath, "--apply"); err != nil {
		t.Fatalf("migrate import --apply: %v", err)
	}
	if _, err := os.Stat(runtime.StatePath); err != nil {
		t.Fatalf("--apply must write the controller state: %v", err)
	}
}
