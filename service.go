package main

import (
	"fmt"
	"runtime"
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
