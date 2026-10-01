package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// importReport summarizes what an import did or would do.
type importReport struct {
	Imported    int
	Skipped     int
	Existing    int
	Diagnostics []string
}

// errHistoryCapTooSmall stops an import that the controller's terminal history
// cap would silently truncate.
var errHistoryCapTooSmall = errors.New("controller history cap is too small for this import")

// importBundle writes legacy history into a controller state store. It never
// enqueues work: only terminal legacy runs are imported, and each imported
// execution keeps its original machine and run ID so repeating the import is
// idempotent. Log files are copied under the new execution ID; a missing log
// is reported explicitly instead of being inferred from any path in the
// bundle.
func importBundle(controller *Controller, bundle *MigrationBundle, mapping *MigrationMapping, bundleDir string, apply bool) (importReport, error) {
	report := importReport{Diagnostics: []string{}}
	cfg := controller.Config()
	logDir := controller.Runtime().LogDir

	runs := append([]LegacyRun(nil), bundle.Runs...)
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].RequestedAt.Before(runs[j].RequestedAt) })

	// Imported executions are terminal, so they compete with the controller's
	// own history for the terminal cap. Appending past the cap would prune the
	// oldest entries and their logs, which would both lose history and make a
	// repeated import non-idempotent, so refuse with an actionable number.
	pending := make([]*Execution, 0, len(runs))
	logSources := map[string]string{}

	for _, run := range runs {
		if !isTerminal(run.Status) {
			report.Skipped++
			report.Diagnostics = append(report.Diagnostics, fmt.Sprintf("skip %s: status %s is not terminal, so it is never imported and never queued", run.ID, run.Status))
			continue
		}
		if _, exists := controller.store.FindByOrigin(bundle.Machine, run.ID); exists {
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
		execution, err := buildImportedExecution(cfg, job, bundle, mapping, task, run)
		if err != nil {
			report.Skipped++
			report.Diagnostics = append(report.Diagnostics, fmt.Sprintf("skip %s: %v", run.ID, err))
			continue
		}
		note, imported := importedLogNote(bundleDir, run)
		execution.Origin.LogImported = imported
		execution.Origin.LogNote = note
		if note != "" {
			report.Diagnostics = append(report.Diagnostics, fmt.Sprintf("%s: %s", run.ID, note))
		}
		pending = append(pending, execution)
		if imported {
			logSources[execution.ID] = filepath.Join(bundleDir, bundleLogDir, filepath.Base(run.ID)+".log")
		}
		report.Imported++
	}

	if err := checkHistoryHeadroom(controller, len(pending), &report); err != nil {
		return report, err
	}
	if !apply {
		report.Diagnostics = append(report.Diagnostics, fmt.Sprintf("dry run: would import %d executions, skip %d, leave %d already imported", report.Imported, report.Skipped, report.Existing))
		return report, nil
	}

	// One mutation keeps the import atomic: either the whole batch lands or
	// the controller state is untouched.
	if err := controller.store.mutate(func(st *controllerStateData) error {
		for _, execution := range pending {
			for st.find(execution.ID) != nil {
				execution.ID = newExecutionID()
			}
			st.Executions = append(st.Executions, execution.clone())
		}
		return nil
	}); err != nil {
		return report, err
	}
	for id, source := range logSources {
		if err := copyFile(source, executionLogPath(logDir, id)); err != nil {
			report.Diagnostics = append(report.Diagnostics, fmt.Sprintf("%s: copy log failed: %v", id, err))
		}
	}
	return report, nil
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
func buildImportedExecution(cfg ControllerConfig, job JobConfig, bundle *MigrationBundle, mapping *MigrationMapping, task MappingTask, run LegacyRun) (*Execution, error) {
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

	query := parameterValuesToQuery(values)
	resolved, err := resolveParameters(cfg, job, query)
	if err != nil {
		return nil, fmt.Errorf("mapped parameters do not match job %q: %w", job.ID, err)
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
	return execution, nil
}

func findLegacyTask(bundle *MigrationBundle, id string) *TaskConfig {
	for i := range bundle.Tasks {
		if bundle.Tasks[i].ID == id {
			return &bundle.Tasks[i]
		}
	}
	return nil
}

// importedLogNote checks the bundle's own log directory for the run's log. The
// log path recorded in the bundle is never trusted as a filesystem path; only
// the legacy run ID is used to derive it.
func importedLogNote(bundleDir string, run LegacyRun) (string, bool) {
	source := filepath.Join(bundleDir, bundleLogDir, filepath.Base(run.ID)+".log")
	info, err := os.Stat(source)
	if err != nil {
		return "no log file for legacy run " + run.ID + " in the bundle; the imported run keeps its metadata without a log", false
	}
	if !info.Mode().IsRegular() {
		return "bundle log entry for " + run.ID + " is not a regular file and was not imported", false
	}
	return "", true
}
