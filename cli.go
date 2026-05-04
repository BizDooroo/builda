package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type serveOptions struct {
	configPath string
	addrs      addressFlags
}

func newRootCommand() *cobra.Command {
	serveOpts := &serveOptions{
		configPath: defaultConfigPath(),
	}

	root := &cobra.Command{
		Use:           "builda",
		Short:         "Run the Builda web task runner",
		Long:          strings.TrimSpace(helpText(serveOpts.configPath)),
		Version:       versionInfo(),
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServeCommand(cmd, serveOpts)
		},
	}
	root.SetVersionTemplate("{{.Version}}\n")
	bindServePersistentFlags(root, serveOpts)

	serveCmd := &cobra.Command{
		Use:          "serve",
		Short:        "Run the Builda HTTP server",
		Long:         strings.TrimSpace(helpText(serveOpts.configPath)),
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServeCommand(cmd, serveOpts)
		},
	}

	root.AddCommand(
		serveCmd,
		newVersionCommand(),
		newSampleConfigCommand(),
		newConfigCommand(serveOpts),
		newServiceCommand(serveOpts),
	)
	return root
}

func bindServePersistentFlags(cmd *cobra.Command, opts *serveOptions) {
	cmd.PersistentFlags().StringVar(&opts.configPath, "config", opts.configPath, "YAML configuration file")
	cmd.PersistentFlags().Var(&opts.addrs, "addr", "HTTP listen address; repeat to bind multiple interfaces and override server.address/server.addresses")
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

func newSampleConfigCommand() *cobra.Command {
	return &cobra.Command{
		Use:          "sample-config",
		Short:        "Print a starter config.yaml",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprint(cmd.OutOrStdout(), sampleConfig)
		},
	}
}

func newConfigCommand(serveOpts *serveOptions) *cobra.Command {
	configCmd := &cobra.Command{
		Use:          "config",
		Short:        "Inspect or replace the Builda config file",
		SilenceUsage: true,
	}

	pathCmd := &cobra.Command{
		Use:          "path",
		Short:        "Print the active config file path",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := commandConfigPath(serveOpts.configPath)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), path)
			return nil
		},
	}

	getCmd := &cobra.Command{
		Use:          "get",
		Short:        "Print the active config file",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := commandConfigPath(serveOpts.configPath)
			if err != nil {
				return err
			}
			if !flagChanged(cmd, "config") {
				if err := ensureDefaultConfig(path); err != nil {
					return fmt.Errorf("initialize default config: %w", err)
				}
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), string(data))
			return nil
		},
	}

	setCmd := &cobra.Command{
		Use:          "set [file]",
		Short:        "Validate and replace the active config file",
		SilenceUsage: true,
		Args:         cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := commandConfigPath(serveOpts.configPath)
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
			if _, err := parseConfig(data); err != nil {
				return fmt.Errorf("invalid config: %w", err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				return err
			}
			if err := writeFileAtomic(path, data, 0644); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "updated %s\n", path)
			return nil
		},
	}

	configCmd.AddCommand(pathCmd, getCmd, setCmd)
	return configCmd
}

func runServeCommand(cmd *cobra.Command, opts *serveOptions) error {
	ensureConfig := !flagChanged(cmd, "config")
	return runServer(opts.configPath, opts.addrs, ensureConfig)
}

func runServer(configPath string, addrs []string, ensureConfig bool) error {
	if ensureConfig {
		if err := ensureDefaultConfig(configPath); err != nil {
			return fmt.Errorf("initialize default config: %w", err)
		}
	}
	cfg, err := loadRuntimeConfig(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	listenAddrs := resolveListenAddresses(configuredListenAddresses(cfg.Server), addrs)
	if err := os.MkdirAll(cfg.Server.LogDir, 0755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "unknown-host"
	}

	runner := NewRunner(cfg.Server.LogDir, cfg.Server.MaxHistory)
	app := &App{
		cfg:        cfg,
		tasks:      buildTaskMap(cfg.Tasks),
		configPath: configPath,
		runner:     runner,
		logDir:     cfg.Server.LogDir,
		hostname:   hostname,
		started:    time.Now(),
	}
	if stamp, err := statFileStamp(configPath); err == nil {
		app.configFile = stamp
	}

	log.Printf("config %s", configPath)
	log.Printf("logs %s", cfg.Server.LogDir)
	go app.watchConfig(configReloadInterval)
	return serveHTTP(listenAddrs, app.routes())
}

func helpText(configPath string) string {
	return fmt.Sprintf(configHelp, configPath)
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
	if cmd.PersistentFlags().Changed(name) {
		return true
	}
	return false
}
