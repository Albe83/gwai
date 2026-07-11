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

## Tooling and Architecture

- Go 1.26 monorepo; runtime code currently uses only the standard library
- Three service binaries under `cmd/`: control plane, OpenAI gateway, and
  Anthropic adapter
- Dapr HTTP service invocation, state, and secret-store building blocks
- Helm chart under `deploy/helm/gwai` for k3s/Kubernetes
- Valkey 9.1 is the local transactional state backend through Dapr `state.redis`
- The versioned provider-neutral contract lives under `api/ir`

Run the required local gate before proposing a change:

```bash
make check
```

Useful targets:

```bash
make build
make local-deploy
make e2e-k3s
```

Preserve the adapter boundary: client protocols translate to/from the IR and
provider adapters translate to/from the same IR. Do not add direct
client-provider converters or place provider credentials in IR payloads.

## Contribution Policy

- `main` is protected; all changes go through a PR with passing CI and review
- Prefer small, reviewable PRs over large monolithic changes
- Every change must include tests proportional to its risk
- New dependencies must be justified (license, maintenance, vulnerabilities, size)
- AI-assisted code must be understood by the author and passes the same quality bar
