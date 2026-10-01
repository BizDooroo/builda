package main

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// helpText renders the legacy standalone configuration guide.
func helpText(configPath string) string {
	return fmt.Sprintf(configHelp, configPath)
}

// Legacy standalone server. It is retained only so an existing installation
// can keep serving while its history is exported and imported into a
// controller. New operation uses the controller and agent roles.
const legacyDeprecationNotice = "builda serve runs the legacy standalone role; new deployments should use \"builda controller serve\" and \"builda agent run\""

func newLegacyServeCommand(opts *serveOptions) *cobra.Command {
	serveCmd := &cobra.Command{
		Use:          "serve",
		Short:        "Run the legacy standalone task server (deprecated)",
		Long:         legacyDeprecationNotice + "\n" + strings.TrimSpace(helpText(opts.configPath)),
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Print(legacyDeprecationNotice)
			return runServer(opts.configPath, opts.addrs, !flagChanged(cmd, "config"))
		},
	}
	bindRoleFlags(serveCmd, opts, true)
	return serveCmd
}

func newLegacyConfigCommand(opts *serveOptions) *cobra.Command {
	cmd := newRoleConfigCommand(opts, sampleConfig, func(data []byte) error {
		_, err := parseConfig(data)
		return err
	})
	cmd.Short = "Inspect or replace the legacy standalone config file"
	bindRoleFlags(cmd, opts, false)
	return cmd
}

func newSampleConfigCommand() *cobra.Command {
	cmd := newRoleSampleConfigCommand(sampleConfig)
	cmd.Short = "Print a starter legacy standalone config.yaml"
	return cmd
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
	if err := os.MkdirAll(cfg.Server.LogDir, 0o755); err != nil {
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
