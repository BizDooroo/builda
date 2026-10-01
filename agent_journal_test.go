package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func newTestJournal(t *testing.T) *agentJournal {
	t.Helper()
	journal, err := newAgentJournal(filepath.Join(t.TempDir(), "exec"))
	if err != nil {
		t.Fatal(err)
	}
	return journal
}

func TestJournalRoundTripAndProtectedMode(t *testing.T) {
	journal := newTestJournal(t)
	entry := &journalEntry{
		ExecutionID: "e1",
		Phase:       journalAccepted,
		AcceptedAt:  time.Now(),
		Assignment:  AgentAssignment{ExecutionID: "e1", JobID: "j", Script: "echo hi"},
		ExitCode:    -1,
	}
	if err := journal.Save(entry); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(journal.entryPath("e1"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("the journal must be written with mode 0600, got %v", info.Mode().Perm())
	}
	loaded, err := journal.Load("e1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Phase != journalAccepted || loaded.Assignment.Script != "echo hi" {
		t.Fatalf("unexpected loaded entry %+v", loaded)
	}
	if ids := journal.IDs(); len(ids) != 1 || ids[0] != "e1" {
		t.Fatalf("unexpected tracked ids %v", ids)
	}
	if err := journal.Save(&journalEntry{}); err == nil {
		t.Fatal("expected an entry without an execution id to be rejected")
	}
	if err := journal.Remove("e1"); err != nil {
		t.Fatal(err)
	}
	if ids := journal.IDs(); len(ids) != 0 {
		t.Fatalf("expected no tracked ids, got %v", ids)
	}
}

func TestJournalListIsOrderedByAcceptance(t *testing.T) {
	journal := newTestJournal(t)
	base := time.Now()
	for index, id := range []string{"third", "first", "second"} {
		offset := []time.Duration{2 * time.Hour, 0, time.Hour}[index]
		if err := journal.Save(&journalEntry{ExecutionID: id, Phase: journalAccepted, AcceptedAt: base.Add(offset)}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := journal.List()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"first", "second", "third"}
	for index, entry := range entries {
		if entry.ExecutionID != want[index] {
			t.Fatalf("entry %d = %q, want %q", index, entry.ExecutionID, want[index])
		}
	}
}

func TestJournalListSkipsUnreadableEntries(t *testing.T) {
	journal := newTestJournal(t)
	if err := journal.Save(&journalEntry{ExecutionID: "good", Phase: journalAccepted}); err != nil {
		t.Fatal(err)
	}
	broken := journal.entryDir("broken")
	if err := os.MkdirAll(broken, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, "journal.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := journal.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ExecutionID != "good" {
		t.Fatalf("unexpected entries %+v", entries)
	}
}

// TestVerifyOrphanNeverStarted covers every phase before process launch.
func TestVerifyOrphanNeverStarted(t *testing.T) {
	for _, phase := range []string{journalAccepted, journalPermitted, journalFinished, journalReported} {
		verdict := verifyOrphan(&journalEntry{ExecutionID: "e", Phase: phase})
		if !verdict.Ended || !verdict.Owned {
			t.Fatalf("phase %q must be treated as never started, got %+v", phase, verdict)
		}
	}
}

// TestVerifyOrphanUnprovableWithoutProcessIdentity is the safety case: the
// agent recorded the intent to start but crashed before it could record the
// pid, so nothing can be proven.
func TestVerifyOrphanUnprovableWithoutProcessIdentity(t *testing.T) {
	verdict := verifyOrphan(&journalEntry{ExecutionID: "e", Phase: journalStarted})
	if verdict.Ended || verdict.Owned {
		t.Fatalf("expected an unprovable verdict, got %+v", verdict)
	}
	if !strings.Contains(verdict.Details, "process identity") {
		t.Fatalf("expected an explanation, got %q", verdict.Details)
	}
}

// TestVerifyOrphanDetectsEndedProcessGroup uses a real short-lived process.
func TestVerifyOrphanDetectsEndedProcessGroup(t *testing.T) {
	pid, pgid, token := startDetachedSleeper(t, "0.05")
	entry := &journalEntry{ExecutionID: "e", Phase: journalStarted, PID: pid, PGID: pgid, ProcessToken: token}

	// While it runs, the verdict is "still running and provably ours".
	verdict := verifyOrphan(entry)
	if verdict.Ended || !verdict.Owned {
		t.Fatalf("a live owned process group must be reported as owned and not ended, got %+v", verdict)
	}

	waitFor(t, "the process group to exit", func() bool {
		return verifyOrphan(entry).Ended
	})
	verdict = verifyOrphan(entry)
	if !verdict.Ended || !verdict.Owned {
		t.Fatalf("expected a proven-ended verdict, got %+v", verdict)
	}
}

// TestVerifyOrphanRefusesToActOnAReusedPid proves the agent never kills an
// unrelated process that happens to hold the recorded pid.
func TestVerifyOrphanRefusesToActOnAReusedPid(t *testing.T) {
	pid, pgid, _ := startDetachedSleeper(t, "5")
	t.Cleanup(func() { _ = killProcessGroup(pgid, syscall.SIGKILL) })

	entry := &journalEntry{
		ExecutionID:  "e",
		Phase:        journalStarted,
		PID:          pid,
		PGID:         pgid,
		ProcessToken: "linux:some-other-boot:12345",
	}
	verdict := verifyOrphan(entry)
	if verdict.Ended {
		t.Fatalf("a reused pid must not be reported as ended, got %+v", verdict)
	}
	if verdict.Owned {
		t.Fatalf("a reused pid must not be reported as owned, got %+v", verdict)
	}
	if !strings.Contains(verdict.Details, "reused") {
		t.Fatalf("expected the reuse explanation, got %q", verdict.Details)
	}
}

// TestProcessStartTokenIsStableAndDisappearsWithTheProcess pins the contract
// the orphan check relies on: for one pid the token never changes while the
// process lives, and it stops resolving once the process is gone.
func TestProcessStartTokenIsStableAndDisappearsWithTheProcess(t *testing.T) {
	pid, pgid, token := startDetachedSleeper(t, "5")
	t.Cleanup(func() { _ = killProcessGroup(pgid, syscall.SIGKILL) })

	again, err := processStartToken(pid)
	if err != nil {
		t.Fatal(err)
	}
	if again != token {
		t.Fatalf("the token must be stable for a live pid: %q then %q", token, again)
	}
	if err := killProcessGroup(pgid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the token to stop resolving", func() bool {
		_, err := processStartToken(pid)
		return err != nil
	})

	if _, err := processStartToken(0); err == nil {
		t.Fatal("expected an invalid pid to be rejected")
	}
	if _, err := processStartToken(1 << 30); err == nil {
		t.Fatal("expected a nonexistent pid to be rejected")
	}
}

func TestProcessGroupExistsClassification(t *testing.T) {
	_, pgid, _ := startDetachedSleeper(t, "5")
	t.Cleanup(func() { _ = killProcessGroup(pgid, syscall.SIGKILL) })
	exists, err := processGroupExists(pgid)
	if err != nil || !exists {
		t.Fatalf("expected the live group to exist, got %v (%v)", exists, err)
	}
	if _, err := processGroupExists(0); err == nil {
		t.Fatal("expected an invalid process group to be rejected")
	}
	if err := killProcessGroup(0, syscall.SIGKILL); err == nil {
		t.Fatal("killing process group 0 must be refused outright")
	}
	if err := killProcessGroup(pgid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the group to disappear", func() bool {
		exists, err := processGroupExists(pgid)
		return err == nil && !exists
	})
}

func TestProcStartTimeParsing(t *testing.T) {
	// A comm field containing spaces and parentheses must not shift the
	// field offsets.
	stat := "1234 (my (weird) prog) S 1 1234 1234 0 -1 4194304 100 0 0 0 1 2 0 0 20 0 1 0 987654 0 0"
	start, err := procStartTime(stat)
	if err != nil {
		t.Fatal(err)
	}
	if start != "987654" {
		t.Fatalf("expected the start time field, got %q", start)
	}
	if _, err := procStartTime("garbage"); err == nil {
		t.Fatal("expected a malformed stat line to be rejected")
	}
	if _, err := procStartTime("1 (a) S 1"); err == nil {
		t.Fatal("expected a truncated stat line to be rejected")
	}
}

// startDetachedSleeper launches a sleep in its own process group and returns
// its pid, process group id, and identity token.
func startDetachedSleeper(t *testing.T, seconds string) (int, int, string) {
	t.Helper()
	attr := &syscall.ProcAttr{Env: os.Environ(), Sys: &syscall.SysProcAttr{Setpgid: true}}
	path, err := findSleepBinary()
	if err != nil {
		t.Skipf("sleep is unavailable: %v", err)
	}
	pid, err := syscall.ForkExec(path, []string{"sleep", seconds}, attr)
	if err != nil {
		t.Fatalf("fork sleep: %v", err)
	}
	go func() {
		var status syscall.WaitStatus
		_, _ = syscall.Wait4(pid, &status, 0, nil)
	}()
	token, err := processStartToken(pid)
	if err != nil {
		t.Skipf("process identity is unavailable on this platform: %v", err)
	}
	if token == "" {
		t.Skip("no process identity token on this platform")
	}
	_ = strconv.Itoa(pid)
	return pid, pid, token
}

func findSleepBinary() (string, error) {
	for _, candidate := range []string{"/bin/sleep", "/usr/bin/sleep"} {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate, nil
		}
	}
	return "", os.ErrNotExist
}
