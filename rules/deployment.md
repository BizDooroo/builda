# Deployment Rules

- When the user asks to deploy, inspect the worktree, run required checks, commit the intended changes, push the current branch, create the next patch SemVer tag unless told otherwise, and push the tag to upstream.
- Derive the next default release tag from the highest existing `vMAJOR.MINOR.PATCH` tag by incrementing `PATCH`.
- Push to the configured upstream remote for the branch. If no upstream exists, use `origin` and set upstream on push.
- Do not include unrelated, generated, secret-bearing, log, coverage, or local binary files in deployment commits.
- If checks fail, do not create or push a release tag until the failure is fixed or the user explicitly accepts the risk.
