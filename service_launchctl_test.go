package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeLaunchctl installs a stand-in for launchctl on PATH so the real plan
// runner can be exercised end to end. It models the two behaviours that made
// the original install unreliable: bootout returns before the job is actually
// gone, and bootstrap refuses a label that is still loaded.
const fakeLaunchctlScript = `#!/usr/bin/env bash
state="$FAKE_LAUNCHCTL_STATE"
log="$FAKE_LAUNCHCTL_LOG"
echo "$@" >> "$log"
read -r status plist < "$state" 2>/dev/null || { status=unloaded; plist=; }

fail() { echo "$2" >&2; exit "$1"; }

case "$1" in
  bootout)
    if [ -n "$FAKE_LAUNCHCTL_REFUSE_BOOTOUT" ]; then
      fail 1 "Boot-out failed: 1: Operation not permitted while System Integrity Protection is engaged"
    fi
    if [ "$status" = loaded ]; then
      # launchd accepts the request and tears the job down asynchronously.
      echo "unloading $plist" > "$state"
      exit 0
    fi
    fail 113 "Could not find service \"$2\" in domain for login"
    ;;
  print)
    if [ "$status" = loaded ]; then
      echo "state = running"
      echo "path = $plist"
      exit 0
    fi
    if [ "$status" = unloading ]; then
      # Still winding down on this call, gone on the next one.
      echo "unloaded" > "$state"
      echo "state = exited"
      echo "path = $plist"
      exit 0
    fi
    fail 113 "Could not find service \"$2\" in domain for login"
    ;;
  enable|disable)
    exit 0
    ;;
  bootstrap)
    if [ "$status" = loaded ]; then
      fail 37 "Bootstrap failed: 37: Operation already in progress"
    fi
    echo "loaded $3" > "$state"
    exit 0
    ;;
  kickstart)
    [ "$status" = loaded ] || fail 113 "Could not find service"
    exit 0
    ;;
esac
fail 64 "unsupported fake launchctl invocation: $*"
`

type fakeLaunchd struct {
	statePath string
	logPath   string
}

func newFakeLaunchd(t *testing.T) *fakeLaunchd {
	t.Helper()
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(binDir, "launchctl")
	if err := os.WriteFile(script, []byte(fakeLaunchctlScript), 0o755); err != nil {
		t.Fatal(err)
	}
	fake := &fakeLaunchd{
		statePath: filepath.Join(dir, "state"),
		logPath:   filepath.Join(dir, "commands.log"),
	}
	if err := os.WriteFile(fake.statePath, []byte("unloaded\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fake.logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_LAUNCHCTL_STATE", fake.statePath)
	t.Setenv("FAKE_LAUNCHCTL_LOG", fake.logPath)
	return fake
}

func (f *fakeLaunchd) status(t *testing.T) (string, string) {
	t.Helper()
	data, err := os.ReadFile(f.statePath)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return "", ""
	}
	if len(fields) == 1 {
		return fields[0], ""
	}
	return fields[0], fields[1]
}

func (f *fakeLaunchd) commands(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(f.logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}

func (f *fakeLaunchd) reset(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(f.logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func runPlan(t *testing.T, commands []serviceCommand) error {
	t.Helper()
	var out bytes.Buffer
	return runServiceCommands(&out, commands)
}

// TestLaunchdInstallRunsAgainstAFakeLaunchctl drives the real plan runner, so
// it covers the asynchronous teardown and the load verification rather than
// just the shape of the plan.
func TestLaunchdInstallRunsAgainstAFakeLaunchctl(t *testing.T) {
	fake := newFakeLaunchd(t)
	plist := filepath.Join(t.TempDir(), "com.bizdooroo.builda.controller.plist")
	spec := serviceSpec{Name: controllerServiceName, Role: RoleController, TargetOS: "darwin"}

	plan, err := serviceEnablePlan(spec, plist, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := runPlan(t, plan); err != nil {
		t.Fatalf("installing into an empty domain must succeed: %v", err)
	}
	status, loadedPath := fake.status(t)
	if status != "loaded" || loadedPath != plist {
		t.Fatalf("expected the new plist to be loaded, got %s %s", status, loadedPath)
	}
	for _, line := range fake.commands(t) {
		if strings.Contains(line, "kickstart") {
			t.Fatalf("install must not kickstart: %q", line)
		}
	}

	// Re-installing over a loaded job waits for the teardown and loads again,
	// which is the case that used to leave the old job running.
	fake.reset(t)
	if err := runPlan(t, plan); err != nil {
		t.Fatalf("re-installing over a loaded job must succeed: %v", err)
	}
	status, loadedPath = fake.status(t)
	if status != "loaded" || loadedPath != plist {
		t.Fatalf("expected the job to be loaded again, got %s %s", status, loadedPath)
	}
	commands := strings.Join(fake.commands(t), "\n")
	if !strings.Contains(commands, "bootout") || !strings.Contains(commands, "bootstrap") {
		t.Fatalf("expected a teardown and a load, got:\n%s", commands)
	}
}

// TestLaunchdInstallFailsLoudlyOnARefusedBootout is the regression guard for
// the worst defect found in review: a permission refusal was classified as
// "already gone", so install reported success while launchd kept running the
// previously loaded job.
func TestLaunchdInstallFailsLoudlyOnARefusedBootout(t *testing.T) {
	fake := newFakeLaunchd(t)
	plist := filepath.Join(t.TempDir(), "com.bizdooroo.builda.controller.plist")
	spec := serviceSpec{Name: controllerServiceName, Role: RoleController, TargetOS: "darwin"}
	plan, err := serviceEnablePlan(spec, plist, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := runPlan(t, plan); err != nil {
		t.Fatal(err)
	}

	// The old job is loaded and launchd refuses to remove it.
	t.Setenv("FAKE_LAUNCHCTL_REFUSE_BOOTOUT", "1")
	fake.reset(t)
	err = runPlan(t, plan)
	if err == nil {
		t.Fatal("a refused teardown must fail the install instead of being swallowed")
	}
	if !strings.Contains(err.Error(), "Operation not permitted") {
		t.Fatalf("the operator needs the refusal in the error, got %v", err)
	}
	if status, _ := fake.status(t); status != "loaded" {
		t.Fatalf("the previously loaded job is still running, which is why the install must fail; got %s", status)
	}
}

// TestLaunchdStartAndRestartAreIdempotent covers the control actions against
// the fake, including that a restart never double-starts.
func TestLaunchdStartAndRestartAreIdempotent(t *testing.T) {
	fake := newFakeLaunchd(t)
	plist := filepath.Join(t.TempDir(), "com.bizdooroo.builda.controller.plist")

	start, err := serviceControlCommands("darwin", controllerServiceName, plist, "start")
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		fake.reset(t)
		if err := runPlan(t, start); err != nil {
			t.Fatalf("start attempt %d failed: %v", attempt, err)
		}
		if status, _ := fake.status(t); status != "loaded" {
			t.Fatalf("after start attempt %d the job should be loaded, got %s", attempt, status)
		}
		for _, line := range fake.commands(t) {
			if strings.Contains(line, "-k") {
				t.Fatalf("start must never kill the job: %q", line)
			}
		}
	}

	restart, err := serviceControlCommands("darwin", controllerServiceName, plist, "restart")
	if err != nil {
		t.Fatal(err)
	}
	fake.reset(t)
	if err := runPlan(t, restart); err != nil {
		t.Fatalf("restart failed: %v", err)
	}
	if status, _ := fake.status(t); status != "loaded" {
		t.Fatalf("after a restart the job should be loaded, got %s", status)
	}
	commands := fake.commands(t)
	bootstraps := 0
	for _, line := range commands {
		if strings.HasPrefix(line, "bootstrap") {
			bootstraps++
		}
		if strings.Contains(line, "-k") {
			t.Fatalf("restart must not kill a job it just started: %q", line)
		}
	}
	if bootstraps != 1 {
		t.Fatalf("a restart must load the job exactly once, got %d bootstraps in %v", bootstraps, commands)
	}

	// Stopping is idempotent and leaves the domain empty.
	stop, err := serviceControlCommands("darwin", controllerServiceName, plist, "stop")
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := runPlan(t, stop); err != nil {
			t.Fatalf("stop attempt %d failed: %v", attempt, err)
		}
		if status, _ := fake.status(t); status != "unloaded" {
			t.Fatalf("after stop attempt %d the job should be unloaded, got %s", attempt, status)
		}
	}
}

// TestLaunchdVerificationRejectsAForeignJob proves the load probe is bound to
// the plist this install wrote, so a different job already occupying the
// label cannot be reported as a successful install.
func TestLaunchdVerificationRejectsAForeignJob(t *testing.T) {
	fake := newFakeLaunchd(t)
	ours := filepath.Join(t.TempDir(), "ours.plist")
	theirs := filepath.Join(t.TempDir(), "theirs.plist")
	if err := os.WriteFile(fake.statePath, []byte("loaded "+theirs+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	start, err := serviceControlCommands("darwin", controllerServiceName, ours, "start")
	if err != nil {
		t.Fatal(err)
	}
	// Shorten the probe so the test does not wait out the real deadline.
	for index := range start {
		if start[index].Probe {
			start[index].Deadline = 300_000_000
		}
	}
	err = runPlan(t, start)
	if err == nil {
		t.Fatal("a label occupied by another plist must not be reported as started")
	}
	if !strings.Contains(err.Error(), ours) {
		t.Fatalf("the error must name the plist that was expected, got %v", err)
	}
}
