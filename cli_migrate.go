package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// Migration CLI. Every subcommand is a dry run until --apply is passed, and
// nothing ever touches the legacy installation: export only reads.
func newMigrateCommand() *cobra.Command {
	migrateCmd := &cobra.Command{
		Use:          "migrate",
		Short:        "Move a legacy standalone installation to the controller and agent roles",
		Long:         strings.TrimSpace(migrateHelp),
		SilenceUsage: true,
	}
	migrateCmd.AddCommand(
		newMigrateExportCommand(),
		newMigratePlanCommand(),
		newMigrateConfigCommand(),
		newMigrateImportCommand(),
	)
	return migrateCmd
}

func newMigrateExportCommand() *cobra.Command {
	var configPath, machine, outDir string
	var apply bool
	cmd := &cobra.Command{
		Use:          "export",
		Short:        "Read a legacy config and run history into a migration bundle",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(machine) == "" {
				return fmt.Errorf("--machine is required; it identifies the source host in imported history")
			}
			if strings.TrimSpace(configPath) == "" {
				configPath = defaultConfigPath()
			}
			if strings.TrimSpace(outDir) == "" {
				return fmt.Errorf("--out-dir is required")
			}
			bundle, diagnostics, err := exportLegacyBundle(machine, configPath, outDir, apply)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "machine %s\ntasks %d\nterminal runs %d\n", bundle.Machine, len(bundle.Tasks), len(bundle.Runs))
			printDiagnostics(cmd, diagnostics)
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "legacy standalone config file to read")
	cmd.Flags().StringVar(&machine, "machine", "", "name identifying this source host")
	cmd.Flags().StringVar(&outDir, "out-dir", "", "directory to write bundle.json and logs into")
	cmd.Flags().BoolVar(&apply, "apply", false, "write the bundle instead of only reporting what it would contain")
	return cmd
}

func newMigratePlanCommand() *cobra.Command {
	var bundleDir, out, agentID, workspaceRoot string
	var apply bool
	cmd := &cobra.Command{
		Use:          "plan",
		Short:        "Draft a reviewable mapping from legacy tasks to jobs and catalog entries",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			bundle, err := loadMigrationBundle(bundleDir)
			if err != nil {
				return err
			}
			if strings.TrimSpace(agentID) == "" {
				agentID = bundle.Machine + "-agent"
			}
			mapping, diagnostics := planMigration(bundle, agentID, workspaceRoot)
			data, err := marshalMapping(mapping)
			if err != nil {
				return err
			}
			if !apply {
				fmt.Fprint(cmd.OutOrStdout(), string(data))
				printDiagnostics(cmd, append(diagnostics, "dry run: pass --apply with --out to write the mapping"))
				return nil
			}
			if strings.TrimSpace(out) == "" {
				out = filepath.Join(bundleDir, mappingFileName)
			}
			if err := writeFileAtomicSync(out, data, 0o600); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", out)
			printDiagnostics(cmd, diagnostics)
			return nil
		},
	}
	cmd.Flags().StringVar(&bundleDir, "bundle", "", "migration bundle directory")
	cmd.Flags().StringVar(&out, "out", "", "mapping file to write with --apply")
	cmd.Flags().StringVar(&agentID, "agent-id", "", "agent id for this machine")
	cmd.Flags().StringVar(&workspaceRoot, "workspace-root", "", "agent workspace root; inferred from legacy scripts when omitted")
	cmd.Flags().BoolVar(&apply, "apply", false, "write the mapping file")
	return cmd
}

func newMigrateConfigCommand() *cobra.Command {
	var bundleDirs, mappingFiles []string
	var outDir string
	var apply bool
	cmd := &cobra.Command{
		Use:          "config",
		Short:        "Generate controller and agent config files from reviewed mappings",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			bundles, mappings, err := loadBundlesAndMappings(bundleDirs, mappingFiles)
			if err != nil {
				return err
			}
			cfg, agents, err := buildRoleConfigs(bundles, mappings)
			if err != nil {
				return err
			}
			if !apply {
				data, err := marshalControllerConfig(cfg)
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "# %s\n%s\n", controllerConfigName, string(data))
				for _, machine := range sortedKeys(agents) {
					fmt.Fprintf(cmd.OutOrStdout(), "# agent-%s.yaml\n", machine)
					encoded, err := marshalAgentConfig(agents[machine])
					if err != nil {
						return err
					}
					fmt.Fprintf(cmd.OutOrStdout(), "%s\n", string(encoded))
				}
				printDiagnostics(cmd, []string{"dry run: pass --apply with --out-dir to write these files"})
				return nil
			}
			if strings.TrimSpace(outDir) == "" {
				return fmt.Errorf("--out-dir is required with --apply")
			}
			written, err := writeRoleConfigs(outDir, cfg, agents)
			if err != nil {
				return err
			}
			for _, path := range written {
				fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", path)
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&bundleDirs, "bundle", nil, "migration bundle directory; repeat for several machines")
	cmd.Flags().StringArrayVar(&mappingFiles, "map", nil, "mapping file matching each --bundle in order")
	cmd.Flags().StringVar(&outDir, "out-dir", "", "directory to write the generated role configs into")
	cmd.Flags().BoolVar(&apply, "apply", false, "write the generated config files")
	return cmd
}

func newMigrateImportCommand() *cobra.Command {
	var bundleDir, mappingFile, configPath string
	var apply bool
	cmd := &cobra.Command{
		Use:          "import",
		Short:        "Import legacy run history into a controller",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			bundle, err := loadMigrationBundle(bundleDir)
			if err != nil {
				return err
			}
			mapping, err := readMappingFile(mappingFile)
			if err != nil {
				return err
			}
			if strings.TrimSpace(configPath) == "" {
				configPath = roleConfigPath(RoleController)
			}
			absConfig, err := filepath.Abs(configPath)
			if err != nil {
				return err
			}
			cfg, err := loadControllerConfig(absConfig)
			if err != nil {
				return err
			}
			open := newController
			if !apply {
				// A dry run must not create state or log directories on the
				// target controller.
				open = func(path string, cfg ControllerConfig, _ []string) (*Controller, error) {
					return newReadOnlyController(path, cfg)
				}
			}
			controller, err := open(absConfig, cfg, nil)
			if err != nil {
				return err
			}
			defer controller.Close()
			report, err := importBundle(controller, bundle, mapping, bundleDir, apply)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "imported %d\nskipped %d\nalready imported %d\n", report.Imported, report.Skipped, report.Existing)
			printDiagnostics(cmd, report.Diagnostics)
			return nil
		},
	}
	cmd.Flags().StringVar(&bundleDir, "bundle", "", "migration bundle directory")
	cmd.Flags().StringVar(&mappingFile, "map", "", "reviewed mapping file")
	cmd.Flags().StringVar(&configPath, "config", "", "controller config file to import into")
	cmd.Flags().BoolVar(&apply, "apply", false, "write the imported history instead of only reporting it")
	return cmd
}

func loadBundlesAndMappings(bundleDirs, mappingFiles []string) ([]*MigrationBundle, []*MigrationMapping, error) {
	if len(bundleDirs) == 0 {
		return nil, nil, fmt.Errorf("at least one --bundle is required")
	}
	if len(mappingFiles) != len(bundleDirs) {
		return nil, nil, fmt.Errorf("provide one --map for each --bundle, in the same order")
	}
	bundles := make([]*MigrationBundle, 0, len(bundleDirs))
	mappings := make([]*MigrationMapping, 0, len(mappingFiles))
	for index, dir := range bundleDirs {
		bundle, err := loadMigrationBundle(dir)
		if err != nil {
			return nil, nil, err
		}
		mapping, err := readMappingFile(mappingFiles[index])
		if err != nil {
			return nil, nil, err
		}
		bundles = append(bundles, bundle)
		mappings = append(mappings, mapping)
	}
	return bundles, mappings, nil
}

func readMappingFile(path string) (*MigrationMapping, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("--map is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseMapping(data)
}

func printDiagnostics(cmd *cobra.Command, diagnostics []string) {
	for _, line := range diagnostics {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", line)
	}
}
