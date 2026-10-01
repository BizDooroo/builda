package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// planStrings renders a plan as "tool arg arg" lines for comparison.
func planStrings(commands []serviceCommand) []string {
	lines := make([]string, 0, len(commands))
	for _, command := range commands {
		lines = append(lines, strings.TrimSpace(command.Name+" "+strings.Join(command.Args, " ")))
	}
	return lines
}

func darwinTarget() string {
	return "gui/" + strconv.Itoa(os.Getuid()) + "/com.bizdooroo.builda.controller"
}

func darwinDomain() string {
	return "gui/" + strconv.Itoa(os.Getuid())
}

// TestLaunchdEnablePlanAvoidsDoubleStart is the regression test for the
// install failure: bootstrap already starts a RunAtLoad job, so following it
// with "kickstart -k" killed the fresh instance and restarted it while the
// old listener was still bound, producing a bind-address-in-use crash loop.
func TestLaunchdEnablePlanAvoidsDoubleStart(t *testing.T) {
	plistPath := filepath.Join(t.TempDir(), "com.bizdooroo.builda.controller.plist")
	spec := serviceSpec{Name: controllerServiceName, Role: RoleController, TargetOS: "darwin"}

	plan, err := serviceEnablePlan(spec, plistPath, true)
	if err != nil {
		t.Fatalf("enable plan: %v", err)
	}
	want := []string{
		"launchctl bootout " + darwinTarget(),
		"launchctl enable " + darwinTarget(),
		"launchctl bootstrap " + darwinDomain() + " " + plistPath,
	}
	got := planStrings(plan)
	if len(got) != len(want) {
		t.Fatalf("enable plan = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("enable plan step %d = %q, want %q", index, got[index], want[index])
		}
	}
	for _, line := range got {
		if strings.Contains(line, "kickstart") {
			t.Fatalf("install must not kickstart after bootstrap, got %q", line)
		}
	}
}

// TestLaunchdEnablePlanOrdersEnableBeforeBootstrap pins the fix for a
// persistent "disabled" override: enable writes the override and must run
// before the job is loaded.
func TestLaunchdEnablePlanOrdersEnableBeforeBootstrap(t *testing.T) {
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
	if enableIndex < 0 || bootstrapIndex < 0 {
		t.Fatalf("expected enable and bootstrap steps, got %v", planStrings(plan))
	}
	if enableIndex > bootstrapIndex {
		t.Fatalf("enable must run before bootstrap, got %v", planStrings(plan))
	}
	if !plan[bootstrapIndex].IgnoreLoaded {
		t.Fatal("bootstrap must tolerate an already loaded job so install is idempotent")
	}
	if plan[enableIndex].Hint != "" && !strings.Contains(plan[bootstrapIndex].Hint, "GUI domain") {
		t.Fatal("bootstrap needs an actionable GUI domain hint")
	}
}

func TestLaunchdEnablePlanWithoutStartOnlyPreparesOverride(t *testing.T) {
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
}

// TestLaunchdControlPlans pins the start/restart/stop command order, including
// that only an explicit restart uses the destructive -k form.
func TestLaunchdControlPlans(t *testing.T) {
	plistPath := filepath.Join(t.TempDir(), "com.bizdooroo.builda.controller.plist")
	cases := map[string][]string{
		"start": {
			"launchctl enable " + darwinTarget(),
			"launchctl bootstrap " + darwinDomain() + " " + plistPath,
			"launchctl kickstart " + darwinTarget(),
		},
		"restart": {
			"launchctl enable " + darwinTarget(),
			"launchctl bootstrap " + darwinDomain() + " " + plistPath,
			"launchctl kickstart -k " + darwinTarget(),
		},
		"stop": {
			"launchctl bootout " + darwinTarget(),
		},
		"status": {
			"launchctl print " + darwinTarget(),
		},
	}
	for action, want := range cases {
		plan, err := serviceControlCommands("darwin", controllerServiceName, plistPath, action)
		if err != nil {
			t.Fatalf("%s plan: %v", action, err)
		}
		got := planStrings(plan)
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("%s plan = %v, want %v", action, got, want)
		}
	}

	start, _ := serviceControlCommands("darwin", controllerServiceName, plistPath, "start")
	for _, command := range start {
		if command.Args[0] == "kickstart" {
			for _, arg := range command.Args {
				if arg == "-k" {
					t.Fatal("start must not kill a running job")
				}
			}
		}
	}
	stop, _ := serviceControlCommands("darwin", controllerServiceName, plistPath, "stop")
	if !stop[0].IgnoreMissing {
		t.Fatal("stop must treat an unloaded job as success")
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
	got := planStrings(plan)
	want := []string{
		"systemctl --user daemon-reload",
		"systemctl --user reset-failed builda-controller.service",
		"systemctl --user enable --now builda-controller.service",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("linux enable plan = %v, want %v", got, want)
	}
	if !plan[1].IgnoreError {
		t.Fatal("reset-failed must be advisory so install stays idempotent")
	}

	control, err := serviceControlCommands("linux", controllerServiceName, "/unit/path", "restart")
	if err != nil {
		t.Fatalf("control plan: %v", err)
	}
	if planStrings(control)[0] != "systemctl --user restart builda-controller.service" {
		t.Fatalf("linux restart plan = %v", planStrings(control))
	}

	disable, err := serviceDisablePlan("linux", controllerServiceName, "/unit/path")
	if err != nil {
		t.Fatalf("disable plan: %v", err)
	}
	if !disable[0].IgnoreError {
		t.Fatal("disable must tolerate an already disabled unit")
	}
}

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
	if launchctlReportsMissing("Bootstrap failed: 5: Input/output error") {
		t.Fatal("a domain error must not be swallowed as a missing service")
	}
	if launchctlReportsAlreadyLoaded("Bootstrap failed: 5: Input/output error") {
		t.Fatal("a domain error must not be swallowed as an already loaded service")
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
	// An explicitly advisory step never fails the plan.
	if err := runServiceCommands(os.Stderr, []serviceCommand{
		{Name: "/nonexistent/launchctl", Args: []string{"bootout"}, IgnoreError: true},
	}); err != nil {
		t.Fatalf("advisory step must not fail the plan: %v", err)
	}
	// Hints are attached to real failures so the operator knows what to do.
	err = runServiceCommands(os.Stderr, []serviceCommand{
		{Name: "/nonexistent/launchctl", Args: []string{"bootstrap"}, Hint: "run this from a desktop session"},
	})
	if err == nil || !strings.Contains(err.Error(), "run this from a desktop session") {
		t.Fatalf("expected the hint in the error, got %v", err)
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
