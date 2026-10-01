package main

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestStatePersistsAtomicallyWithRestrictedMode(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	enqueue(t, controller, "ios-build", map[string]string{"project": "alpha"})

	info, err := os.Stat(controller.Runtime().StatePath)
	if err != nil {
		t.Fatalf("state file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state must be written with mode 0600, got %v", info.Mode().Perm())
	}
	entries, err := os.ReadDir(controller.Runtime().StateDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp") {
			t.Fatalf("no temporary file may be left behind, found %q", entry.Name())
		}
	}
}

// TestPersistenceFailureDoesNotAcknowledgeEnqueue is the fail-closed test: if
// the snapshot cannot be written, the caller is told the enqueue failed and no
// execution appears in memory.
func TestPersistenceFailureDoesNotAcknowledgeEnqueue(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	before := len(controller.store.Executions())

	// Replacing the state directory with an unwritable file makes the atomic
	// write fail at the temporary-file stage.
	stateDir := controller.Runtime().StateDir
	if err := os.RemoveAll(stateDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := controller.Enqueue("ios-build", parameterValuesToQuery(map[string]string{"project": "alpha"}), "test"); err == nil {
		t.Fatal("expected the enqueue to fail when the state cannot be persisted")
	} else if !strings.Contains(err.Error(), "persist controller state") {
		t.Fatalf("expected a persistence error, got %v", err)
	}
	if got := len(controller.store.Executions()); got != before {
		t.Fatalf("a failed persist must not leave the execution in memory, have %d want %d", got, before)
	}
}

// TestPersistenceFailureDoesNotAcknowledgeAssignmentOrStart covers the other
// two acknowledgements that must fail closed.
func TestPersistenceFailureDoesNotAcknowledgeAssignmentOrStart(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	markOnline(controller, "linux-one")
	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	finishExecution(t, controller, execution.ID, StatusSuccess)

	queued := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	if statusOf(t, controller, queued.ID) != StatusAssigned {
		t.Fatal("precondition: the execution should be assigned")
	}
	// Break persistence, then try to grant the start permit.
	stateDir := controller.Runtime().StateDir
	if err := os.RemoveAll(stateDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.GrantPermit("linux-one", queued.ID); err == nil {
		t.Fatal("expected the start permit to fail when it cannot be persisted")
	}
	if statusOf(t, controller, queued.ID) != StatusAssigned {
		t.Fatal("a failed permit persist must leave the execution assigned, not running")
	}

	// Scheduling must also refuse to record an assignment it cannot persist.
	controller.store.read(func(st *controllerStateData) {
		for _, candidate := range st.Executions {
			if candidate.ID == queued.ID {
				candidate.Status = StatusQueued
				candidate.AgentID = ""
			}
		}
	})
	if err := controller.scheduleOnce(); err == nil {
		t.Fatal("expected scheduling to fail when the assignment cannot be persisted")
	}
	if statusOf(t, controller, queued.ID) != StatusQueued {
		t.Fatal("a failed assignment persist must leave the execution queued")
	}
}

func TestHistoryCapPrunesOnlyTerminalExecutions(t *testing.T) {
	document := configDocumentWith("max_history: 50", "max_history: 3")
	controller := newTestController(t, document)
	markOnline(controller, "mac-one")

	// One queued execution that can never run plus five finished ones.
	stuck := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	finished := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		execution := enqueue(t, controller, "ios-build", map[string]string{"project": "alpha"})
		finishExecution(t, controller, execution.ID, StatusSuccess)
		finished = append(finished, execution.ID)
	}

	terminal := 0
	for _, execution := range controller.store.Executions() {
		if isExecutionTerminal(execution.Status) {
			terminal++
		}
	}
	if terminal != 3 {
		t.Fatalf("expected the terminal history to be capped at 3, got %d", terminal)
	}
	if statusOf(t, controller, stuck.ID) != StatusQueued {
		t.Fatal("a queued execution must never be pruned")
	}
	// The oldest terminal executions and their logs are the ones removed.
	for _, id := range finished[:2] {
		if _, ok := controller.store.Find(id); ok {
			t.Fatalf("expected %s to be pruned", id)
		}
	}
}

func TestDeleteExecutionRemovesRecordAndLog(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	markOnline(controller, "mac-one")
	execution := enqueue(t, controller, "ios-build", map[string]string{"project": "alpha"})

	if err := controller.store.DeleteExecution(execution.ID); !errors.Is(err, errExecutionActive) {
		t.Fatalf("expected an active execution to be undeletable, got %v", err)
	}
	finishExecution(t, controller, execution.ID, StatusSuccess)

	logPath := executionLogPath(controller.Runtime().LogDir, execution.ID)
	if err := os.WriteFile(logPath, []byte("log body"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := controller.store.DeleteExecution(execution.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := controller.store.Find(execution.ID); ok {
		t.Fatal("the execution record must be gone")
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("the derived log file must be removed, stat error %v", err)
	}
	if err := controller.store.DeleteExecution("missing"); !errors.Is(err, errExecutionNotFound) {
		t.Fatalf("expected a not-found error, got %v", err)
	}
}

// TestControllerRestartKeepsRunningWorkAndResumesQueue proves a restart does
// not abort live work and does not lose the queue.
func TestControllerRestartKeepsRunningWorkAndResumesQueue(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	markOnline(controller, "linux-one")
	running := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	if _, err := controller.GrantPermit("linux-one", running.ID); err != nil {
		t.Fatal(err)
	}
	queued := enqueue(t, controller, "android-build", map[string]string{"project": "beta"})

	restarted := reloadController(t, controller)
	if got := statusOf(t, restarted, running.ID); got != StatusRunning {
		t.Fatalf("a controller restart must not abort running work, got %q", got)
	}
	if got := statusOf(t, restarted, queued.ID); got != StatusQueued {
		t.Fatalf("the queue must survive a restart, got %q", got)
	}
	// Every agent is offline until it polls again, so nothing is reassigned.
	if restarted.agentOnline("linux-one") {
		t.Fatal("liveness must not be restored from disk")
	}
	if got := assignedAgent(t, restarted, running.ID); got != "linux-one" {
		t.Fatalf("ownership must survive the restart, got %q", got)
	}
}

// TestControllerRestartRecoversDurableLogOffsets proves the log file, not the
// snapshot, is the authoritative record of accepted bytes.
func TestControllerRestartRecoversDurableLogOffsets(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	markOnline(controller, "linux-one")
	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	if _, err := controller.GrantPermit("linux-one", execution.ID); err != nil {
		t.Fatal(err)
	}
	body := []byte("line one\nline two\n")
	acked, err := controller.AppendLog("linux-one", AgentLogRequest{ExecutionID: execution.ID, Offset: 0, Data: body})
	if err != nil {
		t.Fatal(err)
	}
	if acked != int64(len(body)) {
		t.Fatalf("expected %d acknowledged bytes, got %d", len(body), acked)
	}

	restarted := reloadController(t, controller)
	stored := mustFind(t, restarted, execution.ID)
	if stored.LogOffset != int64(len(body)) {
		t.Fatalf("the restart must recover the durable offset, got %d", stored.LogOffset)
	}
}

// TestImportedOriginLedgerIdentifiesLegacyRuns pins the compound key: the
// machine and the legacy run ID together identify a run, and neither alone
// does, so two machines reusing a run ID stay distinct.
func TestImportedOriginLedgerIdentifiesLegacyRuns(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	if err := controller.store.mutate(func(st *controllerStateData) error {
		st.ImportedOrigins = append(st.ImportedOrigins,
			ImportedOrigin{Machine: "mm", LegacyRunID: "legacy-1", ExecutionID: "e1", ImportedAt: time.Now()},
			ImportedOrigin{Machine: "linux", LegacyRunID: "legacy-2", ExecutionID: "e2", ImportedAt: time.Now()},
		)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !controller.store.HasImportedOrigin("mm", "legacy-1") {
		t.Fatal("the imported run must be recognised")
	}
	if controller.store.HasImportedOrigin("linux", "legacy-1") {
		t.Fatal("the machine must be part of the identity")
	}
	if controller.store.HasImportedOrigin("mm", "legacy-2") {
		t.Fatal("the legacy run id must be part of the identity")
	}
	// A key built by joining the two fields could collide; the struct key
	// used here cannot.
	if err := controller.store.mutate(func(st *controllerStateData) error {
		st.ImportedOrigins = append(st.ImportedOrigins,
			ImportedOrigin{Machine: "a", LegacyRunID: "b:c", ExecutionID: "e3"},
			ImportedOrigin{Machine: "a:b", LegacyRunID: "c", ExecutionID: "e4"},
		)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !controller.store.HasImportedOrigin("a", "b:c") || !controller.store.HasImportedOrigin("a:b", "c") {
		t.Fatal("both keys must be recognised independently")
	}
	if controller.store.HasImportedOrigin("a", "b") || controller.store.HasImportedOrigin("a:b:c", "") {
		t.Fatal("no joined form may be treated as a match")
	}
}
func TestExecutionSnapshotIsImmutableAfterConfigEdit(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	execution := enqueue(t, controller, "ios-build", map[string]string{"project": "alpha"})

	// Rewrite the job with a different script and timeout.
	if err := controller.editConfig(func(cfg *ControllerConfig) error {
		for index := range cfg.Jobs {
			if cfg.Jobs[index].ID == "ios-build" {
				cfg.Jobs[index].Script = "echo replaced"
				cfg.Jobs[index].Timeout = "10m"
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("edit config: %v", err)
	}

	stored := mustFind(t, controller, execution.ID)
	if stored.Script != "echo ios" || stored.TimeoutText != "2h" {
		t.Fatalf("the enqueue-time snapshot must not change, got script %q timeout %q", stored.Script, stored.TimeoutText)
	}
	if stored.JobSnapshot.Script != "echo ios" {
		t.Fatal("the job snapshot must not change")
	}
	updated, _ := findJob(controller.Config(), "ios-build")
	if updated.Script != "echo replaced" {
		t.Fatal("the live job must reflect the edit")
	}
}

// TestNewCatalogEntryUpdatesBothJobSelectionLists proves one new project
// reaches every applicable job without touching the jobs.
func TestNewCatalogEntryUpdatesBothJobSelectionLists(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	if err := controller.editConfig(func(cfg *ControllerConfig) error {
		cfg.Catalogs[0].Options = append(cfg.Catalogs[0].Options, OptionConfig{
			Value:  "gamma",
			Label:  "Gamma",
			Labels: []string{"android", "ios"},
			Values: map[string]string{optionPathField: "gamma"},
		})
		return nil
	}); err != nil {
		t.Fatalf("edit config: %v", err)
	}
	cfg := controller.Config()
	for _, jobID := range []string{"android-build", "ios-build"} {
		job, _ := findJob(cfg, jobID)
		if !optionValueExists(parameterOptions(cfg, job.Parameters[0]), "gamma") {
			t.Fatalf("job %s should offer the new catalog entry", jobID)
		}
	}
	// And it is immediately selectable.
	for _, jobID := range []string{"android-build", "ios-build"} {
		if _, err := controller.Enqueue(jobID, parameterValuesToQuery(map[string]string{"project": "gamma"}), "test"); err != nil {
			t.Fatalf("enqueue %s with the new project: %v", jobID, err)
		}
	}
}
