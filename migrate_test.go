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

// TestPlanDerivesJobsProjectsAndActionAliases covers the drafted mapping,
// including the iOS action aliases.
func TestPlanDerivesJobsProjectsAndActionAliases(t *testing.T) {
	fixture := newLegacyFixture(t, "mm", "ios", []string{"alpha", "nested_project"}, nil)
	outDir := filepath.Join(t.TempDir(), "bundle")
	bundle, _, err := exportLegacyBundle("mm", fixture.ConfigPath, outDir, true)
	if err != nil {
		t.Fatal(err)
	}

	mapping, diagnostics := planMigration(bundle, "macos-ios", "")
	if mapping.Agent.WorkspaceRoot != "/Users/someone/git/dooroo" {
		t.Fatalf("the workspace root should be inferred from the legacy scripts, got %q", mapping.Agent.WorkspaceRoot)
	}
	if !containsSubstring(diagnostics, "inferred") {
		t.Fatalf("an inferred workspace root must be flagged for review, got %v", diagnostics)
	}
	if strings.Join(mapping.Agent.Labels, ",") != "ios,macos" {
		t.Fatalf("unexpected agent labels %v", mapping.Agent.Labels)
	}
	if len(mapping.Jobs) != 1 || mapping.Jobs[0].ID != "ios-build" {
		t.Fatalf("unexpected jobs %+v", mapping.Jobs)
	}
	if mapping.Jobs[0].Timeout != "2h" {
		t.Fatalf("the iOS job should get the chosen 2h timeout, got %q", mapping.Jobs[0].Timeout)
	}
	if strings.Join(mapping.Jobs[0].Actions, ",") != "ad-hoc,app-store" {
		t.Fatalf("the legacy actions must map onto the new values, got %v", mapping.Jobs[0].Actions)
	}
	task := mapping.Tasks["alpha"]
	if task.Job != "ios-build" || task.Project != "alpha" || task.ProjectPath != "alpha" {
		t.Fatalf("unexpected task mapping %+v", task)
	}
	if task.InputValues["adhoc"] != "ad-hoc" || task.InputValues["deploy"] != "app-store" {
		t.Fatalf("expected the action aliases to be recorded, got %v", task.InputValues)
	}

	// An explicit workspace root wins and is not flagged.
	explicit, diagnostics := planMigration(bundle, "macos-ios", "/opt/code")
	if explicit.Agent.WorkspaceRoot != "/opt/code" {
		t.Fatalf("an explicit workspace root must win, got %q", explicit.Agent.WorkspaceRoot)
	}
	if containsSubstring(diagnostics, "inferred") {
		t.Fatal("an explicit workspace root must not be flagged as inferred")
	}

	// The mapping round-trips through YAML.
	encoded, err := marshalMapping(mapping)
	if err != nil {
		t.Fatal(err)
	}
	reparsed, err := parseMapping(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if reparsed.Machine != "mm" || len(reparsed.Tasks) != 2 {
		t.Fatalf("unexpected reparsed mapping %+v", reparsed)
	}
	if _, err := parseMapping([]byte("tasks: {}\n")); err == nil {
		t.Fatal("a mapping without a machine name must be rejected")
	}
}

func TestPlanReportsTasksItCannotMap(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	document := "server:\n  log_dir: \"logs\"\ntasks:\n  - id: \"odd\"\n    script: 'echo nothing recognisable'\n"
	if err := os.WriteFile(configPath, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle, _, err := exportLegacyBundle("x", configPath, filepath.Join(dir, "bundle"), true)
	if err != nil {
		t.Fatal(err)
	}
	mapping, diagnostics := planMigration(bundle, "x-agent", "/w")
	if len(mapping.Unmapped) != 1 || mapping.Unmapped[0] != "odd" {
		t.Fatalf("expected the task to be listed as unmapped, got %v", mapping.Unmapped)
	}
	if !containsSubstring(diagnostics, "map it by hand") {
		t.Fatalf("expected an actionable diagnostic, got %v", diagnostics)
	}
}

// TestGeneratedRoleConfigsMergeBothMachines proves one project present on both
// platforms becomes a single catalog entry carrying both platform labels.
func TestGeneratedRoleConfigsMergeBothMachines(t *testing.T) {
	linuxFixture := newLegacyFixture(t, "linux", "android", []string{"alpha", "android_only"}, nil)
	macFixture := newLegacyFixture(t, "mm", "ios", []string{"alpha", "ios_only"}, nil)
	linuxDir := filepath.Join(t.TempDir(), "bundle-linux")
	macDir := filepath.Join(t.TempDir(), "bundle-mm")
	linuxBundle, _, err := exportLegacyBundle("linux", linuxFixture.ConfigPath, linuxDir, true)
	if err != nil {
		t.Fatal(err)
	}
	macBundle, _, err := exportLegacyBundle("mm", macFixture.ConfigPath, macDir, true)
	if err != nil {
		t.Fatal(err)
	}
	linuxMapping, _ := planMigration(linuxBundle, "linux-android", "/home/someone/git/dooroo")
	macMapping, _ := planMigration(macBundle, "macos-ios", "/Users/someone/git/dooroo")

	cfg, agents, err := buildRoleConfigs(
		[]*MigrationBundle{linuxBundle, macBundle},
		[]*MigrationMapping{linuxMapping, macMapping})
	if err != nil {
		t.Fatalf("build role configs: %v", err)
	}

	if len(cfg.Catalogs) != 1 {
		t.Fatalf("expected one shared catalog, got %d", len(cfg.Catalogs))
	}
	alpha, ok := findOption(cfg.Catalogs[0].Options, "alpha")
	if !ok {
		t.Fatal("the shared project must appear once")
	}
	if strings.Join(alpha.Labels, ",") != "android,ios" {
		t.Fatalf("the shared project must carry both platform labels, got %v", alpha.Labels)
	}
	if alpha.Values[optionPathField] != "alpha" {
		t.Fatalf("unexpected project path %v", alpha.Values)
	}
	androidOnly, _ := findOption(cfg.Catalogs[0].Options, "android_only")
	if strings.Join(androidOnly.Labels, ",") != "android" {
		t.Fatalf("an android-only project must carry one label, got %v", androidOnly.Labels)
	}

	if len(cfg.Jobs) != 2 {
		t.Fatalf("expected both platform jobs, got %d", len(cfg.Jobs))
	}
	android, _ := findJob(cfg, "android-build")
	if android.Timeout != "45m" || android.WorkdirParam != "project" {
		t.Fatalf("unexpected android job %+v", android)
	}
	if strings.Join(android.Parameters[0].CatalogLabels, ",") != "android" {
		t.Fatalf("the android job must filter the catalog by its platform, got %v", android.Parameters[0].CatalogLabels)
	}
	if android.Parameters[1].Default != "debug" {
		t.Fatalf("expected the debug default, got %q", android.Parameters[1].Default)
	}
	ios, _ := findJob(cfg, "ios-build")
	if ios.Parameters[1].Default != "ad-hoc" {
		t.Fatalf("expected the ad-hoc default, got %q", ios.Parameters[1].Default)
	}
	if len(cfg.Agents) != 2 {
		t.Fatalf("expected one agent definition per machine, got %d", len(cfg.Agents))
	}

	// The generated agent configs keep each machine's own shell header.
	if !strings.Contains(agents["mm"].Agent.ScriptHeader, "homebrew") {
		t.Fatalf("the mac agent must keep its own header, got %q", agents["mm"].Agent.ScriptHeader)
	}
	if strings.Contains(agents["linux"].Agent.ScriptHeader, "homebrew") {
		t.Fatal("the linux agent must not inherit the mac header")
	}
	if agents["linux"].Agent.WorkspaceRoot != "/home/someone/git/dooroo" {
		t.Fatalf("unexpected linux workspace root %q", agents["linux"].Agent.WorkspaceRoot)
	}

	// Everything written out must load back through the normal loaders.
	outDir := t.TempDir()
	written, err := writeRoleConfigs(outDir, cfg, agents)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 3 {
		t.Fatalf("expected a controller config and two agent configs, got %v", written)
	}
	if _, err := loadControllerConfig(filepath.Join(outDir, controllerConfigName)); err != nil {
		t.Fatalf("the generated controller config must load: %v", err)
	}
	for _, machine := range []string{"linux", "mm"} {
		if _, err := loadAgentConfig(filepath.Join(outDir, "agent-"+machine+".yaml")); err != nil {
			t.Fatalf("the generated %s agent config must load: %v", machine, err)
		}
	}
}

func snapshotDir(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		b.WriteString(path)
		if !info.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			b.WriteString(string(data))
		}
		b.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func containsSubstring(values []string, needle string) bool {
	for _, value := range values {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
