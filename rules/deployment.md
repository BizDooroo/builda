# Deployment Rules

- When the user asks to deploy, inspect the worktree, run required checks, commit the intended changes, push the current branch, create the next patch SemVer tag unless told otherwise, and push the tag to upstream.
- Derive the next default release tag from the highest existing `vMAJOR.MINOR.PATCH` tag by incrementing `PATCH`.
- Push to the configured upstream remote for the branch. If no upstream exists, use `origin` and set upstream on push.
- Do not include unrelated, generated, secret-bearing, log, coverage, or local binary files in deployment commits.
- Before tagging a deployment, inspect GitHub workflow formatting and test steps and keep them aligned with `Makefile` targets and the current file layout.
- If checks fail, do not create or push a release tag until the failure is fixed or the user explicitly accepts the risk.

## User daemons
- Install controller and agent services separately; each role has its own unit name, launchd label, config path, and log files.
- The service target is always the Builda executable itself. Never install a shell wrapper, and validate that the target is an executable regular file outside a temporary directory or the Go build cache, and that its role config parses.
- A launchd job with `RunAtLoad` is already started by `bootstrap`. Never follow bootstrap with `kickstart -k` on install or start; that starts a second instance while the first still holds its socket and produces a restart loop. Reserve `-k` for an explicit restart.
- Run `launchctl enable` before `bootstrap` so a persistent per-user `disabled` override cannot leave the job loaded but never allowed to run.
- Treat "already loaded" and "already gone" launchd responses as success so install, start, and stop stay idempotent.
- launchd user agents live in the GUI domain and need an active desktop login session. Install and start from a terminal on that Mac rather than over SSH.
- Service files pin an absolute binary path. After installing a new binary, reinstall with `--force --binary "$(command -v builda)"` and restart before judging the served Web UI.
- Use `builda <role> service diagnose` to report service file, domain, target, config health, and any persistent override without changing anything.
