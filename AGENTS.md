# Repository Instructions

## Project Shape
- Builda is a small Go web task runner in a flat repository: `main.go` contains the server, runner, templates, and API handlers; `main_test.go` contains focused unit and handler tests.
- `README.md` is the product/operator overview. `DESIGN.md` is visual design reference for the embedded Web UI.

## Required Rule Lookup
- Before non-trivial work, open `rules/index.md` and the relevant rule files.
- Keep `AGENTS.md` short; put reusable lessons and project-specific constraints in `rules/`.
- After each completed task, update the relevant rule file when the work adds a durable lesson.

## Essential Commands
- `go test ./...`
- `gofmt -w main.go main_test.go`
- `git diff --check`
- `gitleaks detect --source . --no-banner --redact --verbose`

## Non-Negotiables
- Treat task commands as privileged shell execution; follow `rules/security.md`.
- Preserve the single-runner queue and persisted run state behavior; follow `rules/architecture.md`.
- Keep tests focused on runner state transitions, config validation, and API behavior; follow `rules/testing.md`.
- Keep runtime logs, local binaries, coverage output, and secret-bearing env files out of Git; follow `rules/workflow.md`.
