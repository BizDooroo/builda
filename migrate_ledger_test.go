package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// finishOneRun drives a fresh execution to a terminal state so the controller
// accumulates history of its own.
func finishOneRun(t *testing.T, controller *Controller, jobID string, values map[string]string) string {
	t.Helper()
	execution := enqueue(t, controller, jobID, values)
	finishExecution(t, controller, execution.ID, StatusSuccess)
	return execution.ID
}

// TestReimportStaysANoOpAfterPruning is the regression guard for idempotence
// that outlives the execution: the ledger, not the surviving history, decides
// whether a legacy run was already imported.
func TestReimportStaysANoOpAfterPruning(t *testing.T) {
	scenario := newMigrationScenario(t, nil)
	markOnline(scenario.controller, "linux-android")

	report, err := importBundle(scenario.controller, scenario.bundle, scenario.mapping, scenario.bundleDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Imported != 2 {
		t.Fatalf("expected two imports, got %+v", report)
	}
	if origins := scenario.controller.store.ImportedOrigins(); len(origins) != 2 {
		t.Fatalf("expected two ledger entries, got %d", len(origins))
	}

	// Ordinary operation now prunes the imported executions out of history.
	if err := scenario.controller.store.SetMaxHistory(1); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		finishOneRun(t, scenario.controller, "android-build", map[string]string{"project": "alpha"})
	}
	for _, execution := range scenario.controller.store.Executions() {
		if execution.Origin != nil {
			t.Fatal("the imported executions should have been pruned by now")
		}
	}

	// Re-importing must still be a no-op, and must not re-add history that
	// the cap would immediately prune again.
	report, err = importBundle(scenario.controller, scenario.bundle, scenario.mapping, scenario.bundleDir, true)
	if err != nil {
		t.Fatalf("a repeated import must not fail: %v", err)
	}
	if report.Imported != 0 || report.Existing != 2 {
		t.Fatalf("a pruned import must still count as already imported, got %+v", report)
	}
}

// TestReimportStaysANoOpAfterDeletion covers an operator deleting a migrated
// run from the history.
func TestReimportStaysANoOpAfterDeletion(t *testing.T) {
	scenario := newMigrationScenario(t, nil)
	if _, err := importBundle(scenario.controller, scenario.bundle, scenario.mapping, scenario.bundleDir, true); err != nil {
		t.Fatal(err)
	}
	for _, execution := range scenario.controller.store.Executions() {
		if err := scenario.controller.store.DeleteExecution(execution.ID); err != nil {
			t.Fatal(err)
		}
	}
	if len(scenario.controller.store.Executions()) != 0 {
		t.Fatal("precondition: the history should be empty")
	}

	report, err := importBundle(scenario.controller, scenario.bundle, scenario.mapping, scenario.bundleDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Imported != 0 || report.Existing != 2 {
		t.Fatalf("a deleted run must not be restored by a re-import, got %+v", report)
	}
	if len(scenario.controller.store.Executions()) != 0 {
		t.Fatal("a re-import must not resurrect deleted history")
	}
}

// TestLedgerSurvivesAControllerRestart proves the ledger is persisted.
func TestLedgerSurvivesAControllerRestart(t *testing.T) {
	scenario := newMigrationScenario(t, nil)
	if _, err := importBundle(scenario.controller, scenario.bundle, scenario.mapping, scenario.bundleDir, true); err != nil {
		t.Fatal(err)
	}
	restarted := reloadController(t, scenario.controller)
	if origins := restarted.store.ImportedOrigins(); len(origins) != 2 {
		t.Fatalf("the ledger must survive a restart, got %d entries", len(origins))
	}
	report, err := importBundle(restarted, scenario.bundle, scenario.mapping, scenario.bundleDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Imported != 0 || report.Existing != 2 {
		t.Fatalf("a re-import after a restart must be a no-op, got %+v", report)
	}
}

// TestLedgerIsBackfilledFromOlderSnapshots keeps a controller upgraded from a
// snapshot written before the ledger existed idempotent.
func TestLedgerIsBackfilledFromOlderSnapshots(t *testing.T) {
	scenario := newMigrationScenario(t, nil)
	if _, err := importBundle(scenario.controller, scenario.bundle, scenario.mapping, scenario.bundleDir, true); err != nil {
		t.Fatal(err)
	}
	// Simulate the older snapshot shape: executions keep their origin, but the
	// ledger does not exist.
	statePath := scenario.controller.Runtime().StatePath
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state controllerStateData
	if err := jsonUnmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.ImportedOrigins) == 0 {
		t.Fatal("precondition: the snapshot should carry a ledger")
	}
	state.ImportedOrigins = nil
	rewritten, err := jsonMarshalIndent(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, rewritten, 0o600); err != nil {
		t.Fatal(err)
	}

	restarted := reloadController(t, scenario.controller)
	if origins := restarted.store.ImportedOrigins(); len(origins) != 2 {
		t.Fatalf("the ledger must be backfilled from retained executions, got %d", len(origins))
	}
	report, err := importBundle(restarted, scenario.bundle, scenario.mapping, scenario.bundleDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Imported != 0 || report.Existing != 2 {
		t.Fatalf("an upgraded controller must stay idempotent, got %+v", report)
	}
}

// TestDuplicateOriginsInOneBundleAreSkippedDeterministically covers a bundle
// that lists the same legacy run twice.
func TestDuplicateOriginsInOneBundleAreSkippedDeterministically(t *testing.T) {
	scenario := newMigrationScenario(t, nil)
	duplicated := *scenario.bundle
	duplicated.Runs = append(append([]LegacyRun(nil), scenario.bundle.Runs...), scenario.bundle.Runs...)

	report, err := importBundle(scenario.controller, &duplicated, scenario.mapping, scenario.bundleDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Imported != 2 {
		t.Fatalf("each legacy run must be imported once, got %+v", report)
	}
	if report.Skipped != 2 || !containsSubstring(report.Diagnostics, "skip duplicate entry for legacy run") {
		t.Fatalf("the duplicates must be reported, got %+v", report)
	}
	if len(scenario.controller.store.Executions()) != 2 {
		t.Fatalf("expected two executions, got %d", len(scenario.controller.store.Executions()))
	}
	if len(scenario.controller.store.ImportedOrigins()) != 2 {
		t.Fatalf("expected two ledger entries, got %d", len(scenario.controller.store.ImportedOrigins()))
	}
}

// TestZeroImportLeavesTheSnapshotUntouched proves an import with nothing to do
// does not rewrite the state, which also means it cannot prune.
func TestZeroImportLeavesTheSnapshotUntouched(t *testing.T) {
	scenario := newMigrationScenario(t, nil)
	if _, err := importBundle(scenario.controller, scenario.bundle, scenario.mapping, scenario.bundleDir, true); err != nil {
		t.Fatal(err)
	}
	var sequenceBefore uint64
	scenario.controller.store.read(func(st *controllerStateData) { sequenceBefore = st.Sequence })
	stateBefore, err := os.ReadFile(scenario.controller.Runtime().StatePath)
	if err != nil {
		t.Fatal(err)
	}

	report, err := importBundle(scenario.controller, scenario.bundle, scenario.mapping, scenario.bundleDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Imported != 0 {
		t.Fatalf("expected nothing to import, got %+v", report)
	}
	var sequenceAfter uint64
	scenario.controller.store.read(func(st *controllerStateData) { sequenceAfter = st.Sequence })
	if sequenceAfter != sequenceBefore {
		t.Fatalf("an import with nothing to do must not mutate the state, sequence %d -> %d", sequenceBefore, sequenceAfter)
	}
	stateAfter, err := os.ReadFile(scenario.controller.Runtime().StatePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(stateBefore) != string(stateAfter) {
		t.Fatal("the snapshot file must be byte-identical after a no-op import")
	}
}

// TestImportRollsBackWhenALogCannotBeCopied proves a failed copy leaves no
// execution, no ledger entry, and no stray file, so a retry simply works.
func TestImportRollsBackWhenALogCannotBeCopied(t *testing.T) {
	scenario := newMigrationScenario(t, nil)
	logDir := scenario.controller.Runtime().LogDir

	// Make the second log unreadable, which fails the copy mid-batch.
	broken := filepath.Join(scenario.bundleDir, bundleLogDir, "linux-run-beta.log")
	if err := os.Chmod(broken, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(broken, 0o600) })
	if os.Geteuid() == 0 {
		t.Skip("running as root, so an unreadable file is still readable")
	}

	_, err := importBundle(scenario.controller, scenario.bundle, scenario.mapping, scenario.bundleDir, true)
	if !errors.Is(err, errImportLogCopy) {
		t.Fatalf("expected the import to fail on the log copy, got %v", err)
	}
	if len(scenario.controller.store.Executions()) != 0 {
		t.Fatal("a failed import must not leave executions behind")
	}
	if len(scenario.controller.store.ImportedOrigins()) != 0 {
		t.Fatal("a failed import must not record anything in the ledger")
	}
	entries, err := os.ReadDir(logDir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".log") {
			t.Fatalf("a failed import left %s behind", entry.Name())
		}
	}
	// The source logs are untouched.
	if _, err := os.Stat(filepath.Join(scenario.bundleDir, bundleLogDir, "linux-run-alpha.log")); err != nil {
		t.Fatalf("the bundle must be left intact: %v", err)
	}

	// Fix the cause and retry: the whole batch lands.
	if err := os.Chmod(broken, 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := importBundle(scenario.controller, scenario.bundle, scenario.mapping, scenario.bundleDir, true)
	if err != nil {
		t.Fatalf("the retry must succeed: %v", err)
	}
	if report.Imported != 2 {
		t.Fatalf("the retry must import the whole batch, got %+v", report)
	}
	for _, execution := range scenario.controller.store.Executions() {
		if _, err := os.Stat(executionLogPath(logDir, execution.ID)); err != nil {
			t.Fatalf("every imported run should have its log: %v", err)
		}
	}
}

// TestImportRollsBackWhenStateCannotBePersisted proves the copied logs are
// removed when the commit fails, so a retry is not blocked by its own debris.
func TestImportRollsBackWhenStateCannotBePersisted(t *testing.T) {
	scenario := newMigrationScenario(t, nil)
	logDir := scenario.controller.Runtime().LogDir
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Break the state file's directory so the atomic write cannot land.
	statePath := scenario.controller.Runtime().StatePath
	stateDir := filepath.Dir(statePath)
	blocked := filepath.Join(stateDir, "blocked")
	if err := os.MkdirAll(blocked, 0o500); err != nil {
		t.Fatal(err)
	}
	scenario.controller.store.statePath = filepath.Join(blocked, "state.json")
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })
	if os.Geteuid() == 0 {
		t.Skip("running as root, so a read-only directory is still writable")
	}

	_, err := importBundle(scenario.controller, scenario.bundle, scenario.mapping, scenario.bundleDir, true)
	if err == nil {
		t.Fatal("expected the import to fail when the state cannot be persisted")
	}
	entries, readErr := os.ReadDir(logDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".log") {
			t.Fatalf("a failed commit must remove the log it copied, found %s", entry.Name())
		}
	}
	if len(scenario.controller.store.Executions()) != 0 {
		t.Fatal("a failed commit must leave no executions")
	}
	if len(scenario.controller.store.ImportedOrigins()) != 0 {
		t.Fatal("a failed commit must leave no ledger entries")
	}

	// Restore persistence and retry.
	scenario.controller.store.statePath = statePath
	report, err := importBundle(scenario.controller, scenario.bundle, scenario.mapping, scenario.bundleDir, true)
	if err != nil {
		t.Fatalf("the retry must succeed: %v", err)
	}
	if report.Imported != 2 {
		t.Fatalf("the retry must import the whole batch, got %+v", report)
	}
}

// TestImportedLogsAreDurableBeforeTheCommit checks the ordering the recovery
// story depends on: no state references a log that is not already on disk.
func TestImportedLogsAreDurableBeforeTheCommit(t *testing.T) {
	scenario := newMigrationScenario(t, nil)
	if _, err := importBundle(scenario.controller, scenario.bundle, scenario.mapping, scenario.bundleDir, true); err != nil {
		t.Fatal(err)
	}
	restarted := reloadController(t, scenario.controller)
	for _, execution := range restarted.store.Executions() {
		path := executionLogPath(restarted.Runtime().LogDir, execution.ID)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("every committed execution must have its log on disk: %v", err)
		}
		if info.Size() == 0 {
			t.Fatalf("the log for %s is empty", execution.ID)
		}
		if execution.LogOffset != info.Size() {
			t.Fatalf("the recovered offset should match the file, got %d want %d", execution.LogOffset, info.Size())
		}
	}
	_ = time.Now()
}
