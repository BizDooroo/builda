# Architecture Rules

- Keep Builda as a small single-binary Go server unless the user asks for a larger split.
- `main.go` owns the HTTP server, YAML config loading, runner, templates, and JSON APIs.
- The runner executes one task at a time. New task starts append `QUEUED` runs, and `dispatchLocked` starts the next queued run only when no run is active.
- Persist run state in `log_dir/runs.json`. On restart, convert stale `RUNNING` runs to `ABORTED` and resume queued runs.
- Resolve relative `server.log_dir` paths from the directory containing the active config file, not from the process working directory.
- Preserve each run's task snapshot so later config edits do not rewrite historical run metadata.
- Keep task run APIs tied to configured task IDs. Do not accept arbitrary command strings through the run API.
- Keep run-list filtering as a read-only task ID filter over persisted summaries; it must not alter queue or run state.
- Keep log reads confined to the configured log directory and derive log filenames from run IDs.
- Task API copy controls must work outside Clipboard API secure-context support by keeping a textarea/`execCommand("copy")` fallback.
- Embedded templates are acceptable for this repository size. If UI grows substantially, split templates only with a clear maintenance benefit.
