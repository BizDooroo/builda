# Deployment Rules

- When the user asks to deploy, inspect the worktree, run required checks, commit the intended changes, push the current branch, create the next patch SemVer tag unless told otherwise, and push the tag to upstream.
- Derive the next default release tag from the highest existing `vMAJOR.MINOR.PATCH` tag by incrementing `PATCH`.
- Push to the configured upstream remote for the branch. If no upstream exists, use `origin` and set upstream on push.
- Do not include unrelated, generated, secret-bearing, log, coverage, or local binary files in deployment commits.
- Before tagging a deployment, inspect GitHub workflow formatting/test steps and keep them aligned with `Makefile` targets and the current file layout; do not rely only on local commands when CI hard-codes file names.
- User daemon service files pin the exact Builda binary path. After installing a new binary with `go install` or a release archive, reinstall the service with `--force --binary "$(command -v builda)"` or another explicit `--binary` and restart it before judging the served Web UI.
- If checks fail, do not create or push a release tag until the failure is fixed or the user explicitly accepts the risk.
