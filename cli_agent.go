package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
)

func newAgentCommand() *cobra.Command {
	opts := &serveOptions{configPath: roleConfigPath(RoleAgent)}

	agentCmd := &cobra.Command{
		Use:          "agent",
		Short:        "Run and manage a Builda agent",
		Long:         strings.TrimSpace(agentHelp),
		SilenceUsage: true,
	}
	bindRoleFlags(agentCmd, opts, false)

	runCmd := &cobra.Command{
		Use:          "run",
		Short:        "Connect to the controller and run assigned jobs",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAgentCommand(cmd, opts)
		},
	}

	var tokenFile string
	enrollCmd := &cobra.Command{
		Use:          "enroll",
		Short:        "Store the per-agent token issued by the controller",
		Long:         "Reads the token from --token-file or stdin and stores it in the agent credential file with mode 0600.",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			runtime, err := agentRuntimeFromOptions(cmd, opts)
			if err != nil {
				return err
			}
			token, err := readTokenInput(cmd, tokenFile)
			if err != nil {
				return err
			}
			if err := writeAgentToken(runtime.CredentialsPath, runtime.ID, token); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "enrolled %s at %s\n", runtime.ID, runtime.CredentialsPath)
			return nil
		},
	}
	enrollCmd.Flags().StringVar(&tokenFile, "token-file", "", "read the token from a file instead of stdin")

	statusCmd := &cobra.Command{
		Use:          "status",
		Short:        "Print the local agent spool state",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			runtime, err := agentRuntimeFromOptions(cmd, opts)
			if err != nil {
				return err
			}
			journal, err := newAgentJournal(runtime.ExecDir)
			if err != nil {
				return err
			}
			entries, err := journal.List()
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "agent %s -> %s\n", runtime.ID, runtime.ControllerURL)
			fmt.Fprintf(cmd.OutOrStdout(), "spool %s\n", runtime.SpoolDir)
			if len(entries) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no tracked executions")
				return nil
			}
			for _, entry := range entries {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", entry.ExecutionID, entry.Phase, entry.Status, entry.Attention)
			}
			return nil
		},
	}

	agentCmd.AddCommand(
		runCmd,
		enrollCmd,
		statusCmd,
		newRoleConfigCommand(opts, sampleAgentConfig, func(data []byte) error {
			if err := requireConfigRole("config", data, RoleAgent); err != nil {
				return err
			}
			_, err := parseAgentConfig(data)
			return err
		}),
		newRoleServiceCommand(RoleAgent, opts),
		newRoleSampleConfigCommand(sampleAgentConfig),
	)
	return agentCmd
}

func agentRuntimeFromOptions(cmd *cobra.Command, opts *serveOptions) (AgentRuntime, error) {
	path, err := commandConfigPath(opts.configPath)
	if err != nil {
		return AgentRuntime{}, err
	}
	if !flagChanged(cmd, "config") {
		if err := ensureRoleConfig(path, sampleAgentConfig); err != nil {
			return AgentRuntime{}, fmt.Errorf("initialize agent config: %w", err)
		}
	}
	cfg, err := loadAgentConfig(path)
	if err != nil {
		return AgentRuntime{}, fmt.Errorf("load agent config: %w", err)
	}
	return agentRuntime(path, cfg)
}

func runAgentCommand(cmd *cobra.Command, opts *serveOptions) error {
	runtime, err := agentRuntimeFromOptions(cmd, opts)
	if err != nil {
		return err
	}
	token, err := readAgentToken(runtime.CredentialsPath)
	if err != nil {
		return err
	}
	agent, err := newAgent(runtime, token)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("agent %s -> %s", runtime.ID, runtime.ControllerURL)
	log.Printf("workspace %s", runtime.WorkspaceRoot)
	log.Printf("spool %s", runtime.SpoolDir)
	if err := agent.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func readTokenInput(cmd *cobra.Command, tokenFile string) (string, error) {
	if strings.TrimSpace(tokenFile) != "" {
		data, err := os.ReadFile(tokenFile)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(data)), nil
	}
	data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 8<<10))
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", errors.New("no token was provided on stdin")
	}
	return token, nil
}
