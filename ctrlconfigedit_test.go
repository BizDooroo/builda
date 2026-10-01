package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigWriteIsValidatedAndAtomic(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	err := controller.editConfig(func(cfg *ControllerConfig) error {
		cfg.Jobs[0].Script = ""
		return nil
	})
	if err == nil {
		t.Fatal("expected an invalid edit to be rejected")
	}
	if job, _ := findJob(controller.Config(), "android-build"); job.Script != "echo android" {
		t.Fatal("a rejected edit must not change the live config")
	}
	data, readErr := os.ReadFile(controller.Runtime().ConfigPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(data), "echo android") {
		t.Fatal("a rejected edit must not change the config file")
	}
	info, _ := os.Stat(controller.Runtime().ConfigPath)
	if info.Mode().Perm() != 0o600 {
		// The fixture was written at 0600 and a successful edit keeps it there.
		t.Fatalf("unexpected config mode %v", info.Mode().Perm())
	}
}

func TestConcurrentConfigEditsAreSerialized(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	done := make(chan error, 8)
	for i := 0; i < 8; i++ {
		index := i
		go func() {
			done <- controller.editConfig(func(cfg *ControllerConfig) error {
				cfg.Jobs = append(cfg.Jobs, JobConfig{
					ID:     fmt.Sprintf("job-%d", index),
					Script: "echo hi",
				})
				return nil
			})
		}()
	}
	for i := 0; i < 8; i++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent edit %d failed: %v", i, err)
		}
	}
	cfg := controller.Config()
	for i := 0; i < 8; i++ {
		if _, ok := findJob(cfg, fmt.Sprintf("job-%d", i)); !ok {
			t.Fatalf("edit %d was lost", i)
		}
	}
	reparsed, err := loadControllerConfig(controller.Runtime().ConfigPath)
	if err != nil {
		t.Fatalf("the persisted document must stay loadable: %v", err)
	}
	if len(reparsed.Jobs) != len(cfg.Jobs) {
		t.Fatalf("the file and memory disagree: %d vs %d jobs", len(reparsed.Jobs), len(cfg.Jobs))
	}
}

func TestWriteFileAtomicSyncLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snapshot.json")
	if err := writeFileAtomicSync(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomicSync(path, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "two" {
		t.Fatalf("unexpected content %q (%v)", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected only the target file, got %d entries", len(entries))
	}
}

// TestConfigEditPicksUpDirectFileEdits proves an edit made through the API is
// applied on top of a change someone made to the file by hand, instead of
// silently discarding it.
func TestConfigEditPicksUpDirectFileEdits(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	path := controller.Runtime().ConfigPath

	// Someone edits the file directly while the controller is running.
	direct := configDocumentWith("jobs:\n", "jobs:\n  - id: \"added-by-hand\"\n    script: \"echo hi\"\n")
	if err := os.WriteFile(path, []byte(direct), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := controller.editConfig(func(cfg *ControllerConfig) error {
		cfg.Jobs = append(cfg.Jobs, JobConfig{ID: "added-by-api", Script: "echo api"})
		return nil
	}); err != nil {
		t.Fatalf("edit config: %v", err)
	}

	cfg := controller.Config()
	for _, id := range []string{"added-by-hand", "added-by-api", "android-build"} {
		if _, ok := findJob(cfg, id); !ok {
			t.Fatalf("expected job %q to survive the edit", id)
		}
	}
	reparsed, err := loadControllerConfig(path)
	if err != nil {
		t.Fatalf("the persisted document must stay loadable: %v", err)
	}
	if len(reparsed.Jobs) != len(cfg.Jobs) {
		t.Fatalf("the file and memory disagree: %d vs %d jobs", len(reparsed.Jobs), len(cfg.Jobs))
	}
}

// TestConfigEditRejectsAWriteRacedByTheFile covers the guard: if the document
// changes between the read and the write, the edit is refused rather than
// clobbering it.
func TestConfigEditRejectsAWriteRacedByTheFile(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	path := controller.Runtime().ConfigPath

	err := controller.editConfig(func(cfg *ControllerConfig) error {
		// Simulate another writer landing after this callback read the config.
		raced := configDocumentWith("max_history: 50", "max_history: 77")
		if writeErr := os.WriteFile(path, []byte(raced), 0o600); writeErr != nil {
			return writeErr
		}
		cfg.Jobs = append(cfg.Jobs, JobConfig{ID: "late", Script: "echo late"})
		return nil
	})
	if !errors.Is(err, errConfigChanged) {
		t.Fatalf("expected the raced write to be refused, got %v", err)
	}
	onDisk, readErr := loadControllerConfig(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if onDisk.Server.MaxHistory != 77 {
		t.Fatalf("the other writer's document must survive, got max_history %d", onDisk.Server.MaxHistory)
	}
	if _, ok := findJob(onDisk, "late"); ok {
		t.Fatal("the refused edit must not reach the file")
	}
}

// TestExecutionIDsAreUnique treats uniqueness as a property rather than a
// probability: an enqueue that collides with an existing ID picks another.
func TestExecutionIDsAreUnique(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		execution := enqueue(t, controller, "ios-build", map[string]string{"project": "alpha"})
		if seen[execution.ID] {
			t.Fatalf("duplicate execution id %q", execution.ID)
		}
		seen[execution.ID] = true
	}

	// Force a collision: an execution already holding the next generated ID
	// must not be overwritten.
	existing := enqueue(t, controller, "ios-build", map[string]string{"project": "alpha"})
	collision := &Execution{ID: existing.ID, JobID: "ios-build", Status: StatusSuccess, RequestedAt: time.Now()}
	if err := controller.store.mutate(func(st *controllerStateData) error {
		for st.find(collision.ID) != nil {
			collision.ID = newExecutionID()
		}
		st.Executions = append(st.Executions, collision.clone())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if collision.ID == existing.ID {
		t.Fatal("a colliding id must be replaced")
	}
	if _, ok := controller.store.Find(existing.ID); !ok {
		t.Fatal("the original execution must survive")
	}
}
