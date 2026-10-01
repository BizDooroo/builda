package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

// runServiceInstall writes the user service file and loads it exactly once.
// Validation happens before anything is written so a bad target never becomes
// a crash-looping daemon.
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
	plan, err := serviceEnablePlan(spec, artifact.Path, opts.start)
	if err != nil {
		return err
	}
	if opts.dryRun {
		// A dry run reports validation instead of failing, so a plan can be
		// generated for another host.
		fmt.Fprintf(cmd.OutOrStdout(), "# %s\n%s", artifact.Path, artifact.Content)
		fmt.Fprintf(cmd.OutOrStdout(), "\n# binary %s\n# config %s\n",
			checkState(validateServiceBinary(spec.BinaryPath)),
			checkState(validateServiceRoleConfig(spec.Role, spec.ConfigPath)))
		fmt.Fprintf(cmd.OutOrStdout(), "# planned commands\n%s", renderServicePlan(plan))
		return nil
	}
	if err := validateServiceBinary(spec.BinaryPath); err != nil {
		return err
	}
	if err := validateServiceRoleConfig(spec.Role, spec.ConfigPath); err != nil {
		return err
	}
	if _, err := os.Stat(artifact.Path); err == nil && !opts.force {
		return fmt.Errorf("%s already exists; use --force to overwrite", artifact.Path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := ensureServiceDirectories(spec, artifact.Path); err != nil {
		return err
	}
	if err := writeFileAtomic(artifact.Path, []byte(artifact.Content), 0o644); err != nil {
		return err
	}
	if err := runServiceCommands(cmd.OutOrStdout(), plan); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "installed %s\n", artifact.Path)
	if spec.TargetOS == "darwin" {
		fmt.Fprintf(cmd.OutOrStdout(), "domain %s\nlabel %s\n", launchdDomain(), launchdLabel(spec.Name))
	}
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
	plan, err := serviceDisablePlan(targetOS, name, path)
	if err != nil {
		return err
	}
	if opts.dryRun {
		fmt.Fprintf(cmd.OutOrStdout(), "# planned commands\n%sremove %s\n", renderServicePlan(plan), path)
		return nil
	}
	if err := runServiceCommands(cmd.OutOrStdout(), plan); err != nil {
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
	if opts.dryRun {
		fmt.Fprint(cmd.OutOrStdout(), renderServicePlan(commands))
		return nil
	}
	return runServiceCommands(cmd.OutOrStdout(), commands)
}

// renderServicePlan prints a plan so --dry-run shows the exact command order.
func renderServicePlan(commands []serviceCommand) string {
	var b strings.Builder
	for _, command := range commands {
		b.WriteString(command.Name)
		for _, arg := range command.Args {
			b.WriteString(" ")
			b.WriteString(arg)
		}
		b.WriteString("\n")
	}
	return b.String()
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
		configPath = roleDefaultConfigPath(opts.role)
	}
	configPath, err = filepath.Abs(configPath)
	if err != nil {
		return serviceSpec{}, err
	}
	if ensureConfig {
		if err := ensureRoleConfig(configPath, roleSampleConfig(opts.role)); err != nil {
			return serviceSpec{}, fmt.Errorf("initialize config: %w", err)
		}
	}
	return serviceSpec{
		Name:       name,
		Role:       opts.role,
		TargetOS:   targetOS,
		BinaryPath: binaryPath,
		ConfigPath: configPath,
		Addrs:      append([]string(nil), serveOpts.addrs...),
	}, nil
}

func roleDefaultConfigPath(role string) string {
	switch role {
	case RoleController, RoleAgent:
		return roleConfigPath(role)
	default:
		return defaultConfigPath()
	}
}

func roleSampleConfig(role string) string {
	switch role {
	case RoleController:
		return sampleControllerConfig
	case RoleAgent:
		return sampleAgentConfig
	default:
		return sampleConfig
	}
}
