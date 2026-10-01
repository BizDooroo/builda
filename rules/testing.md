# Testing Rules

- Run `go test ./...` and `go test -race ./...` after code or executable configuration changes.
- Run `git diff --check` before committing or handing off substantial documentation changes.
- Rebuild the frontend with a frozen install and confirm the committed `web/dist` matches before committing UI work.
- Cover config and parameter validation, environment naming collisions, workspace path and symlink safety, scheduler fairness and parallelism, cancellation and start-permit races including an offline agent, authentication, CSRF, token scopes, every CRUD and filter endpoint, the agent protocol with log offsets and idempotency, and crash recovery for both roles.
- Prefer tests that observe public behaviour through `Controller` methods, the HTTP handlers, or a live agent, rather than internal state.
- Integration tests run a temporary controller and local agents with synthetic shell scripts. Never depend on a real build, a network service, or an installed daemon.
- Express platform service behaviour as command plans so order, flags, and error handling are testable without a live init system, and also run those plans against a stand-in for the tool on PATH. Asserting the plan's shape alone misses what the runner does with an asynchronous teardown or a refused step.
- For shell execution tests, use short commands with explicit timeouts and avoid machine-specific dependencies.
- Drive the Web UI in a real browser for anything user-visible. Two defects that unit tests could not see — the translation pass clearing a log selection, and a view object posted back to an API that rejects unknown fields — were only found that way.
