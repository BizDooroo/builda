package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

// TestPlanKeepsNestedProjectPaths is the regression guard for a project that
// sits one level deeper than its siblings. Taking the parent of each script
// path made that project's root differ from everyone else's, which produced a
// project path that does not exist under the agent workspace.
func TestPlanKeepsNestedProjectPaths(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	root := "/home/someone/git/dooroo"
	document := "server:\n  log_dir: \"logs\"\ntasks:\n"
	for _, project := range []string{"flags", "aladin-lamp/lamp_app", "ornament_pop"} {
		id := strings.ReplaceAll(project, "/", "-")
		document += "  - id: \"" + id + "\"\n    script: 'bash " + root + "/" + project + "/tools/build_android.sh'\n    timeout: \"45m\"\n"
	}
	if err := os.WriteFile(configPath, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle, _, err := exportLegacyBundle("linux", configPath, filepath.Join(dir, "bundle"), true)
	if err != nil {
		t.Fatal(err)
	}

	// The root is inferred from what every project shares, not from one path.
	mapping, _ := planMigration(bundle, "linux-android", "")
	if mapping.Agent.WorkspaceRoot != root {
		t.Fatalf("expected the shared root %q, got %q", root, mapping.Agent.WorkspaceRoot)
	}
	nested := mapping.Tasks["aladin-lamp-lamp_app"]
	if nested.Project != "aladin-lamp/lamp_app" || nested.ProjectPath != "aladin-lamp/lamp_app" {
		t.Fatalf("a nested project must keep every segment, got %+v", nested)
	}
	if flat := mapping.Tasks["flags"]; flat.ProjectPath != "flags" {
		t.Fatalf("a top-level project must stay as it is, got %+v", flat)
	}
	if len(mapping.Unmapped) != 0 {
		t.Fatalf("every task should map, got %v", mapping.Unmapped)
	}

	// An explicit root is honoured, and a project outside it is reported.
	mapping, diagnostics := planMigration(bundle, "linux-android", "/home/someone/git/dooroo/aladin-lamp")
	if len(mapping.Unmapped) != 2 {
		t.Fatalf("projects outside the given root must be reported, got %v", mapping.Unmapped)
	}
	if !containsSubstring(diagnostics, "is not under the workspace root") {
		t.Fatalf("expected an actionable diagnostic, got %v", diagnostics)
	}

	// The generated catalog carries the nested path, and it survives
	// validation, which is what the agent resolves against its workspace.
	mapping, _ = planMigration(bundle, "linux-android", root)
	cfg, _, err := buildRoleConfigs([]*MigrationBundle{bundle}, []*MigrationMapping{mapping})
	if err != nil {
		t.Fatalf("the generated config must be valid: %v", err)
	}
	option, ok := findOption(cfg.Catalogs[0].Options, "aladin-lamp/lamp_app")
	if !ok {
		t.Fatal("the nested project must appear in the catalog")
	}
	if option.Values[optionPathField] != "aladin-lamp/lamp_app" {
		t.Fatalf("unexpected catalog path %v", option.Values)
	}
	if err := validateRelativeWorkspacePath(option.Values[optionPathField]); err != nil {
		t.Fatalf("the nested path must be a valid workspace path: %v", err)
	}
}

// TestInferWorkspaceRootSharesThePrefix pins the root inference directly.
func TestInferWorkspaceRootSharesThePrefix(t *testing.T) {
	cases := []struct {
		paths []string
		want  string
	}{
		{[]string{"/a/b/one", "/a/b/two"}, "/a/b"},
		{[]string{"/a/b/one", "/a/b/nested/two"}, "/a/b"},
		{[]string{"/a/b/only"}, "/a/b"},
		{[]string{"/a/b/one", "/c/d/two"}, ""},
		{nil, ""},
	}
	for _, testCase := range cases {
		if got := inferWorkspaceRoot(testCase.paths); got != testCase.want {
			t.Fatalf("inferWorkspaceRoot(%v) = %q, want %q", testCase.paths, got, testCase.want)
		}
	}
}
