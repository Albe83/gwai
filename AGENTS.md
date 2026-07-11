# AGENTS.md — gwai (Gateway AI)

## Branch & Commit Conventions

- **Branch naming:** `<type>/<ticket>-<short-description>`
  - Types: `feature`, `fix`, `hotfix`, `refactor`, `chore`, `docs`, `test`, `build`, `ci`
  - Example: `feature/ABC-123-customer-api`
- **Commits:** [Conventional Commits](https://www.conventionalcommits.org) — `<type>(<scope>): <description>`
  - Types: `feat`, `fix`, `refactor`, `test`, `docs`, `chore`, `build`, `ci`, `perf`, `revert`
- **Sync with main:** `git fetch origin && git rebase origin/main`
- **Force-push after rebase:** `git push --force-with-lease` (never `--force`)
- **Merge strategy:** squash and merge; delete the branch after merge
- **Releases:** semver tags from `main` (`vMAJOR.MINOR.PATCH`)

## No Tooling Yet

This repo currently has no source code, package manager, build system, or CI pipeline. Treat all tooling choices as open until the stack is decided.

## Contribution Policy

- `main` is protected; all changes go through a PR with passing CI and review
- Prefer small, reviewable PRs over large monolithic changes
- Every change must include tests proportional to its risk
- New dependencies must be justified (license, maintenance, vulnerabilities, size)
- AI-assisted code must be understood by the author and passes the same quality bar
