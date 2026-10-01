package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// migrationScenario is a fully prepared bundle plus mapping and a controller
// built from the generated role config.
type migrationScenario struct {
	controller *Controller
	bundle     *MigrationBundle
	mapping    *MigrationMapping
	bundleDir  string
}

func newMigrationScenario(t *testing.T, extraRuns []LegacyRun) migrationScenario {
	t.Helper()
	fixture := newLegacyFixture(t, "linux", "android", []string{"alpha", "beta"}, extraRuns)
	bundleDir := filepath.Join(t.TempDir(), "bundle")
	bundle, _, err := exportLegacyBundle("linux", fixture.ConfigPath, bundleDir, true)
	if err != nil {
		t.Fatal(err)
	}
	mapping, _ := planMigration(bundle, "linux-android", "/home/someone/git/dooroo")
	cfg, _, err := buildRoleConfigs([]*MigrationBundle{bundle}, []*MigrationMapping{mapping})
	if err != nil {
		t.Fatal(err)
	}
	document, err := marshalControllerConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return migrationScenario{
		controller: newTestController(t, string(document)),
		bundle:     bundle,
		mapping:    mapping,
		bundleDir:  bundleDir,
	}
}

func TestImportIsDryRunByDefault(t *testing.T) {
	scenario := newMigrationScenario(t, nil)
	report, err := importBundle(scenario.controller, scenario.bundle, scenario.mapping, scenario.bundleDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Imported != 2 {
		t.Fatalf("expected two importable runs, got %d", report.Imported)
	}
	if len(scenario.controller.store.Executions()) != 0 {
		t.Fatal("a dry run must not write anything")
	}
	if !containsSubstring(report.Diagnostics, "dry run") {
		t.Fatalf("expected a dry-run diagnostic, got %v", report.Diagnostics)
	}
}

// TestImportMapsJobProjectAndLogName covers the mapped filters and the new
// log path derived from the new execution id.
func TestImportMapsJobProjectAndLogName(t *testing.T) {
	scenario := newMigrationScenario(t, nil)
	report, err := importBundle(scenario.controller, scenario.bundle, scenario.mapping, scenario.bundleDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Imported != 2 || report.Skipped != 0 {
		t.Fatalf("unexpected report %+v", report)
	}

	executions := scenario.controller.store.Executions()
	if len(executions) != 2 {
		t.Fatalf("expected two imported executions, got %d", len(executions))
	}
	for _, execution := range executions {
		if execution.ID == execution.Origin.LegacyRunID {
			t.Fatal("an imported execution must get a new id")
		}
		if execution.JobID != "android-build" {
			t.Fatalf("the job must be mapped for filters, got %q", execution.JobID)
		}
		if execution.Parameters["project"] != execution.Origin.LegacyTask {
			t.Fatalf("the project must be mapped for filters, got %v", execution.Parameters)
		}
		if execution.Parameters["action"] != "debug" {
			t.Fatalf("legacy inputs must be mapped onto parameters, got %v", execution.Parameters)
		}
		if execution.Status != StatusSuccess || execution.AgentID != "linux-android" {
			t.Fatalf("unexpected imported execution %+v", execution)
		}
		if execution.Origin.TimeoutText != "45m" || execution.Origin.Script == "" || execution.Origin.ScriptHdr == "" {
			t.Fatalf("the original snapshot must be preserved, got %+v", execution.Origin)
		}
		if execution.Origin.Inputs["action"] != "debug" {
			t.Fatalf("the original inputs must be preserved, got %v", execution.Origin.Inputs)
		}
		if !execution.Origin.LogImported {
			t.Fatalf("the log should have been imported for %s", execution.Origin.LegacyRunID)
		}

		// The log lives under the new execution id, not the legacy one.
		newPath := executionLogPath(scenario.controller.Runtime().LogDir, execution.ID)
		data, err := os.ReadFile(newPath)
		if err != nil {
			t.Fatalf("expected the log at the derived path %s: %v", newPath, err)
		}
		if !strings.Contains(string(data), execution.Origin.LegacyRunID) {
			t.Fatalf("unexpected log content %q", data)
		}
		legacyPath := executionLogPath(scenario.controller.Runtime().LogDir, execution.Origin.LegacyRunID)
		if _, err := os.Stat(legacyPath); err == nil {
			t.Fatal("no log may be written under the legacy id")
		}
	}

	// The imported history is filterable by job and project.
	api := newControllerAPI(scenario.controller)
	session := adminSession(t, api)
	if got := listRuns(t, api, session, "?job=android-build").Total; got != 2 {
		t.Fatalf("the job filter must find the imported runs, got %d", got)
	}
	if got := listRuns(t, api, session, "?project=beta").Total; got != 1 {
		t.Fatalf("the project filter must find the imported run, got %d", got)
	}
}

// TestImportIsIdempotentPerMachineAndRunID proves re-running an import does
// not duplicate history.
func TestImportIsIdempotentPerMachineAndRunID(t *testing.T) {
	scenario := newMigrationScenario(t, nil)
	if _, err := importBundle(scenario.controller, scenario.bundle, scenario.mapping, scenario.bundleDir, true); err != nil {
		t.Fatal(err)
	}
	report, err := importBundle(scenario.controller, scenario.bundle, scenario.mapping, scenario.bundleDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Imported != 0 || report.Existing != 2 {
		t.Fatalf("a repeated import must be a no-op, got %+v", report)
	}
	if len(scenario.controller.store.Executions()) != 2 {
		t.Fatalf("expected two executions after two imports, got %d", len(scenario.controller.store.Executions()))
	}

	// The same legacy run id from a different machine is a different run.
	other := *scenario.bundle
	other.Machine = "mm"
	report, err = importBundle(scenario.controller, &other, scenario.mapping, scenario.bundleDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Imported != 2 {
		t.Fatalf("a different machine must import separately, got %+v", report)
	}
}

// TestImportNeverQueuesWork is the safety test: nothing imported can run.
func TestImportNeverQueuesWork(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "executed")
	scenario := newMigrationScenario(t, []LegacyRun{
		{ID: "queued-legacy", TaskID: "alpha", Script: "touch " + marker, Status: StatusQueued, RequestedAt: time.Now(), ExitCode: -1},
		{ID: "running-legacy", TaskID: "alpha", Script: "touch " + marker, Status: StatusRunning, RequestedAt: time.Now(), ExitCode: -1},
	})
	markOnline(scenario.controller, "linux-android")

	report, err := importBundle(scenario.controller, scenario.bundle, scenario.mapping, scenario.bundleDir, true)
	if err != nil {
		t.Fatal(err)
	}
	// Non-terminal legacy runs never even reach the bundle, so there is
	// nothing to import for them.
	for _, execution := range scenario.controller.store.Executions() {
		if !isExecutionTerminal(execution.Status) {
			t.Fatalf("imported execution %s is not terminal: %q", execution.ID, execution.Status)
		}
	}
	if err := scenario.controller.scheduleOnce(); err != nil {
		t.Fatal(err)
	}
	if assignment := scenario.controller.AssignmentFor("linux-android"); assignment != nil {
		t.Fatalf("imported history must never be assigned, got %+v", assignment)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("no imported run may execute")
	}
	if report.Imported != 2 {
		t.Fatalf("only the terminal runs are imported, got %+v", report)
	}
}

// TestImportReportsMissingLogsExplicitly covers the diagnostic path and proves
// no path from the bundle is trusted.
func TestImportReportsMissingLogsExplicitly(t *testing.T) {
	scenario := newMigrationScenario(t, nil)
	// Remove one log from the bundle and point its recorded path somewhere
	// dangerous; only the legacy run id may be used to find it.
	target := scenario.bundle.Runs[0]
	if err := os.Remove(filepath.Join(scenario.bundleDir, bundleLogDir, target.ID+".log")); err != nil {
		t.Fatal(err)
	}
	scenario.bundle.Runs[0].LogFile = "../../../etc/passwd"

	report, err := importBundle(scenario.controller, scenario.bundle, scenario.mapping, scenario.bundleDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Imported != 2 {
		t.Fatalf("a missing log must not block the import, got %+v", report)
	}
	if !containsSubstring(report.Diagnostics, "no log file for legacy run "+target.ID) {
		t.Fatalf("expected an explicit missing-log diagnostic, got %v", report.Diagnostics)
	}
	for _, execution := range scenario.controller.store.Executions() {
		if execution.Origin.LegacyRunID != target.ID {
			continue
		}
		if execution.Origin.LogImported {
			t.Fatal("the missing log must not be reported as imported")
		}
		if execution.Origin.LogNote == "" {
			t.Fatal("the run must carry the diagnostic note")
		}
		path := executionLogPath(scenario.controller.Runtime().LogDir, execution.ID)
		if _, err := os.Stat(path); err == nil {
			t.Fatal("no log file may be created for a missing log")
		}
	}
	// Nothing outside the log directory was read or written.
	entries, err := os.ReadDir(scenario.controller.Runtime().LogDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one imported log, got %d", len(entries))
	}
}

func TestImportSkipsUnmappedTasksAndMissingJobs(t *testing.T) {
	scenario := newMigrationScenario(t, []LegacyRun{
		{ID: "orphan-task", TaskID: "not-in-mapping", Status: StatusSuccess, RequestedAt: time.Now()},
	})
	delete(scenario.mapping.Tasks, "beta")

	report, err := importBundle(scenario.controller, scenario.bundle, scenario.mapping, scenario.bundleDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Imported != 1 || report.Skipped != 2 {
		t.Fatalf("unexpected report %+v", report)
	}
	if !containsSubstring(report.Diagnostics, "is not in the mapping") {
		t.Fatalf("expected a mapping diagnostic, got %v", report.Diagnostics)
	}

}

// TestImportSkipsMappingsPointingAtAMissingJob keeps a stale mapping from
// inventing a job that the controller does not define.
func TestImportSkipsMappingsPointingAtAMissingJob(t *testing.T) {
	scenario := newMigrationScenario(t, nil)
	scenario.mapping.Tasks["alpha"] = MappingTask{Job: "ghost-job", Project: "alpha"}

	report, err := importBundle(scenario.controller, scenario.bundle, scenario.mapping, scenario.bundleDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Skipped != 1 {
		t.Fatalf("expected the stale mapping to be skipped, got %+v", report)
	}
	if !containsSubstring(report.Diagnostics, "does not exist on this controller") {
		t.Fatalf("expected a missing-job diagnostic, got %v", report.Diagnostics)
	}
}

// TestImportRejectsParametersTheJobNoLongerAccepts keeps mapped history honest.
func TestImportRejectsParametersTheJobNoLongerAccepts(t *testing.T) {
	scenario := newMigrationScenario(t, nil)
	entry := scenario.mapping.Tasks["alpha"]
	entry.Project = "ghost-project"
	scenario.mapping.Tasks["alpha"] = entry

	report, err := importBundle(scenario.controller, scenario.bundle, scenario.mapping, scenario.bundleDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Skipped != 1 {
		t.Fatalf("expected the unmappable run to be skipped, got %+v", report)
	}
	if !containsSubstring(report.Diagnostics, "mapped parameters do not match job") {
		t.Fatalf("expected a parameter diagnostic, got %v", report.Diagnostics)
	}
}

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
	out, err = newTestRoot(t, "migrate", "plan", "--bundle", outDir, "--workspace-root", "/w")
	if err != nil {
		t.Fatalf("migrate plan: %v", err)
	}
	if !strings.Contains(out.String(), "machine: linux") {
		t.Fatalf("expected the drafted mapping on stdout, got %s", out.String())
	}
	if _, statErr := os.Stat(filepath.Join(outDir, mappingFileName)); !os.IsNotExist(statErr) {
		t.Fatal("migrate plan must default to a dry run")
	}
	if _, err := newTestRoot(t, "migrate", "plan", "--bundle", outDir, "--workspace-root", "/w", "--apply"); err != nil {
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

// TestImportRefusesToExceedTheHistoryCap is the regression guard for a silent
// data loss: imported executions are terminal, so appending past the cap would
// prune the controller's own oldest runs and their logs, and would also make a
// repeated import non-idempotent because the pruned entries are re-imported.
func TestImportRefusesToExceedTheHistoryCap(t *testing.T) {
	fixture := newLegacyFixture(t, "linux", "android", []string{"alpha", "beta"}, nil)
	bundleDir := filepath.Join(t.TempDir(), "bundle")
	bundle, _, err := exportLegacyBundle("linux", fixture.ConfigPath, bundleDir, true)
	if err != nil {
		t.Fatal(err)
	}
	mapping, _ := planMigration(bundle, "linux-android", "/home/someone/git/dooroo")
	cfg, _, err := buildRoleConfigs([]*MigrationBundle{bundle}, []*MigrationMapping{mapping})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Server.MaxHistory = 1
	document, err := marshalControllerConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	controller := newTestController(t, string(document))

	report, err := importBundle(controller, bundle, mapping, bundleDir, true)
	if !errors.Is(err, errHistoryCapTooSmall) {
		t.Fatalf("expected the import to be refused, got %v", err)
	}
	if !containsSubstring(report.Diagnostics, "raise it to at least 2") {
		t.Fatalf("the refusal must name the value to raise the cap to, got %v", report.Diagnostics)
	}
	if len(controller.store.Executions()) != 0 {
		t.Fatal("a refused import must not write anything")
	}
	// A dry run reports the same problem before anything is attempted.
	if _, err := importBundle(controller, bundle, mapping, bundleDir, false); !errors.Is(err, errHistoryCapTooSmall) {
		t.Fatalf("a dry run must report the cap problem, got %v", err)
	}
}

// TestImportIsAtomic proves a batch either lands completely or not at all.
func TestImportIsAtomic(t *testing.T) {
	scenario := newMigrationScenario(t, nil)
	var sequenceBefore uint64
	scenario.controller.store.read(func(st *controllerStateData) { sequenceBefore = st.Sequence })

	if _, err := importBundle(scenario.controller, scenario.bundle, scenario.mapping, scenario.bundleDir, true); err != nil {
		t.Fatal(err)
	}
	var sequenceAfter uint64
	scenario.controller.store.read(func(st *controllerStateData) { sequenceAfter = st.Sequence })
	if sequenceAfter != sequenceBefore+1 {
		t.Fatalf("expected one state mutation for the whole batch, sequence went %d -> %d", sequenceBefore, sequenceAfter)
	}
	if len(scenario.controller.store.Executions()) != 2 {
		t.Fatalf("expected both executions, got %d", len(scenario.controller.store.Executions()))
	}
}

// TestDryRunImportLeavesTheTargetUntouched proves a dry run does not even
// create the controller's state directories.
func TestDryRunImportLeavesTheTargetUntouched(t *testing.T) {
	fixture := newLegacyFixture(t, "linux", "android", []string{"alpha"}, nil)
	bundleDir := filepath.Join(t.TempDir(), "bundle")
	bundle, _, err := exportLegacyBundle("linux", fixture.ConfigPath, bundleDir, true)
	if err != nil {
		t.Fatal(err)
	}
	mapping, _ := planMigration(bundle, "linux-android", "/home/someone/git/dooroo")
	cfg, _, err := buildRoleConfigs([]*MigrationBundle{bundle}, []*MigrationMapping{mapping})
	if err != nil {
		t.Fatal(err)
	}
	document, err := marshalControllerConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	configPath := filepath.Join(dir, controllerConfigName)
	if err := os.WriteFile(configPath, document, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadControllerConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := newReadOnlyController(configPath, loaded)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()

	report, err := importBundle(controller, bundle, mapping, bundleDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Imported != 1 {
		t.Fatalf("expected the dry run to report one importable run, got %+v", report)
	}
	for _, path := range []string{controller.Runtime().StateDir, controller.Runtime().LogDir, controller.Runtime().StatePath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("a dry run must not create %s (stat error %v)", path, err)
		}
	}
}
