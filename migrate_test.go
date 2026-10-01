package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// legacyFixture writes a synthetic legacy installation: a standalone config, a
// runs.json, and the matching log files.
type legacyFixture struct {
	Dir        string
	ConfigPath string
	LogDir     string
}

func newLegacyFixture(t *testing.T, machine string, platform string, projects []string, extraRuns []LegacyRun) legacyFixture {
	t.Helper()
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	root := "/home/someone/git/dooroo"
	actions := []string{"debug", "release", "aab", "bump"}
	header := "#!/usr/bin/env bash\nset -euo pipefail\nsource \"$HOME/.config/builda/env.sh\""
	timeout := "45m"
	if platform == "ios" {
		root = "/Users/someone/git/dooroo"
		actions = []string{"adhoc", "deploy"}
		header = "#!/usr/bin/env bash\nexport PATH=\"/opt/homebrew/bin:$PATH\"\n[[ -f \"$HOME/.bashrc\" ]] && source \"$HOME/.bashrc\""
		timeout = "90m"
	}

	var builder strings.Builder
	builder.WriteString("server:\n  address: \"127.0.0.1:28088\"\n  log_dir: \"logs\"\n  script_header: |\n")
	for _, line := range strings.Split(header, "\n") {
		builder.WriteString("    " + line + "\n")
	}
	builder.WriteString("tasks:\n")
	for _, project := range projects {
		builder.WriteString("  - id: \"" + project + "\"\n")
		builder.WriteString("    name: \"" + project + "\"\n")
		builder.WriteString("    script: 'bash " + root + "/" + project + "/tools/build_" + platform + ".sh \"$BUILDA_INPUT_ACTION\"'\n")
		builder.WriteString("    timeout: \"" + timeout + "\"\n")
		builder.WriteString("    inputs:\n      - id: \"action\"\n        type: \"choice\"\n        default: \"" + actions[0] + "\"\n        options:\n")
		for _, action := range actions {
			builder.WriteString("          - \"" + action + "\"\n")
		}
	}
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(builder.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	runs := make([]LegacyRun, 0, len(projects)+len(extraRuns))
	base := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	for index, project := range projects {
		id := machine + "-run-" + project
		runs = append(runs, LegacyRun{
			ID:          id,
			TaskID:      project,
			TaskName:    project,
			Script:      "bash " + root + "/" + project + "/tools/build_" + platform + ".sh",
			Inputs:      map[string]string{"action": actions[0]},
			TimeoutText: timeout,
			Status:      StatusSuccess,
			RequestedAt: base.Add(time.Duration(index) * time.Minute),
			StartedAt:   base.Add(time.Duration(index) * time.Minute),
			FinishedAt:  base.Add(time.Duration(index)*time.Minute + 30*time.Second),
			ExitCode:    0,
		})
		if err := os.WriteFile(filepath.Join(logDir, id+".log"), []byte("legacy log for "+id+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runs = append(runs, extraRuns...)
	encoded, err := json.MarshalIndent(runs, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "runs.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return legacyFixture{Dir: dir, ConfigPath: configPath, LogDir: logDir}
}

// TestReadLegacyRunStateNeverExecutesAnything is the migration safety test: the
// legacy state is read as data, with no runner and no dispatch.
func TestReadLegacyRunStateNeverExecutesAnything(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "executed")
	fixture := newLegacyFixture(t, "linux", "android", []string{"alpha"}, []LegacyRun{{
		ID:          "queued-run",
		TaskID:      "alpha",
		Script:      "touch " + marker,
		Status:      StatusQueued,
		RequestedAt: time.Now(),
		ExitCode:    -1,
	}})

	runs, err := readLegacyRunState(filepath.Join(fixture.LogDir, "runs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected two legacy runs, got %d", len(runs))
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("reading legacy state must never execute a queued run")
	}

	// A missing state file is not an error.
	empty, err := readLegacyRunState(filepath.Join(t.TempDir(), "runs.json"))
	if err != nil || len(empty) != 0 {
		t.Fatalf("a missing run state must read as empty, got %v (%v)", empty, err)
	}
	// A corrupt state file is reported.
	broken := filepath.Join(t.TempDir(), "runs.json")
	if err := os.WriteFile(broken, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readLegacyRunState(broken); err == nil {
		t.Fatal("expected a corrupt run state to be reported")
	}
}

func TestExportIsDryRunByDefaultAndLeavesLegacyUntouched(t *testing.T) {
	fixture := newLegacyFixture(t, "linux", "android", []string{"alpha", "beta"}, nil)
	outDir := filepath.Join(t.TempDir(), "bundle")
	before := snapshotDir(t, fixture.Dir)

	bundle, diagnostics, err := exportLegacyBundle("linux", fixture.ConfigPath, outDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Tasks) != 2 || len(bundle.Runs) != 2 {
		t.Fatalf("unexpected bundle %+v", bundle)
	}
	if _, err := os.Stat(outDir); !os.IsNotExist(err) {
		t.Fatal("a dry run must not write the bundle")
	}
	if !containsSubstring(diagnostics, "dry run") {
		t.Fatalf("expected a dry-run diagnostic, got %v", diagnostics)
	}

	if _, _, err := exportLegacyBundle("linux", fixture.ConfigPath, outDir, true); err != nil {
		t.Fatal(err)
	}
	if snapshotDir(t, fixture.Dir) != before {
		t.Fatal("export must leave the legacy installation untouched")
	}
	loaded, err := loadMigrationBundle(outDir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Machine != "linux" || loaded.ScriptHeader == "" {
		t.Fatalf("unexpected loaded bundle %+v", loaded)
	}
	for _, run := range loaded.Runs {
		data, err := os.ReadFile(filepath.Join(outDir, bundleLogDir, run.ID+".log"))
		if err != nil {
			t.Fatalf("the bundle must carry the log for %s: %v", run.ID, err)
		}
		if !strings.Contains(string(data), run.ID) {
			t.Fatalf("unexpected log content for %s", run.ID)
		}
	}
}

func TestExportSkipsNonTerminalRunsAndReportsMissingLogs(t *testing.T) {
	fixture := newLegacyFixture(t, "linux", "android", []string{"alpha"}, []LegacyRun{
		{ID: "still-running", TaskID: "alpha", Status: StatusRunning, RequestedAt: time.Now(), ExitCode: -1},
		{ID: "no-log-run", TaskID: "alpha", Status: StatusFailed, RequestedAt: time.Now(), ExitCode: 1},
	})
	outDir := filepath.Join(t.TempDir(), "bundle")
	bundle, diagnostics, err := exportLegacyBundle("linux", fixture.ConfigPath, outDir, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range bundle.Runs {
		if run.ID == "still-running" {
			t.Fatal("a non-terminal run must never be exported")
		}
	}
	if !containsSubstring(diagnostics, "is not terminal") {
		t.Fatalf("expected a diagnostic for the running run, got %v", diagnostics)
	}
	if !containsSubstring(diagnostics, "missing log for no-log-run") {
		t.Fatalf("expected a missing-log diagnostic, got %v", diagnostics)
	}
	if len(bundle.MissingLogs) != 1 || bundle.MissingLogs[0] != "no-log-run" {
		t.Fatalf("unexpected missing log list %v", bundle.MissingLogs)
	}
}

func TestExportRejectsARoleConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "controller.yaml")
	if err := os.WriteFile(path, []byte(sampleControllerConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := exportLegacyBundle("x", path, dir, false); err == nil {
		t.Fatal("expected a controller config to be rejected by migrate export")
	}
}
