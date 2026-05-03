package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

const (
	defaultServiceName = "builda"
	launchdLabelPrefix = "com.bizdooroo"
)

type serviceOptions struct {
	name       string
	binaryPath string
	start      bool
	force      bool
	dryRun     bool
	targetOS   string
}

type serviceSpec struct {
	Name       string
	TargetOS   string
	BinaryPath string
	ConfigPath string
	Addrs      []string
}

type serviceArtifact struct {
	Path    string
	Content string
}

type serviceCommand struct {
	Name         string
	Args         []string
	StreamOutput bool
	IgnoreError  bool
}

func newServiceCommand(serveOpts *serveOptions) *cobra.Command {
	opts := &serviceOptions{
		name:     defaultServiceName,
		start:    true,
		targetOS: runtime.GOOS,
	}

	serviceCmd := &cobra.Command{
		Use:   "service",
		Short: "Install or remove Builda as a user daemon",
		Long: strings.TrimSpace(`Install Builda as a user-level daemon.

On Linux, Builda writes a systemd user unit under ~/.config/systemd/user.
On macOS, Builda writes a launchd LaunchAgent under ~/Library/LaunchAgents.
Builda is internal-only software; do not bind it to untrusted networks.`),
		SilenceUsage: true,
	}

	installCmd := &cobra.Command{
		Use:          "install",
		Short:        "Install and enable the user daemon",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServiceInstall(cmd, serveOpts, opts)
		},
	}
	bindServiceFlags(installCmd, opts)
	installCmd.Flags().BoolVar(&opts.start, "start", true, "start or restart the service after installation")
	installCmd.Flags().BoolVar(&opts.force, "force", false, "overwrite an existing service file")
	installCmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "print the service file without writing or running commands")
	installCmd.Flags().StringVar(&opts.targetOS, "target", opts.targetOS, "service target for generated output: linux or darwin")

	uninstallCmd := &cobra.Command{
		Use:          "uninstall",
		Short:        "Disable and remove the user daemon",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServiceUninstall(cmd, opts)
		},
	}
	bindServiceFlags(uninstallCmd, opts)
	uninstallCmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "print planned actions without writing or running commands")

	printCmd := &cobra.Command{
		Use:          "print",
		Short:        "Print the service file for Linux or macOS",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, err := buildServiceSpec(serveOpts, opts, false)
			if err != nil {
				return err
			}
			artifact, err := renderServiceArtifact(spec)
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), artifact.Content)
			return nil
		},
	}
	bindServiceFlags(printCmd, opts)
	printCmd.Flags().StringVar(&opts.targetOS, "target", opts.targetOS, "service target to print: linux or darwin")

	serviceCmd.AddCommand(
		installCmd,
		uninstallCmd,
		printCmd,
		newServiceControlCommand(opts, "start"),
		newServiceControlCommand(opts, "stop"),
		newServiceControlCommand(opts, "restart"),
		newServiceControlCommand(opts, "status"),
	)
	return serviceCmd
}

func bindServiceFlags(cmd *cobra.Command, opts *serviceOptions) {
	cmd.Flags().StringVar(&opts.name, "name", opts.name, "service name")
	cmd.Flags().StringVar(&opts.binaryPath, "binary", opts.binaryPath, "path to the builda binary; defaults to the current executable")
}

func bindServiceNameFlag(cmd *cobra.Command, opts *serviceOptions) {
	cmd.Flags().StringVar(&opts.name, "name", opts.name, "service name")
}

func newServiceControlCommand(opts *serviceOptions, action string) *cobra.Command {
	cmd := &cobra.Command{
		Use:          action,
		Short:        titleServiceAction(action) + " the user daemon",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServiceControl(cmd, opts, action)
		},
	}
	bindServiceNameFlag(cmd, opts)
	return cmd
}

func titleServiceAction(action string) string {
	if action == "" {
		return action
	}
	return strings.ToUpper(action[:1]) + action[1:]
}

func runServiceInstall(cmd *cobra.Command, serveOpts *serveOptions, opts *serviceOptions) error {
	spec, err := buildServiceSpec(serveOpts, opts, !opts.dryRun)
	if err != nil {
		return err
	}
	if !opts.dryRun && spec.TargetOS != runtime.GOOS {
		return fmt.Errorf("cannot install %s service on %s; use --dry-run or service print to generate files for another OS", spec.TargetOS, runtime.GOOS)
	}
	artifact, err := renderServiceArtifact(spec)
	if err != nil {
		return err
	}
	if opts.dryRun {
		fmt.Fprintf(cmd.OutOrStdout(), "# %s\n%s", artifact.Path, artifact.Content)
		return nil
	}
	if _, err := os.Stat(artifact.Path); err == nil && !opts.force {
		return fmt.Errorf("%s already exists; use --force to overwrite", artifact.Path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(artifact.Path), 0755); err != nil {
		return err
	}
	if err := writeFileAtomic(artifact.Path, []byte(artifact.Content), 0644); err != nil {
		return err
	}
	if err := enableService(spec, artifact.Path, opts.start); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "installed %s\n", artifact.Path)
	return nil
}

func runServiceUninstall(cmd *cobra.Command, opts *serviceOptions) error {
	name, err := normalizeServiceName(opts.name)
	if err != nil {
		return err
	}
	targetOS := runtime.GOOS
	if targetOS != "linux" && targetOS != "darwin" {
		return fmt.Errorf("service uninstall is not supported on %s", targetOS)
	}
	path, err := servicePath(targetOS, name)
	if err != nil {
		return err
	}
	if opts.dryRun {
		fmt.Fprintf(cmd.OutOrStdout(), "remove %s\n", path)
		return nil
	}
	if err := disableService(targetOS, name, path); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", path)
	return nil
}

func runServiceControl(cmd *cobra.Command, opts *serviceOptions, action string) error {
	name, err := normalizeServiceName(opts.name)
	if err != nil {
		return err
	}
	targetOS := runtime.GOOS
	if targetOS != "linux" && targetOS != "darwin" {
		return fmt.Errorf("service %s is not supported on %s", action, targetOS)
	}
	path, err := servicePath(targetOS, name)
	if err != nil {
		return err
	}
	commands, err := serviceControlCommands(targetOS, name, path, action)
	if err != nil {
		return err
	}
	for _, command := range commands {
		var runErr error
		if command.StreamOutput {
			runErr = runCommandOutput(cmd.OutOrStdout(), command.Name, command.Args...)
		} else {
			runErr = runCommand(command.Name, command.Args...)
		}
		if runErr != nil && !command.IgnoreError {
			return runErr
		}
	}
	return nil
}

func buildServiceSpec(serveOpts *serveOptions, opts *serviceOptions, ensureConfig bool) (serviceSpec, error) {
	name, err := normalizeServiceName(opts.name)
	if err != nil {
		return serviceSpec{}, err
	}
	targetOS := strings.TrimSpace(opts.targetOS)
	if targetOS == "" {
		targetOS = runtime.GOOS
	}
	if targetOS != "linux" && targetOS != "darwin" {
		return serviceSpec{}, fmt.Errorf("service target must be linux or darwin, got %q", targetOS)
	}
	binaryPath := strings.TrimSpace(opts.binaryPath)
	if binaryPath == "" {
		binaryPath, err = os.Executable()
		if err != nil {
			return serviceSpec{}, fmt.Errorf("resolve current executable: %w", err)
		}
	}
	binaryPath, err = filepath.Abs(binaryPath)
	if err != nil {
		return serviceSpec{}, err
	}
	configPath := strings.TrimSpace(serveOpts.configPath)
	if configPath == "" {
		configPath = defaultConfigPath()
	}
	configPath, err = filepath.Abs(configPath)
	if err != nil {
		return serviceSpec{}, err
	}
	if ensureConfig {
		if err := ensureDefaultConfig(configPath); err != nil {
			return serviceSpec{}, fmt.Errorf("initialize config: %w", err)
		}
	}
	return serviceSpec{
		Name:       name,
		TargetOS:   targetOS,
		BinaryPath: binaryPath,
		ConfigPath: configPath,
		Addrs:      append([]string(nil), serveOpts.addrs...),
	}, nil
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
	for _, addr := range resolveListenAddresses("", spec.Addrs) {
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
