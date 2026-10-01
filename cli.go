package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// serveOptions carries the config path and listen overrides shared by every
// role command and by the service installers.
type serveOptions struct {
	configPath string
	addrs      addressFlags
}

func newRootCommand() *cobra.Command {
	legacyOpts := &serveOptions{configPath: defaultConfigPath()}

	root := &cobra.Command{
		Use:           "builda",
		Short:         "Run the Builda build controller or agent",
		Long:          strings.TrimSpace(rootHelp),
		Version:       versionInfo(),
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	root.SetVersionTemplate("{{.Version}}\n")

	root.AddCommand(
		newControllerCommand(),
		newAgentCommand(),
		newMigrateCommand(),
		newVersionCommand(),
		newLegacyServeCommand(legacyOpts),
		newLegacyConfigCommand(legacyOpts),
		newServiceCommand(legacyOpts),
		newSampleConfigCommand(),
	)
	return root
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:          "version",
		Short:        "Print version information",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintln(cmd.OutOrStdout(), versionInfo())
		},
	}
}

// bindRoleFlags binds the config path and listen overrides for a role command.
func bindRoleFlags(cmd *cobra.Command, opts *serveOptions, withAddr bool) {
	cmd.PersistentFlags().StringVar(&opts.configPath, "config", opts.configPath, "YAML configuration file")
	if withAddr {
		cmd.PersistentFlags().Var(&opts.addrs, "addr", "HTTP listen address; repeat to bind multiple interfaces and override server.address/server.addresses")
	}
}

// newRoleConfigCommand builds the shared config path/get/set command group.
// Validation always runs before the active config file is replaced.
func newRoleConfigCommand(opts *serveOptions, sample string, validate func([]byte) error) *cobra.Command {
	configCmd := &cobra.Command{
		Use:          "config",
		Short:        "Inspect or replace the active config file",
		SilenceUsage: true,
	}

	configCmd.AddCommand(&cobra.Command{
		Use:          "path",
		Short:        "Print the active config file path",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := commandConfigPath(opts.configPath)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), path)
			return nil
		},
	})

	configCmd.AddCommand(&cobra.Command{
		Use:          "get",
		Short:        "Print the active config file",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := commandConfigPath(opts.configPath)
			if err != nil {
				return err
			}
			if err := ensureRoleConfig(path, sample); err != nil {
				return fmt.Errorf("initialize config: %w", err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), string(data))
			return nil
		},
	})

	configCmd.AddCommand(&cobra.Command{
		Use:          "set [file]",
		Short:        "Validate and replace the active config file",
		SilenceUsage: true,
		Args:         cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := commandConfigPath(opts.configPath)
			if err != nil {
				return err
			}
			var data []byte
			if len(args) == 1 {
				data, err = os.ReadFile(args[0])
			} else {
				data, err = io.ReadAll(cmd.InOrStdin())
			}
			if err != nil {
				return err
			}
			if err := validate(data); err != nil {
				return fmt.Errorf("invalid config: %w", err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := writeFileAtomicSync(path, data, 0o600); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "updated %s\n", path)
			return nil
		},
	})

	return configCmd
}

func newRoleSampleConfigCommand(sample string) *cobra.Command {
	return &cobra.Command{
		Use:          "sample-config",
		Short:        "Print a starter config file for this role",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprint(cmd.OutOrStdout(), sample)
		},
	}
}

func commandConfigPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = defaultConfigPath()
	}
	return filepath.Abs(path)
}

func flagChanged(cmd *cobra.Command, name string) bool {
	if cmd.Flags().Changed(name) {
		return true
	}
	if cmd.InheritedFlags().Changed(name) {
		return true
	}
	return cmd.PersistentFlags().Changed(name)
}
