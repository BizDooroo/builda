package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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
	b.WriteString("Description=Builda task runner\n")
	b.WriteString("After=network-online.target\n\n")
	b.WriteString("[Service]\n")
	b.WriteString("Type=simple\n")
	b.WriteString("ExecStart=")
	b.WriteString(strings.Join(systemdQuoteArgs(args), " "))
	b.WriteString("\n")
	b.WriteString("Restart=always\n")
	b.WriteString("RestartSec=5s\n\n")
	b.WriteString("[Install]\n")
	b.WriteString("WantedBy=default.target\n")
	return b.String()
}

func renderLaunchdPlist(spec serviceSpec) (string, error) {
	logDir, err := userLogDir()
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
	writePlistTrue(&b, "RunAtLoad")
	writePlistTrue(&b, "KeepAlive")
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

func serviceExecArgs(spec serviceSpec) []string {
	args := []string{spec.BinaryPath, "serve", "--config", spec.ConfigPath}
	if len(spec.Addrs) == 0 {
		return args
	}
	for _, addr := range resolveListenAddresses(nil, spec.Addrs) {
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

func userLogDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Logs"), nil
}
