# Workflow Rules

- Use `rg` for repository search and `gofmt` for Go formatting.
- Treat files over 300 lines as a warning sign; split any source or test file that exceeds 500 lines, including CSS and scripts under `web/src/`.
- When splitting a CSS file, do not cut inside a selector list; check the first and last rule of each resulting file.
- Keep the Makefile `fmt` target aligned with every tracked Go source file, and keep Makefile targets simple command wrappers.
- Keep commits scoped to one user-visible purpose and stage files explicitly. Each commit's tracked state must build and pass its tests on its own; verify with `git checkout-index` into a scratch directory before committing.
- Preserve unrelated user changes in the worktree.
- Keep `.gitignore` current for runtime state, agent spool directories, credential files, logs, binaries, coverage files, temp files, and secret-bearing env files.
- Update `README.md` when API shape, operator workflow, or security posture changes, and keep `--help` aligned with the YAML schema so it is sufficient for authoring a config.
- Keep `AGENTS.md` short and move durable project guidance into `rules/`.
- Public releases are tag-driven from SemVer tags; the GitHub Actions release workflow owns GoReleaser publishing. Keep GoReleaser output in ignored `dist/`.
- Keep `go.mod` aligned with the public GitHub module path, and do not add a dependency that forces a newer Go toolchain than the module declares.
- Keep Astro client output filenames and internal export names deterministic when CI compares regenerated `web/dist` against committed assets.
