package main

import (
	"fmt"
	"os"
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
	role       string
	binaryPath string
	start      bool
	force      bool
	dryRun     bool
	targetOS   string
}

type serviceSpec struct {
	Name       string
	Role       string
	TargetOS   string
	BinaryPath string
	ConfigPath string
	Addrs      []string
}

type serviceArtifact struct {
	Path    string
	Content string
}

// serviceCommand is one step of a platform service plan. The ignore flags
// encode the idempotence rules of the underlying tool so repeating an install,
// a start, or a stop is always safe.
type serviceCommand struct {
	Name          string
	Args          []string
	StreamOutput  bool
	IgnoreError   bool
	IgnoreMissing bool
	IgnoreLoaded  bool
	Hint          string
}

func newServiceCommand(serveOpts *serveOptions) *cobra.Command {
	return newRoleServiceCommand(RoleStandalone, serveOpts)
}

// newRoleServiceCommand builds the user-scoped service command group for one
// role so a controller and an agent can be installed side by side.
func newRoleServiceCommand(role string, serveOpts *serveOptions) *cobra.Command {
	opts := &serviceOptions{
		name:     roleDefaultServiceName(role),
		role:     role,
		start:    true,
		targetOS: runtime.GOOS,
	}

	serviceCmd := &cobra.Command{
		Use:   "service",
		Short: "Install or remove this Builda role as a user daemon",
		Long: strings.TrimSpace(`Install Builda as a user-level daemon.

On Linux, Builda writes a systemd user unit under ~/.config/systemd/user.
On macOS, Builda writes a launchd LaunchAgent under ~/Library/LaunchAgents.

The service runs the Builda executable directly. There is no shell, no login
session, and no wrapper script, so the daemon never opens a terminal or
sources an interactive profile. Install validates that the target is a stable
executable regular file and that the role config parses before writing
anything, creates the redirected log directory, clears any persistent launchd
"disabled" override before bootstrapping, and relies on RunAtLoad for the
single startup instead of following bootstrap with "kickstart -k".

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

	if role == RoleStandalone {
		// The legacy service group sits directly under the root command, so it
		// owns the config and listen flags itself.
		bindRoleFlags(serviceCmd, serveOpts, true)
	}
	serviceCmd.AddCommand(
		installCmd,
		uninstallCmd,
		printCmd,
		newServiceDiagnoseCommand(serveOpts, opts),
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

// serviceActionSummaries keep the command list readable; "status the daemon"
// is not a sentence.
var serviceActionSummaries = map[string]string{
	"start":   "Start the user daemon if it is not already running",
	"stop":    "Stop the user daemon",
	"restart": "Restart the user daemon",
	"status":  "Print the current state of the user daemon",
}

func newServiceControlCommand(opts *serviceOptions, action string) *cobra.Command {
	cmd := &cobra.Command{
		Use:          action,
		Short:        serviceActionSummaries[action],
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServiceControl(cmd, opts, action)
		},
	}
	bindServiceNameFlag(cmd, opts)
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "print the planned commands without running them")
	return cmd
}

// roleDefaultServiceName keeps controller and agent units distinct on a host
// that runs both.
func roleDefaultServiceName(role string) string {
	switch role {
	case RoleController:
		return controllerServiceName
	case RoleAgent:
		return agentServiceName
	default:
		return defaultServiceName
	}
}

// newServiceDiagnoseCommand reports everything needed to repair a user daemon
// without changing any of it.
func newServiceDiagnoseCommand(serveOpts *serveOptions, opts *serviceOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:          "diagnose",
		Short:        "Report service file, domain, target, and config health without changing anything",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServiceDiagnose(cmd, serveOpts, opts)
		},
	}
	bindServiceFlags(cmd, opts)
	return cmd
}

func runServiceDiagnose(cmd *cobra.Command, serveOpts *serveOptions, opts *serviceOptions) error {
	spec, err := buildServiceSpec(serveOpts, opts, false)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	artifact, err := renderServiceArtifact(spec)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "role          %s\n", spec.Role)
	fmt.Fprintf(out, "service name  %s\n", spec.Name)
	fmt.Fprintf(out, "service file  %s %s\n", artifact.Path, fileState(artifact.Path))
	fmt.Fprintf(out, "binary        %s %s\n", spec.BinaryPath, checkState(validateServiceBinary(spec.BinaryPath)))
	fmt.Fprintf(out, "config        %s %s\n", spec.ConfigPath, checkState(validateServiceRoleConfig(spec.Role, spec.ConfigPath)))
	if spec.TargetOS == "darwin" {
		logDir, _ := serviceLogDir()
		fmt.Fprintf(out, "domain        %s\n", launchdDomain())
		fmt.Fprintf(out, "label         %s\n", launchdLabel(spec.Name))
		fmt.Fprintf(out, "log dir       %s %s\n", logDir, fileState(logDir))
		fmt.Fprintf(out, "disabled      %s\n", launchdDisabledState(spec.Name))
	}
	fmt.Fprintf(out, "\n# repair\n%s service install --force --binary %q\n",
		serviceRepairCommand(spec.Role), spec.BinaryPath)
	fmt.Fprintf(out, "%s service restart\n", serviceRepairCommand(spec.Role))
	fmt.Fprintf(out, "\n# planned start commands\n")
	plan, err := serviceControlCommands(spec.TargetOS, spec.Name, artifact.Path, "start")
	if err != nil {
		return err
	}
	fmt.Fprint(out, renderServicePlan(plan))
	return nil
}

func serviceRepairCommand(role string) string {
	switch role {
	case RoleController:
		return "builda controller"
	case RoleAgent:
		return "builda agent"
	default:
		return "builda"
	}
}

func fileState(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return "(missing)"
	}
	if info.IsDir() {
		return "(directory)"
	}
	return "(present)"
}

func checkState(err error) string {
	if err == nil {
		return "(ok)"
	}
	return "(" + err.Error() + ")"
}

// launchdDisabledState reports the persistent per-user override of a label.
func launchdDisabledState(name string) string {
	output, err := runCommandCapture("launchctl", "print-disabled", launchdDomain())
	if err != nil {
		return "unknown: " + err.Error()
	}
	label := launchdLabel(name)
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "\""+label+"\"") {
			return strings.TrimSpace(line)
		}
	}
	return "no persistent override for " + label
}
