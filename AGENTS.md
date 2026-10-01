# Repository Instructions

## Project Shape
- Builda is a single Go binary with two operational roles in a flat repository: `builda controller` owns jobs, catalogs, agents, the queue, history, and logs; `builda agent` connects outbound and runs one job at a time. The legacy `builda serve` standalone role is retained for migration only.
- Go sources are split by responsibility: `ctrl*.go` for controller state, scheduling, auth, and HTTP; `api_*.go` for endpoints; `agent*.go` for the agent; `migrate*.go` for migration; `cli*.go` and `service*.go` for commands and user daemons.
- `README.md` is the product and operator overview. `DESIGN.md` is visual design reference for the embedded Web UI. `examples/` holds config files for each role.

## Required Rule Lookup
- Before non-trivial work, open `rules/index.md` and the relevant rule files.
- Keep `AGENTS.md` short; put reusable lessons and project-specific constraints in `rules/`.
- After each completed task, update the relevant rule file when the work adds a durable lesson.

## Essential Commands
- `go test ./...` and `go test -race ./...`
- `gofmt -w *.go`
- `pnpm --dir web install --frozen-lockfile && pnpm --dir web build`
- `git diff --check`
- `gitleaks detect --source . --no-banner --redact --verbose`

## Non-Negotiables
- Treat job scripts and the agent shell header as privileged shell execution; follow `rules/security.md`.
- Preserve the single central queue, the fail-closed persistence path, and the execution lifecycle; follow `rules/architecture.md`.
- Never let an agent re-execute an incomplete run or kill a process it cannot prove it owns; follow `rules/architecture.md`.
- Keep tests covering scheduling, cancellation races, auth, the agent protocol, and recovery; follow `rules/testing.md`.
- Keep runtime state, spool directories, credentials, logs, binaries, and coverage output out of Git; follow `rules/workflow.md`.
- When asked to deploy, commit, push, patch-tag, and push the tag; follow `rules/deployment.md`.
