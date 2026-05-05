# Workflow Rules

- Use `rg` for repository search and `gofmt` for Go formatting.
- Treat files over 300 lines as a warning sign and avoid growing them further without a reason; immediately split any source or test file that exceeds 500 lines.
- Keep the Makefile `fmt` target aligned with every tracked Go source file.
- Keep Makefile targets simple command wrappers without silent `@` prefixes, echo banners, or help text.
- Keep commits scoped to one user-visible purpose and stage files explicitly.
- Preserve unrelated user changes in the worktree.
- Keep `.gitignore` current for local agent metadata, logs, binaries, coverage files, temp files, and secret-bearing env files.
- Do not commit generated logs from `log_dir`, local binaries, coverage output, or gitleaks reports.
- Update `README.md` when API shape, operator workflow, or security posture changes.
- Keep `--help` config guidance aligned with the YAML schema so it remains sufficient for authoring `config.yaml`.
- Keep `AGENTS.md` short and move durable project guidance into `rules/`.
- Public releases are tag-driven from SemVer tags like `v0.1.0`; the GitHub Actions release workflow owns GoReleaser publishing.
- Keep GoReleaser output in ignored `dist/`, and do not commit release archives, checksums generated locally, or attestation output.
- Keep `go.mod` aligned with the public GitHub module path so `go install github.com/BizDooroo/builda@latest` works.
- Keep Astro client output filenames deterministic when CI compares regenerated `web/dist` against committed assets.
