# Architecture Rules

- Keep Builda as a small single-binary Go server unless the user asks for a larger split.
- `main.go` owns the HTTP server, YAML config loading, runner, templates, and JSON APIs.
- Cobra command setup belongs in `cli.go`, with service installation details in `service.go`.
- The runner executes one task at a time. New task starts append `QUEUED` runs, and `dispatchLocked` starts the next queued run only when no run is active.
- Persist run state in `log_dir/runs.json`. On restart, convert stale `RUNNING` runs to `ABORTED` and resume queued runs.
- Resolve relative `server.log_dir` paths from the directory containing the active config file, not from the process working directory.
- Preserve each run's task snapshot so later config edits do not rewrite historical run metadata.
- Keep task run APIs tied to configured task IDs. Do not accept arbitrary command strings through the run API.
- Task run inputs must be declared on the task config, validated before queueing, persisted on the run, and passed to commands through `BUILDA_INPUT_*` environment variables.
- `wait` is reserved as a task run API control query parameter. `wait=1` should block until the queued run reaches a terminal state and return the run log with the response.
- Keep run-list filtering as a read-only task ID filter over persisted summaries; it must not alter queue or run state.
- Keep log reads confined to the configured log directory and derive log filenames from run IDs.
- Task API copy controls must work outside Clipboard API secure-context support by keeping a textarea/`execCommand("copy")` fallback.
- Embedded templates are acceptable for this repository size. If UI grows substantially, split templates only with a clear maintenance benefit.
- Keep daemon installation user-scoped. Linux installs should target systemd user units, and macOS installs should target launchd LaunchAgents; do not require root-owned system service files unless explicitly requested.
- Config write paths, including CLI commands and HTTP handlers, must parse and validate YAML before replacing the current config file.
- A running server should reload the active config file after it changes so `builda config set` updates configured tasks without a daemon restart.
