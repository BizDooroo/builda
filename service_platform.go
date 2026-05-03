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

func enableService(spec serviceSpec, path string, start bool) error {
	switch spec.TargetOS {
	case "linux":
		if err := runCommand("systemctl", "--user", "daemon-reload"); err != nil {
			return err
		}
		args := []string{"--user", "enable"}
		if start {
			args = append(args, "--now")
		}
		args = append(args, spec.Name+".service")
		return runCommand("systemctl", args...)
	case "darwin":
		if !start {
			return nil
		}
		domain := launchdDomain()
		_ = runCommand("launchctl", "bootout", domain, path)
		if err := runCommand("launchctl", "bootstrap", domain, path); err != nil {
			return err
		}
		label := launchdLabel(spec.Name)
		if err := runCommand("launchctl", "enable", domain+"/"+label); err != nil {
			return err
		}
		return runCommand("launchctl", "kickstart", "-k", domain+"/"+label)
	default:
		return fmt.Errorf("service install is not supported on %s", spec.TargetOS)
	}
}

func disableService(targetOS, name, path string) error {
	switch targetOS {
	case "linux":
		_ = runCommand("systemctl", "--user", "disable", "--now", name+".service")
		return runCommand("systemctl", "--user", "daemon-reload")
	case "darwin":
		_ = runCommand("launchctl", "bootout", launchdDomain(), path)
		return nil
	default:
		return fmt.Errorf("service uninstall is not supported on %s", targetOS)
	}
}

func serviceControlCommands(targetOS, name, path, action string) ([]serviceCommand, error) {
	switch targetOS {
	case "linux":
		if action != "start" && action != "stop" && action != "restart" && action != "status" {
			return nil, fmt.Errorf("unknown service action %q", action)
		}
		return []serviceCommand{{
			Name:         "systemctl",
			Args:         []string{"--user", action, name + ".service"},
			StreamOutput: action == "status",
		}}, nil
	case "darwin":
		domain := launchdDomain()
		label := launchdLabel(name)
		target := domain + "/" + label
		switch action {
		case "start":
			return []serviceCommand{
				{Name: "launchctl", Args: []string{"bootstrap", domain, path}},
				{Name: "launchctl", Args: []string{"enable", target}},
				{Name: "launchctl", Args: []string{"kickstart", "-k", target}},
			}, nil
		case "stop":
			return []serviceCommand{{Name: "launchctl", Args: []string{"bootout", domain, path}}}, nil
		case "restart":
			return []serviceCommand{
				{Name: "launchctl", Args: []string{"bootout", domain, path}, IgnoreError: true},
				{Name: "launchctl", Args: []string{"bootstrap", domain, path}},
				{Name: "launchctl", Args: []string{"enable", target}},
				{Name: "launchctl", Args: []string{"kickstart", "-k", target}},
			}, nil
		case "status":
			return []serviceCommand{{Name: "launchctl", Args: []string{"print", target}, StreamOutput: true}}, nil
		default:
			return nil, fmt.Errorf("unknown service action %q", action)
		}
	default:
		return nil, fmt.Errorf("service control is not supported on %s", targetOS)
	}
}

func runCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
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

func launchdLabel(name string) string {
	if name == defaultServiceName {
		return launchdLabelPrefix + ".builda"
	}
	return launchdLabelPrefix + ".builda." + name
}

func launchdDomain() string {
	return "gui/" + strconv.Itoa(os.Getuid())
}
