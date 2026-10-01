package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// planStrings renders a plan as "tool arg arg" lines for comparison.
func planStrings(commands []serviceCommand) []string {
	lines := make([]string, 0, len(commands))
	for _, command := range commands {
		lines = append(lines, strings.TrimSpace(command.Name+" "+strings.Join(command.Args, " ")))
	}
	return lines
}

func darwinDomain() string {
	return "gui/" + strconv.Itoa(os.Getuid())
}

func darwinTarget() string {
	return darwinDomain() + "/com.bizdooroo.builda.controller"
}

func requirePlan(t *testing.T, got, want []string, what string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("%s plan =\n  %v\nwant\n  %v", what, got, want)
	}
}

// requireNoKickstartKill is the regression guard for the install failure: a
// plist with RunAtLoad is already started by bootstrap, so killing it and
// starting again raced the old listener and produced a crash loop.
func requireNoKickstartKill(t *testing.T, commands []serviceCommand, what string) {
	t.Helper()
	for _, command := range commands {
		for _, arg := range command.Args {
			if arg == "-k" {
				t.Fatalf("%s must never kill a job to start it: %v", what, planStrings(commands))
			}
		}
	}
}

func TestLaunchdInstallPlanVerifiesUnloadAndLoad(t *testing.T) {
	plistPath := filepath.Join(t.TempDir(), "com.bizdooroo.builda.controller.plist")
	spec := serviceSpec{Name: controllerServiceName, Role: RoleController, TargetOS: "darwin"}

	plan, err := serviceEnablePlan(spec, plistPath, true)
	if err != nil {
		t.Fatalf("enable plan: %v", err)
	}
	requirePlan(t, planStrings(plan), []string{
		"launchctl bootout " + darwinTarget(),
		"launchctl print " + darwinTarget(),
		"launchctl enable " + darwinTarget(),
		"launchctl bootstrap " + darwinDomain() + " " + plistPath,
		"launchctl print " + darwinTarget(),
	}, "install")
	requireNoKickstartKill(t, plan, "install")

	// bootout is asynchronous, so the plan waits for the label to disappear
	// before loading anything.
	if !plan[0].IgnoreMissing || !plan[0].IgnoreBusy {
		t.Fatalf("bootout must tolerate a missing job and an in-progress teardown: %+v", plan[0])
	}
	settle := plan[1]
	if !settle.Probe || settle.ExpectSuccess || settle.Deadline <= 0 {
		t.Fatalf("install must wait for the unload to finish: %+v", settle)
	}
	// Because the unload was proven, an "already loaded" bootstrap here is a
	// genuine surprise and must fail instead of being swallowed.
	bootstrap := plan[3]
	if bootstrap.IgnoreLoaded {
		t.Fatal("install must not tolerate an already loaded job after a verified unload")
	}
	if !strings.Contains(bootstrap.Hint, "GUI domain") {
		t.Fatalf("bootstrap needs the actionable domain hint, got %q", bootstrap.Hint)
	}
	// Loading is proven, and the loaded job must be the plist just written.
	verify := plan[4]
	if !verify.Probe || !verify.ExpectSuccess || verify.ExpectOutput != plistPath {
		t.Fatalf("install must prove the loaded job references the new plist: %+v", verify)
	}
}

func TestLaunchdInstallPlanOrdersEnableBeforeBootstrap(t *testing.T) {
	plistPath := filepath.Join(t.TempDir(), "agent.plist")
	spec := serviceSpec{Name: agentServiceName, Role: RoleAgent, TargetOS: "darwin"}
	plan, err := serviceEnablePlan(spec, plistPath, true)
	if err != nil {
		t.Fatalf("enable plan: %v", err)
	}
	enableIndex, bootstrapIndex := -1, -1
	for index, command := range plan {
		switch command.Args[0] {
		case "enable":
			enableIndex = index
		case "bootstrap":
			bootstrapIndex = index
		}
	}
	if enableIndex < 0 || bootstrapIndex < 0 || enableIndex > bootstrapIndex {
		t.Fatalf("enable must run before bootstrap, got %v", planStrings(plan))
	}
}

func TestLaunchdInstallWithoutStartOnlyPreparesTheOverride(t *testing.T) {
	plistPath := filepath.Join(t.TempDir(), "x.plist")
	spec := serviceSpec{Name: controllerServiceName, Role: RoleController, TargetOS: "darwin"}
	plan, err := serviceEnablePlan(spec, plistPath, false)
	if err != nil {
		t.Fatalf("enable plan: %v", err)
	}
	for _, line := range planStrings(plan) {
		if strings.Contains(line, "bootstrap") || strings.Contains(line, "kickstart") {
			t.Fatalf("--start=false must not load or start the job, got %v", planStrings(plan))
		}
	}
	if planStrings(plan)[len(plan)-1] != "launchctl enable "+darwinTarget() {
		t.Fatalf("expected the override to be cleared, got %v", planStrings(plan))
	}
}

func TestLaunchdControlPlans(t *testing.T) {
	plistPath := filepath.Join(t.TempDir(), "com.bizdooroo.builda.controller.plist")
	teardown := []string{
		"launchctl bootout " + darwinTarget(),
		"launchctl print " + darwinTarget(),
	}
	load := []string{
		"launchctl enable " + darwinTarget(),
		"launchctl bootstrap " + darwinDomain() + " " + plistPath,
		"launchctl print " + darwinTarget(),
	}
	cases := map[string][]string{
		// start tolerates an already loaded job and uses kickstart without -k,
		// which starts a loaded job and is a no-op when it already runs.
		"start": {load[0], load[1], "launchctl kickstart " + darwinTarget(), load[2]},
		// restart is a verified unload followed by a verified load, never a
		// bootstrap followed by a kill.
		"restart": append(append([]string{}, teardown...), load...),
		"stop":    teardown,
		"status":  {"launchctl print " + darwinTarget()},
	}
	for action, want := range cases {
		plan, err := serviceControlCommands("darwin", controllerServiceName, plistPath, action)
		if err != nil {
			t.Fatalf("%s plan: %v", action, err)
		}
		requirePlan(t, planStrings(plan), want, action)
		requireNoKickstartKill(t, plan, action)
	}

	start, _ := serviceControlCommands("darwin", controllerServiceName, plistPath, "start")
	if !start[1].IgnoreLoaded {
		t.Fatal("start must tolerate an already loaded job")
	}
	if last := start[len(start)-1]; !last.Probe || !last.ExpectSuccess || last.ExpectOutput != plistPath {
		t.Fatalf("start must prove the end state: %+v", last)
	}
	stop, _ := serviceControlCommands("darwin", controllerServiceName, plistPath, "stop")
	if !stop[0].IgnoreMissing || !stop[1].Probe || stop[1].ExpectSuccess {
		t.Fatalf("stop must tolerate an unloaded job and wait for the unload: %+v", stop)
	}
	status, _ := serviceControlCommands("darwin", controllerServiceName, plistPath, "status")
	if !status[0].IgnoreError || !status[0].StreamOutput {
		t.Fatal("status must stream output and must not fail just because the job is stopped")
	}
	if _, err := serviceControlCommands("darwin", controllerServiceName, plistPath, "bogus"); err == nil {
		t.Fatal("expected an unknown action to be rejected")
	}
}

func TestLinuxControlAndEnablePlans(t *testing.T) {
	spec := serviceSpec{Name: controllerServiceName, Role: RoleController, TargetOS: "linux"}
	plan, err := serviceEnablePlan(spec, "/unit/path", true)
	if err != nil {
		t.Fatalf("enable plan: %v", err)
	}
	requirePlan(t, planStrings(plan), []string{
		"systemctl --user daemon-reload",
		"systemctl --user reset-failed builda-controller.service",
		"systemctl --user enable --now builda-controller.service",
		"systemctl --user is-active builda-controller.service",
	}, "linux install")
	if !plan[1].IgnoreError {
		t.Fatal("reset-failed must be advisory so install stays idempotent")
	}
	if last := plan[3]; !last.Probe || !last.ExpectSuccess {
		t.Fatalf("linux install must prove the unit became active: %+v", last)
	}

	withoutStart, err := serviceEnablePlan(spec, "/unit/path", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range withoutStart {
		if command.Probe {
			t.Fatal("--start=false must not wait for the unit to become active")
		}
	}

	for _, action := range []string{"start", "restart"} {
		control, err := serviceControlCommands("linux", controllerServiceName, "/unit/path", action)
		if err != nil {
			t.Fatalf("%s plan: %v", action, err)
		}
		requirePlan(t, planStrings(control), []string{
			"systemctl --user " + action + " builda-controller.service",
			"systemctl --user is-active builda-controller.service",
		}, "linux "+action)
	}
	status, err := serviceControlCommands("linux", controllerServiceName, "/unit/path", "status")
	if err != nil {
		t.Fatal(err)
	}
	if !status[0].IgnoreError {
		t.Fatal("a stopped unit exits non-zero; status must report it rather than fail")
	}

	disable, err := serviceDisablePlan("linux", controllerServiceName, "/unit/path")
	if err != nil {
		t.Fatalf("disable plan: %v", err)
	}
	if !disable[0].IgnoreError {
		t.Fatal("disable must tolerate an already disabled unit")
	}
}

// TestLaunchctlIdempotenceClassification is the regression guard for the
// highest-severity defect found in review: a permission or System Integrity
// refusal must never be read as "the job is already gone", because the plan
// would then report a successful install while launchd keeps running the
// previously loaded job.
func TestLaunchctlIdempotenceClassification(t *testing.T) {
	missing := []string{
		"Boot-out failed: 36: Operation now in progress\nCould not find service \"com.bizdooroo.builda\" in domain for login",
		"launchctl bootout: No such process",
		"Boot-out failed: 3: No such process",
	}
	for _, message := range missing {
		if !launchctlReportsMissing(message) {
			t.Fatalf("expected %q to be classified as already removed", message)
		}
	}
	loaded := []string{
		"Bootstrap failed: 37: Operation already in progress",
		"Load failed: 37: service already loaded",
		"Bootstrap failed: 17: File exists",
	}
	for _, message := range loaded {
		if !launchctlReportsAlreadyLoaded(message) {
			t.Fatalf("expected %q to be classified as already loaded", message)
		}
	}
	inProgress := []string{
		"Boot-out failed: 36: Operation now in progress",
		"Operation now in progress",
	}
	for _, message := range inProgress {
		if !launchctlReportsInProgress(message) {
			t.Fatalf("expected %q to be classified as an unfinished teardown", message)
		}
	}
	for _, refusal := range []string{
		"Boot-out failed: 1: Operation not permitted",
		"Operation not permitted while System Integrity Protection is engaged",
		"Bootstrap failed: 5: Input/output error",
	} {
		if launchctlReportsMissing(refusal) {
			t.Fatalf("%q must not be swallowed as a missing service", refusal)
		}
		if launchctlReportsAlreadyLoaded(refusal) {
			t.Fatalf("%q must not be swallowed as an already loaded service", refusal)
		}
		if launchctlReportsInProgress(refusal) {
			t.Fatalf("%q must not be swallowed as an unfinished teardown", refusal)
		}
	}
}

func TestRunServiceCommandsAppliesIdempotenceRules(t *testing.T) {
	// A missing-tolerant step whose command does not exist still fails,
	// because the output does not match a launchd "already gone" response.
	err := runServiceCommands(os.Stderr, []serviceCommand{
		{Name: "/nonexistent/launchctl", Args: []string{"bootout"}, IgnoreMissing: true},
	})
	if err == nil {
		t.Fatal("expected a missing tool to surface as an error")
	}
	if err := runServiceCommands(os.Stderr, []serviceCommand{
		{Name: "/nonexistent/launchctl", Args: []string{"bootout"}, IgnoreError: true},
	}); err != nil {
		t.Fatalf("advisory step must not fail the plan: %v", err)
	}
	err = runServiceCommands(os.Stderr, []serviceCommand{
		{Name: "/nonexistent/launchctl", Args: []string{"bootstrap"}, Hint: "run this from a desktop session"},
	})
	if err == nil || !strings.Contains(err.Error(), "run this from a desktop session") {
		t.Fatalf("expected the hint in the error, got %v", err)
	}
}

// TestRunServiceProbeWaitsForTheExpectedState exercises the probe mechanism
// with real commands instead of launchctl.
func TestRunServiceProbeWaitsForTheExpectedState(t *testing.T) {
	// A probe that expects failure succeeds immediately against a command
	// that fails, which is how a teardown is proven.
	if err := runServiceCommands(os.Stderr, []serviceCommand{{
		Name: "/nonexistent/launchctl", Args: []string{"print"},
		Probe: true, ExpectSuccess: false, Deadline: time.Second,
	}}); err != nil {
		t.Fatalf("a failing command must satisfy a probe expecting failure: %v", err)
	}

	// A probe that expects success times out against a failing command, and
	// says what it was waiting for.
	err := runServiceCommands(os.Stderr, []serviceCommand{{
		Name: "/nonexistent/launchctl", Args: []string{"print"},
		Probe: true, ExpectSuccess: true, Deadline: 300 * time.Millisecond,
		Describe: "waiting for launchd to load the plist",
	}})
	if err == nil || !strings.Contains(err.Error(), "waiting for launchd to load the plist") {
		t.Fatalf("expected a descriptive timeout, got %v", err)
	}

	// A probe that requires specific output fails when the output does not
	// mention it, which is how a load is bound to the plist just written.
	err = runServiceCommands(os.Stderr, []serviceCommand{{
		Name: "/bin/echo", Args: []string{"path = /some/other.plist"},
		Probe: true, ExpectSuccess: true, ExpectOutput: "/expected.plist",
		Deadline: 300 * time.Millisecond, Describe: "verifying the loaded job",
	}})
	if err == nil || !strings.Contains(err.Error(), "/expected.plist") {
		t.Fatalf("expected the output requirement in the error, got %v", err)
	}
	if err := runServiceCommands(os.Stderr, []serviceCommand{{
		Name: "/bin/echo", Args: []string{"path = /expected.plist"},
		Probe: true, ExpectSuccess: true, ExpectOutput: "/expected.plist",
		Deadline: time.Second,
	}}); err != nil {
		t.Fatalf("a matching output must satisfy the probe: %v", err)
	}
}

func TestLaunchdDomainHintIsActionable(t *testing.T) {
	hint := launchdDomainHint()
	for _, want := range []string{darwinDomain(), "desktop login session", "launchctl asuser"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("expected hint to mention %q, got %q", want, hint)
		}
	}
}
