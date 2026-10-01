package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Service control plans. Each platform action is expressed as an ordered list
// of steps so the order, the flags, and the error handling are testable
// without a live init system.
//
// launchd notes, verified against a real LaunchAgent:
//   - A plist with RunAtLoad=true is already started by `bootstrap`. Following
//     bootstrap with `kickstart -k` kills that instance and starts a second
//     one while the first still holds its listening socket, which produces a
//     "bind: address already in use" crash loop paced by the KeepAlive
//     throttle. No plan here ever uses `-k`.
//   - `bootout` only signals the job; it returns before the job is gone, and
//     the plist's ExitTimeOut bounds how long that takes. A plan that loads
//     immediately afterwards can hit "Operation already in progress", so every
//     teardown is followed by a probe that waits for the job to disappear.
//   - `enable` writes a persistent per-user override and must run before
//     `bootstrap`, otherwise a previously disabled label loads but is never
//     allowed to run.
//   - Loading is verified: the probe after `bootstrap` requires the loaded job
//     to reference the plist this install just wrote, so a swallowed error can
//     never be reported as a successful install.
const (
	launchdUnloadDeadline = 35 * time.Second
	launchdLoadDeadline   = 10 * time.Second
	systemdStartDeadline  = 10 * time.Second
)

// launchdTeardown stops a job and waits for launchd to finish unloading it.
func launchdTeardown(name string) []serviceCommand {
	target := launchdServiceTarget(name)
	return []serviceCommand{
		{Name: "launchctl", Args: []string{"bootout", target}, IgnoreMissing: true, IgnoreBusy: true},
		{
			Name:          "launchctl",
			Args:          []string{"print", target},
			Probe:         true,
			ExpectSuccess: false,
			Deadline:      launchdUnloadDeadline,
			Describe:      "waiting for launchd to unload " + target,
			Hint:          "the previous job is still shutting down; stop it with \"launchctl bootout " + target + "\" and retry",
		},
	}
}

// launchdLoad enables, loads, and then proves the loaded job is the one at
// plistPath.
func launchdLoad(name, plistPath string, tolerateLoaded bool) []serviceCommand {
	target := launchdServiceTarget(name)
	return []serviceCommand{
		{Name: "launchctl", Args: []string{"enable", target}, IgnoreMissing: true},
		{
			Name:         "launchctl",
			Args:         []string{"bootstrap", launchdDomain(), plistPath},
			IgnoreLoaded: tolerateLoaded,
			Hint:         launchdDomainHint(),
		},
		{
			Name:          "launchctl",
			Args:          []string{"print", target},
			Probe:         true,
			ExpectSuccess: true,
			ExpectOutput:  plistPath,
			Deadline:      launchdLoadDeadline,
			Describe:      "waiting for launchd to load " + plistPath,
			Hint:          launchdDomainHint(),
		},
	}
}

func serviceEnablePlan(spec serviceSpec, path string, start bool) ([]serviceCommand, error) {
	switch spec.TargetOS {
	case "linux":
		commands := []serviceCommand{
			{Name: "systemctl", Args: []string{"--user", "daemon-reload"}},
			{Name: "systemctl", Args: []string{"--user", "reset-failed", spec.Name + ".service"}, IgnoreError: true},
		}
		args := []string{"--user", "enable"}
		if start {
			args = append(args, "--now")
		}
		args = append(args, spec.Name+".service")
		commands = append(commands, serviceCommand{Name: "systemctl", Args: args, Hint: linuxUserServiceHint})
		if start {
			commands = append(commands, systemdActiveProbe(spec.Name))
		}
		return commands, nil
	case "darwin":
		commands := launchdTeardown(spec.Name)
		if !start {
			// The plist is written and the override cleared, but nothing is
			// loaded, so there is nothing to verify.
			return append(commands, serviceCommand{
				Name: "launchctl", Args: []string{"enable", launchdServiceTarget(spec.Name)}, IgnoreMissing: true,
			}), nil
		}
		// The teardown probe proved the label is unloaded, so a bootstrap that
		// reports "already loaded" here is a genuine surprise and must fail.
		return append(commands, launchdLoad(spec.Name, path, false)...), nil
	default:
		return nil, fmt.Errorf("service install is not supported on %s", spec.TargetOS)
	}
}

func serviceDisablePlan(targetOS, name, path string) ([]serviceCommand, error) {
	switch targetOS {
	case "linux":
		return []serviceCommand{
			{Name: "systemctl", Args: []string{"--user", "disable", "--now", name + ".service"}, IgnoreError: true},
			{Name: "systemctl", Args: []string{"--user", "daemon-reload"}},
		}, nil
	case "darwin":
		return launchdTeardown(name), nil
	default:
		return nil, fmt.Errorf("service uninstall is not supported on %s", targetOS)
	}
}

func systemdActiveProbe(name string) serviceCommand {
	return serviceCommand{
		Name:          "systemctl",
		Args:          []string{"--user", "is-active", name + ".service"},
		Probe:         true,
		ExpectSuccess: true,
		Deadline:      systemdStartDeadline,
		Describe:      "waiting for " + name + ".service to become active",
		Hint:          linuxUserServiceHint,
	}
}

func serviceControlCommands(targetOS, name, path, action string) ([]serviceCommand, error) {
	switch targetOS {
	case "linux":
		switch action {
		case "start", "restart":
			return []serviceCommand{
				{Name: "systemctl", Args: []string{"--user", action, name + ".service"}, Hint: linuxUserServiceHint},
				systemdActiveProbe(name),
			}, nil
		case "stop":
			return []serviceCommand{{Name: "systemctl", Args: []string{"--user", "stop", name + ".service"}, Hint: linuxUserServiceHint}}, nil
		case "status":
			// A stopped unit exits non-zero; that is the answer, not a failure.
			return []serviceCommand{{
				Name:         "systemctl",
				Args:         []string{"--user", "status", name + ".service"},
				StreamOutput: true,
				IgnoreError:  true,
			}}, nil
		default:
			return nil, fmt.Errorf("unknown service action %q", action)
		}
	case "darwin":
		target := launchdServiceTarget(name)
		switch action {
		case "start":
			// The job is usually already loaded, so tolerate that and use
			// kickstart without -k, which starts a loaded job and is a no-op
			// when it is already running. The probe proves the end state.
			plan := launchdLoad(name, path, true)
			return append(plan[:len(plan)-1],
				serviceCommand{Name: "launchctl", Args: []string{"kickstart", target}, Hint: launchdDomainHint()},
				plan[len(plan)-1],
			), nil
		case "restart":
			// A verified unload followed by a verified load. Never a bootstrap
			// followed by a kill, which would start the job twice.
			return append(launchdTeardown(name), launchdLoad(name, path, false)...), nil
		case "stop":
			return launchdTeardown(name), nil
		case "status":
			return []serviceCommand{{
				Name:         "launchctl",
				Args:         []string{"print", target},
				StreamOutput: true,
				IgnoreError:  true,
			}}, nil
		default:
			return nil, fmt.Errorf("unknown service action %q", action)
		}
	default:
		return nil, fmt.Errorf("service control is not supported on %s", targetOS)
	}
}

const linuxUserServiceHint = "systemd user units need a live user manager; on a headless host run \"loginctl enable-linger $USER\""

// launchdDomainHint explains the one launchd failure an operator cannot fix
// from the command output alone.
func launchdDomainHint() string {
	return fmt.Sprintf("launchd user agents live in the %s GUI domain and need an active desktop login session; run this from a terminal on the Mac itself rather than over SSH, or wrap it with \"launchctl asuser %d\"", launchdDomain(), os.Getuid())
}

// runServiceCommands executes a plan, applying the idempotence rules of each
// step and attaching actionable hints to real failures.
func runServiceCommands(out io.Writer, commands []serviceCommand) error {
	for _, command := range commands {
		if command.Probe {
			if err := runServiceProbe(command); err != nil {
				return decorateServiceError(command, err)
			}
			continue
		}
		output, err := executeServiceCommand(out, command)
		if err == nil {
			continue
		}
		if command.IgnoreError {
			continue
		}
		if command.IgnoreMissing && launchctlReportsMissing(output) {
			continue
		}
		if command.IgnoreBusy && launchctlReportsInProgress(output) {
			continue
		}
		if command.IgnoreLoaded && launchctlReportsAlreadyLoaded(output) {
			continue
		}
		return decorateServiceError(command, err)
	}
	return nil
}

// runServiceProbe polls one command until the state it asserts holds.
func runServiceProbe(command serviceCommand) error {
	deadline := command.Deadline
	if deadline <= 0 {
		deadline = launchdLoadDeadline
	}
	stop := time.Now().Add(deadline)
	var lastOutput string
	for {
		output, err := runCommandCapture(command.Name, command.Args...)
		lastOutput = output
		succeeded := err == nil
		if succeeded == command.ExpectSuccess {
			if !command.ExpectSuccess || command.ExpectOutput == "" || strings.Contains(output, command.ExpectOutput) {
				return nil
			}
		}
		if !time.Now().Before(stop) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	describe := command.Describe
	if describe == "" {
		describe = command.Name + " " + strings.Join(command.Args, " ")
	}
	if command.ExpectSuccess && command.ExpectOutput != "" {
		return fmt.Errorf("%s timed out after %s; the loaded job does not reference %q: %s",
			describe, deadline, command.ExpectOutput, strings.TrimSpace(lastOutput))
	}
	return fmt.Errorf("%s timed out after %s: %s", describe, deadline, strings.TrimSpace(lastOutput))
}

func executeServiceCommand(out io.Writer, command serviceCommand) (string, error) {
	if command.StreamOutput {
		return "", runCommandOutput(out, command.Name, command.Args...)
	}
	return runCommandCapture(command.Name, command.Args...)
}

func decorateServiceError(command serviceCommand, err error) error {
	if command.Hint == "" {
		return err
	}
	return fmt.Errorf("%w\nhint: %s", err, command.Hint)
}

// launchctlReportsMissing matches the launchd responses that mean "the thing
// you asked me to remove is already gone". A permission or System Integrity
// refusal is deliberately absent: swallowing it would report a successful
// install while launchd keeps running the previously loaded job.
func launchctlReportsMissing(message string) bool {
	lowered := strings.ToLower(message)
	for _, needle := range []string{
		"no such process",
		"could not find service",
		"not find specified service",
		"no such file or directory",
	} {
		if strings.Contains(lowered, needle) {
			return true
		}
	}
	return strings.Contains(lowered, "boot-out failed: 3:")
}

// launchctlReportsInProgress matches a teardown that launchd has accepted but
// not finished. The probe that follows such a step proves the outcome.
func launchctlReportsInProgress(message string) bool {
	lowered := strings.ToLower(message)
	return strings.Contains(lowered, "operation now in progress") ||
		strings.Contains(lowered, "boot-out failed: 36")
}

// launchctlReportsAlreadyLoaded matches the launchd responses that mean "this
// job is already bootstrapped", which makes a repeated start a no-op.
func launchctlReportsAlreadyLoaded(message string) bool {
	lowered := strings.ToLower(message)
	for _, needle := range []string{
		"service already loaded",
		"already bootstrapped",
		"bootstrap failed: 37",
		"operation already in progress",
		"file exists",
	} {
		if strings.Contains(lowered, needle) {
			return true
		}
	}
	return false
}

func runCommand(name string, args ...string) error {
	_, err := runCommandCapture(name, args...)
	return err
}

// runCommandCapture returns the combined diagnostic output alongside the error
// so idempotence rules can inspect what the tool actually said.
func runCommandCapture(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var stderr, stdout bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stdout
	err := cmd.Run()
	combined := strings.TrimSpace(stderr.String() + "\n" + stdout.String())
	if err == nil {
		return combined, nil
	}
	if combined != "" {
		return combined, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, combined)
	}
	return combined, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
}

func runCommandOutput(out io.Writer, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = out
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, message)
		}
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func normalizeServiceName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("service name must not be empty")
	}
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			continue
		}
		return "", fmt.Errorf("service name %q must contain only letters, digits, dots, underscores, and hyphens", name)
	}
	return name, nil
}

// launchdLabel keeps the controller and the agent on distinct reverse-DNS
// labels so both roles can be installed on one Mac.
func launchdLabel(name string) string {
	switch name {
	case defaultServiceName:
		return launchdLabelPrefix + ".builda"
	case controllerServiceName:
		return launchdLabelPrefix + ".builda.controller"
	case agentServiceName:
		return launchdLabelPrefix + ".builda.agent"
	}
	if suffix, ok := strings.CutPrefix(name, defaultServiceName+"-"); ok && suffix != "" {
		return launchdLabelPrefix + ".builda." + suffix
	}
	return launchdLabelPrefix + ".builda." + name
}

func launchdServiceTarget(name string) string {
	return launchdDomain() + "/" + launchdLabel(name)
}

func launchdDomain() string {
	return "gui/" + strconv.Itoa(os.Getuid())
}
