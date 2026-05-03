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
