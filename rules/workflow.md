# Workflow Rules

- Use `rg` for repository search and `gofmt` for Go formatting.
- Keep commits scoped to one user-visible purpose and stage files explicitly.
- Preserve unrelated user changes in the worktree.
- Keep `.gitignore` current for local agent metadata, logs, binaries, coverage files, temp files, and secret-bearing env files.
- Do not commit generated logs from `log_dir`, local binaries, coverage output, or gitleaks reports.
- Update `README.md` when API shape, operator workflow, or security posture changes.
- Keep `AGENTS.md` short and move durable project guidance into `rules/`.
