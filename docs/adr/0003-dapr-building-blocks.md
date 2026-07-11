# ADR 0003: Dapr invocation, state, and secrets

- Status: accepted
- Date: 2026-07-11

## Decision

Use Dapr HTTP service invocation between data-plane/control-plane services,
Dapr transactional state for control-plane persistence, and Dapr Secret Store
for provider credentials. Local Kubernetes uses Valkey through `state.redis`.

## Consequences

Service discovery, mTLS, tracing context, database choice, and secret backend
remain platform concerns. Every service requires a sidecar in Kubernetes. Dapr
body-size, ACL, token, API-allowlist, component-scope, and resiliency settings
are part of the application contract and are therefore versioned in Helm.

