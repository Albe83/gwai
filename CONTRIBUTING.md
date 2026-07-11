# Collaboration and Contribution Guidelines

## 1. Purpose

This document defines the operating rules for contributing to the project in a consistent, traceable, and secure manner.

The project adopts a **trunk-based development model with short-lived feature branches**:

- `main` is the only permanent branch;
- every change is developed on a temporary branch;
- every integration is performed through a Pull Request;
- Continuous Integration automatically validates each contribution;
- `main` must always remain stable and potentially releasable.

---

## 2. Collaboration Principles

Every contribution must follow these principles:

1. **Small and frequent changes**  
   Prefer limited and easily reviewable contributions over large, monolithic changes.

2. **Shared responsibility**  
   The author of a change is responsible for its quality, tests, documentation, and any side effects.

3. **Automation before manual control**  
   Every repeatable check must be performed by the CI pipeline.

4. **Transparency**  
   Decisions, constraints, risks, and technical trade-offs must be documented in the Pull Request or in the project tracking tools.

5. **Keep `main` stable**  
   A change must not be merged if it makes `main` impossible to build, test, or deploy.

---

## 3. Branches

### 3.1 Permanent Branch

The only permanent branch is:

```text
main
```

The `main` branch:

- is protected;
- does not allow direct pushes;
- requires Pull Requests;
- requires all mandatory automated checks to pass;
- represents the most up-to-date and stable state of the product.

### 3.2 Temporary Branches

Every change must be developed on a branch created from the latest version of `main`.

Examples:

```text
feature/ABC-123-customer-api
fix/ABC-456-login-timeout
chore/ABC-789-update-dependencies
docs/ABC-321-contribution-guide
refactor/ABC-654-simplify-auth-service
```

Recommended convention:

```text
<type>/<ticket>-<short-description>
```

Allowed types:

- `feature`: new functionality;
- `fix`: defect correction;
- `hotfix`: urgent production correction;
- `refactor`: internal change without functional impact;
- `chore`: technical maintenance;
- `docs`: documentation;
- `test`: test additions or updates;
- `build`: build, packaging, or dependency changes;
- `ci`: pipeline changes.

### 3.3 Branch Lifetime

Branches must remain short-lived.

Operational target:

- preferred lifetime: a few hours;
- recommended maximum lifetime: one or two working days;
- frequent synchronization with `main`;
- no branch should become a parallel development line.

If a feature requires more time, it must be split into smaller, independently mergeable increments.

Incomplete functionality must be disabled through:

- feature flags;
- configuration;
- permissions;
- non-exposed endpoints;
- components that are not yet activated.

It must not remain isolated for weeks on long-lived branches.

---

## 4. Starting a Change

Before starting:

1. verify that a ticket, issue, or tracked activity exists;
2. clarify the objective, requirements, and acceptance criteria;
3. update the local repository;
4. create a branch from `main`.

Example:

```bash
git switch main
git pull --ff-only
git switch -c feature/ABC-123-customer-api
```

Do not create new branches from obsolete branches or from other feature branches unless explicitly agreed.

---

## 5. Commits

### 5.1 Commit Characteristics

Every commit must be:

- atomic;
- understandable;
- limited to a single intent;
- buildable and testable whenever technically possible;
- free of temporary files, credentials, or local artifacts.

Avoid generic commit messages such as:

```text
fix
update
changes
work in progress
various changes
```

### 5.2 Commit Message Format

The use of **Conventional Commits** is recommended.

Format:

```text
<type>(<scope>): <description>
```

Examples:

```text
feat(api): add customer search endpoint
fix(auth): correct early token expiration
refactor(order): simplify order validation
test(api): add integration tests for customer search
docs: update deployment instructions
```

Main types:

- `feat`;
- `fix`;
- `refactor`;
- `test`;
- `docs`;
- `chore`;
- `build`;
- `ci`;
- `perf`;
- `revert`.

For incompatible changes:

```text
feat(api)!: change customer response format
```

or:

```text
BREAKING CHANGE: customerId replaces id
```

### 5.3 Temporary Commits

`WIP`, `fixup`, or `squash` commits are allowed during local development, but they must be reordered or consolidated before merge when required by repository policy.

---

## 6. Updating a Branch

Before opening or updating a Pull Request, synchronize the branch with `main`.

Recommended method:

```bash
git fetch origin
git rebase origin/main
```

Alternatively, when allowed by the team rules:

```bash
git merge origin/main
```

Do not rebase a shared branch without coordinating with the other contributors.

After a rebase:

```bash
git push --force-with-lease
```

Do not use:

```bash
git push --force
```

---

## 7. Pull Requests

Every change must be integrated through a Pull Request.

### 7.1 Size

A Pull Request must be:

- focused on a single objective;
- small enough to be reviewed in a reasonable amount of time;
- free from unrelated changes;
- accompanied by relevant tests and documentation.

As a reference, a Pull Request should be reviewable in less than 30-45 minutes.

If a change is too large, it must be split into multiple independent contributions.

### 7.2 Title

The title must be clear and descriptive.

Example:

```text
feat(api): add customer search by tax code
```

### 7.3 Description

The description must include at least:

- context;
- problem being solved;
- chosen solution;
- impacts;
- risks;
- test method;
- any incompatible changes;
- references to tickets or issues.

Recommended template:

```markdown
## Context

Description of the problem or requirement.

## Change

Brief description of the implemented solution.

## Impact

- affected components;
- configuration changes;
- migrations;
- dependencies;
- compatibility.

## Tests Performed

- [ ] unit tests
- [ ] integration tests
- [ ] manual tests
- [ ] regression tests

## Risks

Description of known risks and mitigations.

## Ticket

Closes ABC-123
```

### 7.4 Draft Pull Requests

Open a Pull Request as Draft when:

- the work is not yet complete;
- early feedback is needed;
- the solution requires architectural validation;
- the work in progress should be made visible.

A Draft Pull Request must not be considered ready for merge.

---

## 8. Code Review

### 8.1 Review Objectives

The review must verify:

- functional correctness;
- readability;
- simplicity;
- architectural consistency;
- test quality;
- security;
- error handling;
- observability;
- compatibility;
- maintainability;
- documentation.

The review must not focus only on code style, which should be checked automatically.

### 8.2 Reviewer Rules

The reviewer must:

- understand the problem before evaluating the solution;
- distinguish between defects, mandatory changes, and suggestions;
- explain requested changes;
- avoid personal preferences that are not supported by standards;
- clearly identify risks and alternatives;
- approve only changes considered suitable for production.

Recommended comment prefixes:

```text
blocking:
suggestion:
question:
nit:
security:
performance:
```

Example:

```text
blocking: this query may allow unauthorized cross-tenant access.
```

### 8.3 Author Rules

The Pull Request author must:

- respond to comments;
- apply or discuss requested changes;
- not unilaterally resolve conversations that are still open;
- request a new review after significant changes;
- ensure that the pipeline is green again.

---

## 9. Continuous Integration

The CI pipeline must run on every Pull Request.

Recommended minimum checks:

- formatting;
- linting;
- compilation;
- unit tests;
- integration tests;
- dependency checks;
- vulnerability scanning;
- secret scanning;
- static analysis;
- manifest and configuration validation;
- artifact build;
- SBOM generation, where applicable.

A Pull Request must not be merged when:

- a mandatory check has failed;
- the pipeline has been skipped without justification;
- blocking vulnerabilities are present;
- mandatory conversations remain unresolved;
- the branch is not up to date with `main`, when required.

Disabling a check just to obtain a merge is not allowed.  
The underlying issue must be resolved or formally accepted through the project's exception process.

---

## 10. Testing

Every change must include tests proportionate to its risk.

### 10.1 Minimum Requirements

- new application logic: unit tests;
- component integration: integration tests;
- API or contract changes: contract tests;
- bug fixes: a test that reproduces the defect;
- critical changes: regression tests;
- UI changes: functional or end-to-end tests where applicable;
- infrastructure changes: automated manifest validation.

### 10.2 Test Quality

Tests must:

- be deterministic;
- be independent;
- be understandable;
- verify behavior rather than internal implementation;
- avoid unnecessary dependencies on external systems;
- produce useful failure messages.

Increasing code coverage alone is not sufficient: tests must protect relevant behavior.

---

## 11. Security

The repository must never contain:

- passwords;
- tokens;
- API keys;
- private certificates;
- connection strings containing credentials;
- real personal data;
- database dumps;
- local configuration files containing secrets;
- unnecessary confidential information.

Secrets must be managed through approved systems, such as:

- secret managers;
- protected pipeline variables;
- Kubernetes Secrets;
- corporate vaults;
- workload identity mechanisms.

If a secret is accidentally committed:

1. stop work immediately;
2. revoke or rotate the secret;
3. inform the relevant owners;
4. remove the data from Git history;
5. verify whether unauthorized access occurred.

Deleting it in a later commit is not sufficient.

---

## 12. Dependencies

The introduction of a new dependency must be justified.

Before adding one, verify:

- license;
- maintenance status;
- update frequency;
- known vulnerabilities;
- size and impact;
- compatibility;
- availability of alternatives already in use;
- lock-in risk;
- maintainer reliability.

Dependencies must be versioned and, where possible, pinned through lock files.

Automated dependency updates must still pass through the same review and CI rules.

---

## 13. Documentation

Every contribution must update the documentation when it changes:

- user behavior;
- APIs;
- configuration;
- deployment;
- requirements;
- dependencies;
- operating procedures;
- troubleshooting;
- runbooks;
- data models;
- architecture.

Code is not considered self-documenting when decisions, constraints, or behavior are not obvious.

Relevant architectural decisions must be recorded through Architecture Decision Records when required.

---

## 14. Database Changes

Every schema change must:

- be versioned;
- be automatable;
- remain backward compatible where possible;
- include a rollback or recovery strategy;
- avoid prolonged locks;
- be tested on representative data;
- support progressive deployment.

For incompatible changes, prefer the following model:

1. add;
2. migrate;
3. transition;
4. remove.

Do not introduce incompatible database and application changes at the same time if doing so prevents rollback or rolling updates.

---

## 15. Compatibility and Breaking Changes

An incompatible change must be explicitly declared in the Pull Request.

The Pull Request must identify:

- affected components;
- affected users or consumers;
- migration procedure;
- rollout strategy;
- rollback capability;
- compatibility period;
- planned removal date of the previous behavior.

Breaking changes should be avoided when a reasonable backward-compatible solution exists.

---

## 16. Merge

A merge is allowed only when:

- the Pull Request is approved;
- all mandatory checks have passed;
- all blocking comments are resolved;
- tests are adequate;
- documentation is updated;
- there are no conflicts;
- the branch is up to date;
- the change is ready for release or protected by a feature flag.

Recommended strategy:

```text
Squash and merge
```

This strategy keeps the `main` history compact and readable.

The squash commit message must follow the defined commit format.

After the merge, the branch must be deleted.

---

## 17. Releases

Releases must be generated from `main`.

Principles:

- no manual rebuild between environments;
- the same artifact is promoted through development, test, staging, and production;
- every release is identified by a tag;
- artifacts are immutable;
- the pipeline records the version, commit, and build metadata.

Example:

```text
v1.4.0
```

Semantic Versioning is recommended:

```text
MAJOR.MINOR.PATCH
```

- `MAJOR`: incompatible changes;
- `MINOR`: backward-compatible functionality;
- `PATCH`: backward-compatible fixes.

Release branches are not part of the default workflow.

They may be created only when required to:

- maintain multiple supported versions;
- stabilize a release separately;
- distribute software through rigid release cycles;
- manage an LTS version.

---

## 18. Hotfixes

A hotfix must branch from `main`, unless separate maintenance lines exist.

Example:

```bash
git switch main
git pull --ff-only
git switch -c hotfix/ABC-999-fix-authentication
```

Hotfixes still require:

- a Pull Request;
- review;
- tests;
- pipeline validation;
- tracking;
- incident documentation.

Urgency may shorten the process, but it does not remove essential controls.

---

## 19. Feature Flags

Feature flags must be used to separate:

- code integration;
- functional activation;
- user release.

Every feature flag must have:

- an owner;
- a default value;
- a review date;
- a removal strategy;
- a description of its behavior;
- a rollback plan.

Temporary feature flags must be removed once they are no longer needed.

---

## 20. Contributions Generated with AI Tools

AI tools may be used to support development, but they do not transfer responsibility for the contribution.

Anyone proposing code generated or modified with AI must:

- fully understand how it works;
- verify correctness and security;
- check licensing and provenance;
- remove inappropriate references or sensitive data;
- add adequate tests;
- verify that no non-existent APIs were introduced;
- avoid sharing unauthorized confidential code or data with external services.

AI-generated code is subject to the same controls as manually written code.

A change must not be approved if the author cannot explain it.

---

## 21. Communication

Technical discussions must take place in shared project tools:

- issues;
- tickets;
- Pull Requests;
- ADRs;
- documentation;
- project channels.

Decisions made verbally or in private chats must be documented in a traceable location when they affect:

- architecture;
- requirements;
- security;
- priorities;
- product behavior;
- operating procedures.

---

## 22. Definition of Done

A change is complete when:

- [ ] acceptance criteria are met;
- [ ] the code has been reviewed;
- [ ] the pipeline is green;
- [ ] the necessary tests are present;
- [ ] the documentation is updated;
- [ ] no secrets or sensitive data are present;
- [ ] observability and logging are adequate;
- [ ] any migration is documented;
- [ ] rollback is possible or formally managed;
- [ ] the change is merged into `main`;
- [ ] the branch has been deleted;
- [ ] the ticket has been updated or closed.

---

## 23. Exceptions

Any exception to these rules must be:

- justified;
- tracked;
- time-limited;
- approved by the technical owner;
- accompanied by a risk assessment.

Exceptions must not become the normal operating model.

---

## 24. Operational Summary

The standard workflow is:

```text
Ticket
  ↓
Short-lived branch from main
  ↓
Local development and testing
  ↓
Pull Request
  ↓
Automated CI
  ↓
Code review
  ↓
Squash and merge
  ↓
Main
  ↓
Build and promotion of the same artifact
```

The fundamental rule is simple:

> Integrate small, tested, and reviewed changes into `main` as early as possible, while keeping the product stable and releasable at all times.
