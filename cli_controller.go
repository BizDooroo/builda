package main

import (
	"bufio"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newControllerCommand() *cobra.Command {
	opts := &serveOptions{configPath: roleConfigPath(RoleController)}

	controllerCmd := &cobra.Command{
		Use:          "controller",
		Short:        "Run and manage the Builda controller",
		Long:         strings.TrimSpace(controllerHelp),
		SilenceUsage: true,
	}
	bindRoleFlags(controllerCmd, opts, true)

	serveCmd := &cobra.Command{
		Use:          "serve",
		Short:        "Run the controller HTTP server",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runControllerServe(cmd, opts)
		},
	}

	controllerCmd.AddCommand(
		serveCmd,
		newControllerAdminCommand(opts),
		newControllerAgentCommand(opts),
		newControllerTokenCommand(opts),
		newRoleConfigCommand(opts, sampleControllerConfig, func(data []byte) error {
			if err := requireConfigRole("config", data, RoleController); err != nil {
				return err
			}
			_, err := parseControllerConfig(data)
			return err
		}),
		newRoleServiceCommand(RoleController, opts),
		newRoleSampleConfigCommand(sampleControllerConfig),
	)
	return controllerCmd
}

func runControllerServe(cmd *cobra.Command, opts *serveOptions) error {
	path, err := commandConfigPath(opts.configPath)
	if err != nil {
		return err
	}
	if !flagChanged(cmd, "config") {
		if err := ensureRoleConfig(path, sampleControllerConfig); err != nil {
			return fmt.Errorf("initialize controller config: %w", err)
		}
	}
	cfg, err := loadControllerConfig(path)
	if err != nil {
		return fmt.Errorf("load controller config: %w", err)
	}
	controller, err := newController(path, cfg, opts.addrs)
	if err != nil {
		return err
	}
	defer controller.Close()

	runtime := controller.Runtime()
	log.Printf("config %s", path)
	log.Printf("state %s", runtime.StateDir)
	if !controller.auth.HasAdmin() {
		log.Printf("no admin credential yet; run %q on this host before signing in", "builda controller admin set-password")
	}
	go controller.schedulerLoop(time.Second)
	go controller.watchConfig(configReloadInterval)
	return serveControllerHTTP(runtime.ListenAddresses, newControllerAPI(controller).routes())
}

// watchConfig hot-reloads job, catalog, and agent edits without a restart.
func (c *Controller) watchConfig(interval time.Duration) {
	if interval <= 0 {
		interval = configReloadInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-ticker.C:
		}
		if err := c.reloadConfigIfChanged(); err != nil {
			log.Printf("reload controller config: %v", err)
		}
	}
}

// newControllerAdminCommand bootstraps the single admin credential locally.
// External admin sign-in is impossible until this has run on the host.
func newControllerAdminCommand(opts *serveOptions) *cobra.Command {
	adminCmd := &cobra.Command{
		Use:          "admin",
		Short:        "Manage the controller administrator credential",
		SilenceUsage: true,
	}
	var username string
	var passwordFile string
	setCmd := &cobra.Command{
		Use:          "set-password",
		Short:        "Create or rotate the admin password on this host",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			auth, err := controllerAuthStore(opts)
			if err != nil {
				return err
			}
			password, err := readPasswordInput(cmd, passwordFile)
			if err != nil {
				return err
			}
			if err := auth.SetAdminPassword(username, password); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "updated admin credential for %q\n", username)
			return nil
		},
	}
	setCmd.Flags().StringVar(&username, "user", adminUserName, "administrator user name")
	setCmd.Flags().StringVar(&passwordFile, "password-file", "", "read the password from a file instead of the terminal")
	adminCmd.AddCommand(setCmd)
	return adminCmd
}

// newControllerAgentCommand issues and revokes per-agent tokens. The raw token
// is printed once and only its hash is stored.
func newControllerAgentCommand(opts *serveOptions) *cobra.Command {
	agentCmd := &cobra.Command{
		Use:          "agent",
		Short:        "Manage agent enrollment tokens",
		SilenceUsage: true,
	}
	agentCmd.AddCommand(&cobra.Command{
		Use:          "token <agent-id>",
		Short:        "Issue or rotate the token of one agent",
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, auth, err := controllerConfigAndAuth(opts)
			if err != nil {
				return err
			}
			if _, ok := findAgentDefinition(cfg, args[0]); !ok {
				return fmt.Errorf("agent %q is not configured; add it under agents: first", args[0])
			}
			secret, _, err := auth.IssueAgentToken(args[0])
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), secret)
			return nil
		},
	})
	agentCmd.AddCommand(&cobra.Command{
		Use:          "revoke <agent-id>",
		Short:        "Revoke the token of one agent",
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			auth, err := controllerAuthStore(opts)
			if err != nil {
				return err
			}
			if err := auth.RevokeAgentToken(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "revoked agent token for %s\n", args[0])
			return nil
		},
	})
	return agentCmd
}

func newControllerTokenCommand(opts *serveOptions) *cobra.Command {
	tokenCmd := &cobra.Command{
		Use:          "token",
		Short:        "Manage API bearer tokens for external automation",
		SilenceUsage: true,
	}
	tokenCmd.AddCommand(&cobra.Command{
		Use:          "create <name>",
		Short:        "Issue an API bearer token and print it once",
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			auth, err := controllerAuthStore(opts)
			if err != nil {
				return err
			}
			secret, _, err := auth.IssueAPIToken(args[0])
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), secret)
			return nil
		},
	})
	tokenCmd.AddCommand(&cobra.Command{
		Use:          "list",
		Short:        "List API token metadata",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			auth, err := controllerAuthStore(opts)
			if err != nil {
				return err
			}
			for _, token := range auth.APITokens() {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", token.ID, token.Name, token.CreatedAt.Format(time.RFC3339))
			}
			return nil
		},
	})
	tokenCmd.AddCommand(&cobra.Command{
		Use:          "revoke <token-id>",
		Short:        "Revoke one API token",
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			auth, err := controllerAuthStore(opts)
			if err != nil {
				return err
			}
			if err := auth.RevokeAPIToken(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "revoked %s\n", args[0])
			return nil
		},
	})
	return tokenCmd
}

func controllerConfigAndAuth(opts *serveOptions) (ControllerConfig, *AuthStore, error) {
	path, err := commandConfigPath(opts.configPath)
	if err != nil {
		return ControllerConfig{}, nil, err
	}
	if err := ensureRoleConfig(path, sampleControllerConfig); err != nil {
		return ControllerConfig{}, nil, err
	}
	cfg, err := loadControllerConfig(path)
	if err != nil {
		return ControllerConfig{}, nil, err
	}
	runtime, err := controllerRuntime(path, cfg)
	if err != nil {
		return ControllerConfig{}, nil, err
	}
	if err := os.MkdirAll(runtime.StateDir, 0o700); err != nil {
		return ControllerConfig{}, nil, err
	}
	auth, err := newAuthStore(runtime.CredentialsPath)
	if err != nil {
		return ControllerConfig{}, nil, err
	}
	return cfg, auth, nil
}

func controllerAuthStore(opts *serveOptions) (*AuthStore, error) {
	_, auth, err := controllerConfigAndAuth(opts)
	return auth, err
}

// readPasswordInput reads a password from a file or from stdin. On an
// interactive terminal the prompt is repeated and echo is disabled through
// stty, which keeps the binary dependency-free.
func readPasswordInput(cmd *cobra.Command, passwordFile string) (string, error) {
	if strings.TrimSpace(passwordFile) != "" {
		data, err := os.ReadFile(passwordFile)
		if err != nil {
			return "", err
		}
		return strings.TrimRight(string(data), "\r\n"), nil
	}
	reader := bufio.NewReader(cmd.InOrStdin())
	if !stdinIsTerminal() {
		line, err := reader.ReadString('\n')
		if err != nil && strings.TrimSpace(line) == "" {
			return "", errors.New("no password was provided on stdin")
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	restore := disableTerminalEcho()
	defer restore()
	fmt.Fprint(cmd.OutOrStdout(), "New password: ")
	first, err := reader.ReadString('\n')
	fmt.Fprintln(cmd.OutOrStdout())
	if err != nil {
		return "", err
	}
	fmt.Fprint(cmd.OutOrStdout(), "Repeat password: ")
	second, err := reader.ReadString('\n')
	fmt.Fprintln(cmd.OutOrStdout())
	if err != nil {
		return "", err
	}
	first = strings.TrimRight(first, "\r\n")
	second = strings.TrimRight(second, "\r\n")
	if first != second {
		return "", errors.New("passwords did not match")
	}
	return first, nil
}

func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// disableTerminalEcho hides typed characters and returns a restore function.
func disableTerminalEcho() func() {
	if err := runTerminalCommand("stty", "-echo"); err != nil {
		return func() {}
	}
	return func() { _ = runTerminalCommand("stty", "echo") }
}

func runTerminalCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	return cmd.Run()
}
