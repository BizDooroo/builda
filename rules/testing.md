# Testing Rules

- Run `go test ./...` after code or executable configuration changes.
- Run `git diff --check` before committing or handing off substantial documentation changes.
- Add or update tests for config validation, task API behavior, runner queueing, cancellation, restart recovery, and persistence changes.
- Prefer tests that observe public behavior through `Runner` methods or HTTP handlers.
- For shell execution tests, use short commands with explicit timeouts and avoid network or machine-specific dependencies.
- When changing path handling, include encoded or unusual task IDs where relevant.
