package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Migration bundle. Export is strictly read-only: it reads the legacy config
// and its runs.json directly and never constructs a Runner, so exporting can
// never start a queued legacy run.
const (
	bundleFileName  = "bundle.json"
	bundleLogDir    = "logs"
	bundleVersion   = 1
	mappingFileName = "mapping.yaml"
)

// MigrationBundle is the self-contained export of one legacy machine.
type MigrationBundle struct {
	Version      int          `json:"version"`
	Machine      string       `json:"machine"`
	ExportedAt   time.Time    `json:"exported_at"`
	ConfigPath   string       `json:"config_path"`
	ScriptHeader string       `json:"script_header"`
	LogDir       string       `json:"log_dir"`
	Tasks        []TaskConfig `json:"tasks"`
	Runs         []LegacyRun  `json:"runs"`
	MissingLogs  []string     `json:"missing_logs,omitempty"`
}

// LegacyRun is one persisted standalone run, read as data.
type LegacyRun struct {
	ID          string            `json:"id"`
	TaskID      string            `json:"task_id"`
	TaskName    string            `json:"task_name"`
	Script      string            `json:"script"`
	Inputs      map[string]string `json:"inputs,omitempty"`
	TimeoutText string            `json:"timeout_text"`
	Status      string            `json:"status"`
	RequestedAt time.Time         `json:"requested_at"`
	StartedAt   time.Time         `json:"started_at"`
	FinishedAt  time.Time         `json:"finished_at"`
	CanceledAt  time.Time         `json:"canceled_at"`
	ExitCode    int               `json:"exit_code"`
	Error       string            `json:"error,omitempty"`
	LogFile     string            `json:"log_file,omitempty"`
}

// readLegacyRunState parses runs.json without any runner, scheduler, or
// dispatch side effect.
func readLegacyRunState(path string) ([]LegacyRun, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var runs []LegacyRun
	if err := json.Unmarshal(data, &runs); err != nil {
		return nil, fmt.Errorf("parse legacy run state %s: %w", path, err)
	}
	kept := make([]LegacyRun, 0, len(runs))
	for _, run := range runs {
		if strings.TrimSpace(run.ID) == "" {
			continue
		}
		kept = append(kept, run)
	}
	sort.SliceStable(kept, func(i, j int) bool {
		return kept[i].RequestedAt.Before(kept[j].RequestedAt)
	})
	return kept, nil
}

// exportLegacyBundle builds the bundle for one machine. Logs are copied by
// legacy run ID; a missing log is recorded as an explicit diagnostic rather
// than silently dropped.
func exportLegacyBundle(machine, configPath, outDir string, apply bool) (*MigrationBundle, []string, error) {
	configPath, err := filepath.Abs(configPath)
	if err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, nil, err
	}
	role, err := detectConfigRole(data)
	if err != nil {
		return nil, nil, err
	}
	if role != RoleStandalone {
		return nil, nil, fmt.Errorf("%s declares role %q; migrate export reads a legacy standalone config", configPath, role)
	}
	cfg, err := parseConfig(data)
	if err != nil {
		return nil, nil, fmt.Errorf("parse legacy config %s: %w", configPath, err)
	}
	logDir := resolveLogDir(configPath, cfg.Server.LogDir)
	runs, err := readLegacyRunState(filepath.Join(logDir, "runs.json"))
	if err != nil {
		return nil, nil, err
	}

	bundle := &MigrationBundle{
		Version:      bundleVersion,
		Machine:      machine,
		ExportedAt:   time.Now().UTC(),
		ConfigPath:   configPath,
		ScriptHeader: cfg.Server.ScriptHeader,
		LogDir:       logDir,
		Tasks:        cfg.Tasks,
	}
	diagnostics := make([]string, 0)
	for _, run := range runs {
		if !isTerminal(run.Status) {
			diagnostics = append(diagnostics, fmt.Sprintf("skip %s: status %s is not terminal; stop the legacy service and let it settle before exporting", run.ID, run.Status))
			continue
		}
		source := filepath.Join(logDir, filepath.Base(run.ID)+".log")
		if _, err := os.Stat(source); err != nil {
			bundle.MissingLogs = append(bundle.MissingLogs, run.ID)
			diagnostics = append(diagnostics, fmt.Sprintf("missing log for %s at %s", run.ID, source))
		} else {
			run.LogFile = filepath.Join(bundleLogDir, filepath.Base(run.ID)+".log")
		}
		bundle.Runs = append(bundle.Runs, run)
	}

	if !apply {
		diagnostics = append(diagnostics, fmt.Sprintf("dry run: would write %s with %d tasks and %d terminal runs", filepath.Join(outDir, bundleFileName), len(bundle.Tasks), len(bundle.Runs)))
		return bundle, diagnostics, nil
	}
	if err := writeMigrationBundle(bundle, logDir, outDir); err != nil {
		return nil, diagnostics, err
	}
	diagnostics = append(diagnostics, fmt.Sprintf("wrote %s with %d tasks and %d terminal runs", filepath.Join(outDir, bundleFileName), len(bundle.Tasks), len(bundle.Runs)))
	return bundle, diagnostics, nil
}

func writeMigrationBundle(bundle *MigrationBundle, logDir, outDir string) error {
	if err := os.MkdirAll(filepath.Join(outDir, bundleLogDir), 0o700); err != nil {
		return err
	}
	for _, run := range bundle.Runs {
		if run.LogFile == "" {
			continue
		}
		source := filepath.Join(logDir, filepath.Base(run.ID)+".log")
		target := filepath.Join(outDir, bundleLogDir, filepath.Base(run.ID)+".log")
		if err := copyFile(source, target); err != nil {
			return err
		}
	}
	encoded, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomicSync(filepath.Join(outDir, bundleFileName), encoded, 0o600)
}

func loadMigrationBundle(dir string) (*MigrationBundle, error) {
	data, err := os.ReadFile(filepath.Join(dir, bundleFileName))
	if err != nil {
		return nil, err
	}
	var bundle MigrationBundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return nil, fmt.Errorf("parse migration bundle: %w", err)
	}
	if bundle.Machine == "" {
		return nil, errors.New("migration bundle has no machine name")
	}
	return &bundle, nil
}

func copyFile(source, target string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return writeFileAtomicSync(target, data, 0o600)
}
