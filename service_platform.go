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
)

// Service control plans. Each platform action is expressed as an ordered list
// of commands so the order, the flags, and the error handling are testable
// without a live init system.
//
// launchd notes, verified against a real LaunchAgent:
//   - A plist with RunAtLoad=true is already started by `bootstrap`. Following
//     bootstrap with `kickstart -k` kills that instance and starts a second
//     one while the first still holds its listening socket, which produces a
//     "bind: address already in use" crash loop paced by the KeepAlive
//     throttle. Install and start therefore never use `-k`.
//   - `enable` writes a persistent per-user override and must run before
//     `bootstrap`, otherwise a previously disabled label loads but is never
//     allowed to run.
//   - `bootout` on a label that is not loaded is a successful no-op for our
//     purposes, and `bootstrap` on a label that is already loaded is too.
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
		return append(commands, serviceCommand{Name: "systemctl", Args: args, Hint: linuxUserServiceHint}), nil
	case "darwin":
		target := launchdServiceTarget(spec.Name)
		commands := []serviceCommand{
			{Name: "launchctl", Args: []string{"bootout", target}, IgnoreMissing: true},
			{Name: "launchctl", Args: []string{"enable", target}, IgnoreMissing: true},
		}
		if !start {
			return commands, nil
		}
		// RunAtLoad starts the job exactly once here. No kickstart follows.
		return append(commands, serviceCommand{
			Name:         "launchctl",
			Args:         []string{"bootstrap", launchdDomain(), path},
			IgnoreLoaded: true,
			Hint:         launchdDomainHint(),
		}), nil
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
		return []serviceCommand{
			{Name: "launchctl", Args: []string{"bootout", launchdServiceTarget(name)}, IgnoreMissing: true},
		}, nil
	default:
		return nil, fmt.Errorf("service uninstall is not supported on %s", targetOS)
	}
}

func serviceControlCommands(targetOS, name, path, action string) ([]serviceCommand, error) {
	switch targetOS {
	case "linux":
		switch action {
		case "start", "stop", "restart", "status":
			return []serviceCommand{{
				Name:         "systemctl",
				Args:         []string{"--user", action, name + ".service"},
				StreamOutput: action == "status",
				Hint:         linuxUserServiceHint,
			}}, nil
		default:
			return nil, fmt.Errorf("unknown service action %q", action)
		}
	case "darwin":
		domain := launchdDomain()
		target := launchdServiceTarget(name)
		// Clearing a persistent override and loading the job are idempotent
		// preconditions shared by start and restart.
		load := []serviceCommand{
			{Name: "launchctl", Args: []string{"enable", target}, IgnoreMissing: true},
			{Name: "launchctl", Args: []string{"bootstrap", domain, path}, IgnoreLoaded: true, Hint: launchdDomainHint()},
		}
		switch action {
		case "start":
			// kickstart without -k starts a loaded job and is a no-op when it
			// is already running, so repeated starts never restart the job.
			return append(load, serviceCommand{Name: "launchctl", Args: []string{"kickstart", target}, Hint: launchdDomainHint()}), nil
		case "restart":
			return append(load, serviceCommand{Name: "launchctl", Args: []string{"kickstart", "-k", target}, Hint: launchdDomainHint()}), nil
		case "stop":
			return []serviceCommand{{Name: "launchctl", Args: []string{"bootout", target}, IgnoreMissing: true}}, nil
		case "status":
			return []serviceCommand{{Name: "launchctl", Args: []string{"print", target}, StreamOutput: true, Hint: launchdDomainHint()}}, nil
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
		if command.IgnoreLoaded && launchctlReportsAlreadyLoaded(output) {
			continue
		}
		return decorateServiceError(command, err)
	}
	return nil
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
// you asked me to remove is already gone".
func launchctlReportsMissing(message string) bool {
	lowered := strings.ToLower(message)
	for _, needle := range []string{
		"no such process",
		"could not find service",
		"not find specified service",
		"no such file or directory",
		"operation not permitted while System Integrity", // never retried
	} {
		if strings.Contains(lowered, needle) {
			return true
		}
	}
	return strings.Contains(lowered, "boot-out failed: 3:")
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
