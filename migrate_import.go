package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// importReport summarizes what an import did or would do.
type importReport struct {
	Imported    int
	Skipped     int
	Existing    int
	Diagnostics []string
}

var (
	// errHistoryCapTooSmall stops an import the terminal history cap cannot
	// hold. Letting it through would prune the controller's own oldest runs
	// and their logs, which is both data loss and a source of re-imports.
	errHistoryCapTooSmall = errors.New("controller history cap is too small for this import")
	// errImportLogCopy reports a log that could not be staged. Nothing is
	// committed when it happens, so the import can simply be retried.
	errImportLogCopy = errors.New("import log copy failed")
)

// plannedImport is one legacy run resolved against the current controller
// configuration, together with the bundle log that belongs to it.
type plannedImport struct {
	Execution *Execution
	LogSource string
}

// importBundle writes legacy history into a controller. It never enqueues
// work: only terminal legacy runs are imported. Each import is recorded in a
// durable ledger keyed by machine and legacy run ID, so repeating it is a
// no-op even after the execution it produced has been pruned or deleted.
//
// The import is all-or-nothing. Every log is copied under its new execution
// ID and flushed before any state is committed; if a copy fails, or the state
// cannot be persisted, the files this import created are removed and nothing
// is recorded, so a retry starts from a clean slate.
func importBundle(controller *Controller, bundle *MigrationBundle, mapping *MigrationMapping, bundleDir string, apply bool) (importReport, error) {
	report := importReport{Diagnostics: []string{}}
	planned, err := planImports(controller, bundle, mapping, bundleDir, &report)
	if err != nil {
		return report, err
	}

	if len(planned) == 0 {
		// Nothing to do, so the snapshot is left completely alone: no
		// mutation means no prune and no rewrite.
		if !apply {
			report.Diagnostics = append(report.Diagnostics, importDryRunSummary(report))
		}
		return report, nil
	}
	if err := checkHistoryHeadroom(controller, len(planned), &report); err != nil {
		return report, err
	}
	if !apply {
		report.Diagnostics = append(report.Diagnostics, importDryRunSummary(report))
		return report, nil
	}
	return applyImports(controller, bundle, planned, report)
}

// planImports resolves every legacy run against the current configuration.
func planImports(controller *Controller, bundle *MigrationBundle, mapping *MigrationMapping, bundleDir string, report *importReport) ([]plannedImport, error) {
	cfg := controller.Config()
	runs := append([]LegacyRun(nil), bundle.Runs...)
	sort.SliceStable(runs, func(i, j int) bool {
		if runs[i].RequestedAt.Equal(runs[j].RequestedAt) {
			return runs[i].ID < runs[j].ID
		}
		return runs[i].RequestedAt.Before(runs[j].RequestedAt)
	})

	planned := make([]plannedImport, 0, len(runs))
	taken := map[string]bool{}
	for _, execution := range controller.store.Executions() {
		taken[execution.ID] = true
	}
	seen := map[originKey]bool{}

	for _, run := range runs {
		key := originKey{Machine: bundle.Machine, LegacyRunID: run.ID}
		if seen[key] {
			// A bundle that lists one legacy run twice is skipped
			// deterministically: the first occurrence in the sorted order wins.
			report.Skipped++
			report.Diagnostics = append(report.Diagnostics, fmt.Sprintf("skip duplicate entry for legacy run %s in this bundle", run.ID))
			continue
		}
		seen[key] = true

		if !isTerminal(run.Status) {
			report.Skipped++
			report.Diagnostics = append(report.Diagnostics, fmt.Sprintf("skip %s: status %s is not terminal, so it is never imported and never queued", run.ID, run.Status))
			continue
		}
		if controller.store.HasImportedOrigin(bundle.Machine, run.ID) {
			report.Existing++
			continue
		}
		task, ok := mapping.Tasks[run.TaskID]
		if !ok {
			report.Skipped++
			report.Diagnostics = append(report.Diagnostics, fmt.Sprintf("skip %s: legacy task %q is not in the mapping", run.ID, run.TaskID))
			continue
		}
		job, ok := findJob(cfg, task.Job)
		if !ok {
			report.Skipped++
			report.Diagnostics = append(report.Diagnostics, fmt.Sprintf("skip %s: mapped job %q does not exist on this controller", run.ID, task.Job))
			continue
		}
		execution, retired, err := buildImportedExecution(cfg, job, bundle, mapping, task, run)
		if err != nil {
			report.Skipped++
			report.Diagnostics = append(report.Diagnostics, fmt.Sprintf("skip %s: %v", run.ID, err))
			continue
		}
		if len(retired) > 0 {
			report.Diagnostics = append(report.Diagnostics, fmt.Sprintf(
				"%s: imported with %s, which job %q no longer offers; the run is kept as history and cannot be re-run as it is",
				run.ID, strings.Join(retired, ", "), job.ID))
		}
		for taken[execution.ID] {
			execution.ID = newExecutionID()
		}
		taken[execution.ID] = true

		source, note := importedLogSource(bundleDir, run)
		execution.Origin.LogImported = source != ""
		execution.Origin.LogNote = note
		if note != "" {
			report.Diagnostics = append(report.Diagnostics, fmt.Sprintf("%s: %s", run.ID, note))
		}
		planned = append(planned, plannedImport{Execution: execution, LogSource: source})
		report.Imported++
	}
	return planned, nil
}

// applyImports copies every log and then commits the batch. Anything this
// import created is removed when either step fails.
func applyImports(controller *Controller, bundle *MigrationBundle, planned []plannedImport, report importReport) (importReport, error) {
	logDir := controller.Runtime().LogDir
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return report, err
	}

	created := make([]string, 0, len(planned))
	cleanup := func() {
		for _, path := range created {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				report.Diagnostics = append(report.Diagnostics, fmt.Sprintf("could not remove staged log %s: %v", path, err))
			}
		}
	}

	for _, item := range planned {
		if item.LogSource == "" {
			continue
		}
		target := executionLogPath(logDir, item.Execution.ID)
		// copyFile writes through the durable path, so the bytes are flushed
		// before any state references them. The source is only ever read.
		if err := copyFile(item.LogSource, target); err != nil {
			cleanup()
			return report, fmt.Errorf("%w: %s: %v", errImportLogCopy, item.Execution.Origin.LegacyRunID, err)
		}
		created = append(created, target)
	}

	now := time.Now().UTC()
	maxHistory := normalizeMaxHistory(controller.Runtime().MaxHistory)
	err := controller.store.mutate(func(st *controllerStateData) error {
		for _, item := range planned {
			if st.find(item.Execution.ID) != nil {
				return fmt.Errorf("execution id %s is already in use", item.Execution.ID)
			}
			key := originKey{Machine: bundle.Machine, LegacyRunID: item.Execution.Origin.LegacyRunID}
			if st.hasOrigin(key) {
				return fmt.Errorf("legacy run %s was imported concurrently", key.LegacyRunID)
			}
			st.Executions = append(st.Executions, item.Execution.clone())
			st.ImportedOrigins = append(st.ImportedOrigins, ImportedOrigin{
				Machine:     key.Machine,
				LegacyRunID: key.LegacyRunID,
				ExecutionID: item.Execution.ID,
				ImportedAt:  now,
			})
		}
		// Refuse inside the transaction too, so a run that finished since the
		// headroom check cannot make the commit prune live history.
		terminal := 0
		for _, execution := range st.Executions {
			if execution != nil && isExecutionTerminal(execution.Status) {
				terminal++
			}
		}
		if terminal > maxHistory {
			return fmt.Errorf("%w: the import would leave %d terminal executions, above server.max_history=%d", errHistoryCapTooSmall, terminal, maxHistory)
		}
		return nil
	})
	if err != nil {
		cleanup()
		return report, err
	}
	return report, nil
}

func importDryRunSummary(report importReport) string {
	return fmt.Sprintf("dry run: would import %d executions, skip %d, leave %d already imported",
		report.Imported, report.Skipped, report.Existing)
}

// checkHistoryHeadroom refuses an import the terminal history cap cannot hold.
func checkHistoryHeadroom(controller *Controller, incoming int, report *importReport) error {
	if incoming == 0 {
		return nil
	}
	maxHistory := normalizeMaxHistory(controller.Runtime().MaxHistory)
	terminal := 0
	for _, execution := range controller.store.Executions() {
		if isExecutionTerminal(execution.Status) {
			terminal++
		}
	}
	if terminal+incoming <= maxHistory {
		return nil
	}
	message := fmt.Sprintf(
		"importing %d executions on top of %d terminal executions would exceed server.max_history=%d; raise it to at least %d and retry",
		incoming, terminal, maxHistory, terminal+incoming)
	report.Diagnostics = append(report.Diagnostics, message)
	return fmt.Errorf("%w: %s", errHistoryCapTooSmall, message)
}

// buildImportedExecution maps one legacy run onto the new job and parameter
// shape so job and project filters work across migrated history.
func buildImportedExecution(cfg ControllerConfig, job JobConfig, bundle *MigrationBundle, mapping *MigrationMapping, task MappingTask, run LegacyRun) (*Execution, []string, error) {
	values := map[string]string{}
	for name, value := range task.Parameters {
		values[name] = value
	}
	if task.Project != "" {
		values[mapping.ProjectParam] = task.Project
	}
	for legacyID, value := range run.Inputs {
		target, ok := task.InputMap[legacyID]
		if !ok {
			continue
		}
		if mapped, ok := task.InputValues[value]; ok {
			value = mapped
		}
		values[target] = value
	}

	resolved, retired, err := resolveHistoricalParameters(cfg, job, values)
	if err != nil {
		return nil, nil, fmt.Errorf("mapped parameters do not match job %q: %w", job.ID, err)
	}

	legacyTask := findLegacyTask(bundle, run.TaskID)
	execution := &Execution{
		ID:              newExecutionID(),
		JobID:           job.ID,
		JobName:         job.Name,
		Labels:          append([]string(nil), job.Labels...),
		Script:          job.Script,
		TimeoutText:     job.Timeout,
		WorkdirPath:     resolved.WorkdirPath,
		Parameters:      resolved.Values,
		ParameterEnv:    resolved.Env,
		SelectedOptions: resolved.Options,
		JobSnapshot:     cloneJobConfig(job),
		Status:          run.Status,
		AgentID:         mapping.Agent.ID,
		RequestedBy:     "migration:" + bundle.Machine,
		ExitCode:        run.ExitCode,
		Error:           run.Error,
		LogComplete:     true,
		RequestedAt:     run.RequestedAt,
		StartedAt:       run.StartedAt,
		FinishedAt:      run.FinishedAt,
		CanceledAt:      run.CanceledAt,
		Origin: &ExecutionOrigin{
			Machine:     bundle.Machine,
			LegacyRunID: run.ID,
			LegacyTask:  run.TaskID,
			TaskName:    run.TaskName,
			TimeoutText: run.TimeoutText,
			Script:      run.Script,
			ScriptHdr:   bundle.ScriptHeader,
			Inputs:      cloneStringMap(run.Inputs),
			ImportedAt:  time.Now().UTC(),
		},
	}
	if legacyTask != nil && execution.Origin.TaskName == "" {
		execution.Origin.TaskName = legacyTask.Name
	}
	return execution, retired, nil
}

func findLegacyTask(bundle *MigrationBundle, id string) *TaskConfig {
	for i := range bundle.Tasks {
		if bundle.Tasks[i].ID == id {
			return &bundle.Tasks[i]
		}
	}
	return nil
}

// importedLogSource locates a run's log inside the bundle. The path recorded
// in the bundle is never trusted; only the legacy run ID is used to derive it.
func importedLogSource(bundleDir string, run LegacyRun) (string, string) {
	source := filepath.Join(bundleDir, bundleLogDir, filepath.Base(run.ID)+".log")
	info, err := os.Stat(source)
	if err != nil {
		return "", "no log file for legacy run " + run.ID + " in the bundle; the imported run keeps its metadata without a log"
	}
	if !info.Mode().IsRegular() {
		return "", "bundle log entry for " + run.ID + " is not a regular file and was not imported"
	}
	return source, ""
}
