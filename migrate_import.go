package main

import (
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
		if !apply {
			report.Imported++
			continue
		}
		if err := controller.store.mutate(func(st *controllerStateData) error {
			st.Executions = append(st.Executions, execution.clone())
			return nil
		}); err != nil {
			return report, err
		}
		if imported {
			source := filepath.Join(bundleDir, bundleLogDir, filepath.Base(run.ID)+".log")
			target := executionLogPath(logDir, execution.ID)
			if err := copyFile(source, target); err != nil {
				report.Diagnostics = append(report.Diagnostics, fmt.Sprintf("%s: copy log failed: %v", run.ID, err))
			}
		}
		report.Imported++
	}
	if !apply {
		report.Diagnostics = append(report.Diagnostics, fmt.Sprintf("dry run: would import %d executions, skip %d, leave %d already imported", report.Imported, report.Skipped, report.Existing))
	}
	return report, nil
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
