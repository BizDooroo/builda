package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Service file rendering. The daemon is always the Builda executable itself:
// no shell, no wrapper, no login session, so nothing can open a terminal or
// source an interactive profile.
const (
	serviceRestartThrottleSeconds = 10
	serviceExitTimeoutSeconds     = 30
)

// servicePATH is the explicit search path given to the daemon. launchd starts
// user agents with only /usr/bin:/bin:/usr/sbin:/sbin, which leaves Homebrew
// build tools unreachable, so the service file states the path it needs.
func servicePATH(targetOS string) string {
	switch targetOS {
	case "darwin":
		return "/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:/usr/local/sbin:/usr/bin:/bin:/usr/sbin:/sbin"
	default:
		return "/usr/local/bin:/usr/local/sbin:/usr/bin:/bin:/usr/sbin:/sbin"
	}
}

func renderServiceArtifact(spec serviceSpec) (serviceArtifact, error) {
	path, err := servicePath(spec.TargetOS, spec.Name)
	if err != nil {
		return serviceArtifact{}, err
	}
	switch spec.TargetOS {
	case "linux":
		return serviceArtifact{Path: path, Content: renderSystemdUnit(spec)}, nil
	case "darwin":
		content, err := renderLaunchdPlist(spec)
		if err != nil {
			return serviceArtifact{}, err
		}
		return serviceArtifact{Path: path, Content: content}, nil
	default:
		return serviceArtifact{}, fmt.Errorf("service target must be linux or darwin, got %q", spec.TargetOS)
	}
}

func renderSystemdUnit(spec serviceSpec) string {
	args := serviceExecArgs(spec)
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=Builda " + serviceDescriptionRole(spec.Role) + "\n")
	// network-online.target does not exist in the user manager, so ordering
	// against it would be silently ignored.
	b.WriteString("After=default.target\n\n")
	b.WriteString("[Service]\n")
	b.WriteString("Type=simple\n")
	b.WriteString("ExecStart=")
	b.WriteString(strings.Join(systemdQuoteArgs(args), " "))
	b.WriteString("\n")
	b.WriteString("Environment=PATH=" + servicePATH(spec.TargetOS) + "\n")
	b.WriteString("WorkingDirectory=" + filepath.Dir(spec.ConfigPath) + "\n")
	b.WriteString("Restart=always\n")
	b.WriteString(fmt.Sprintf("RestartSec=%ds\n", serviceRestartThrottleSeconds))
	b.WriteString(fmt.Sprintf("TimeoutStopSec=%ds\n\n", serviceExitTimeoutSeconds))
	b.WriteString("[Install]\n")
	b.WriteString("WantedBy=default.target\n")
	return b.String()
}

func renderLaunchdPlist(spec serviceSpec) (string, error) {
	logDir, err := serviceLogDir()
	if err != nil {
		return "", err
	}
	label := launchdLabel(spec.Name)
	var b strings.Builder
	b.WriteString(xml.Header)
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">`)
	b.WriteString("\n<plist version=\"1.0\">\n<dict>\n")
	writePlistString(&b, "Label", label)
	b.WriteString("  <key>ProgramArguments</key>\n")
	b.WriteString("  <array>\n")
	for _, arg := range serviceExecArgs(spec) {
		b.WriteString("    <string>")
		b.WriteString(xmlEscape(arg))
		b.WriteString("</string>\n")
	}
	b.WriteString("  </array>\n")
	b.WriteString("  <key>EnvironmentVariables</key>\n")
	b.WriteString("  <dict>\n")
	b.WriteString("    <key>PATH</key>\n")
	b.WriteString("    <string>" + xmlEscape(servicePATH(spec.TargetOS)) + "</string>\n")
	b.WriteString("  </dict>\n")
	writePlistTrue(&b, "RunAtLoad")
	writePlistTrue(&b, "KeepAlive")
	writePlistString(&b, "ProcessType", servicePlistProcessType(spec.Role))
	// Aqua pins the agent to the desktop login session, which is the only
	// session type that can reach the login keychain used by code signing,
	// and prevents a second copy loading into another session type.
	writePlistString(&b, "LimitLoadToSessionType", "Aqua")
	writePlistInteger(&b, "ThrottleInterval", serviceRestartThrottleSeconds)
	writePlistInteger(&b, "ExitTimeOut", serviceExitTimeoutSeconds)
	writePlistString(&b, "StandardOutPath", filepath.Join(logDir, spec.Name+".out.log"))
	writePlistString(&b, "StandardErrorPath", filepath.Join(logDir, spec.Name+".err.log"))
	writePlistString(&b, "WorkingDirectory", filepath.Dir(spec.ConfigPath))
	b.WriteString("</dict>\n</plist>\n")
	return b.String(), nil
}

func writePlistString(b *strings.Builder, key, value string) {
	b.WriteString("  <key>")
	b.WriteString(xmlEscape(key))
	b.WriteString("</key>\n")
	b.WriteString("  <string>")
	b.WriteString(xmlEscape(value))
	b.WriteString("</string>\n")
}

func writePlistInteger(b *strings.Builder, key string, value int) {
	b.WriteString("  <key>")
	b.WriteString(xmlEscape(key))
	b.WriteString("</key>\n")
	b.WriteString(fmt.Sprintf("  <integer>%d</integer>\n", value))
}

func writePlistTrue(b *strings.Builder, key string) {
	b.WriteString("  <key>")
	b.WriteString(xmlEscape(key))
	b.WriteString("</key>\n")
	b.WriteString("  <true/>\n")
}

func xmlEscape(value string) string {
	var b bytes.Buffer
	if err := xml.EscapeText(&b, []byte(value)); err != nil {
		return value
	}
	return b.String()
}

// serviceExecArgs renders the role-specific command line of the daemon.
func serviceExecArgs(spec serviceSpec) []string {
	args := []string{spec.BinaryPath}
	switch spec.Role {
	case RoleController:
		args = append(args, "controller", "serve")
	case RoleAgent:
		args = append(args, "agent", "run")
	default:
		args = append(args, "serve")
	}
	args = append(args, "--config", spec.ConfigPath)
	if spec.Role == RoleAgent {
		// The agent has no listener, so listen overrides do not apply.
		return args
	}
	for _, addr := range spec.Addrs {
		args = append(args, "--addr", addr)
	}
	return args
}

func systemdQuoteArgs(args []string) []string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		arg = strings.ReplaceAll(arg, `\`, `\\`)
		arg = strings.ReplaceAll(arg, `"`, `\"`)
		arg = strings.ReplaceAll(arg, `%`, `%%`)
		quoted = append(quoted, `"`+arg+`"`)
	}
	return quoted
}

func servicePath(targetOS, name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch targetOS {
	case "linux":
		configHome := os.Getenv("XDG_CONFIG_HOME")
		if strings.TrimSpace(configHome) == "" {
			configHome = filepath.Join(home, ".config")
		}
		return filepath.Join(configHome, "systemd", "user", name+".service"), nil
	case "darwin":
		return filepath.Join(home, "Library", "LaunchAgents", launchdLabel(name)+".plist"), nil
	default:
		return "", fmt.Errorf("service target must be linux or darwin, got %q", targetOS)
	}
}

// serviceLogDir is the directory holding the daemon's redirected stdout and
// stderr. Install creates it so launchd never fails to spawn on a missing path.
func serviceLogDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Logs", "builda"), nil
}

// servicePlistProcessType picks the launchd scheduling tier. Background puts a
// job on a throttled CPU and I/O tier, and an agent's children inherit it, so
// a build agent uses Adaptive instead; a controller only serves HTTP and is
// genuinely a background job.
func servicePlistProcessType(role string) string {
	if role == RoleAgent {
		return "Adaptive"
	}
	return "Background"
}

func serviceDescriptionRole(role string) string {
	switch role {
	case RoleController:
		return "controller"
	case RoleAgent:
		return "agent"
	default:
		return "task runner"
	}
}
