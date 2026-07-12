# ADR 0004: Transactional control-plane indexes

- Status: accepted
- Date: 2026-07-11
- Updated: 2026-07-12

## Decision

Persist resources independently and maintain collection and unique lookup
indexes in the same Dapr state transaction. Use ETags for updates. Hold a local
mutex around compound mutations and deploy one replica of each control-plane
writer.

ADR 0007 divides the registry into three state domains. The same rule applies
inside each domain. Virtual-key mutations additionally update the per-user index
and touch the owner-subject ETag in their transaction; no transaction spans
state components.

## Consequences

Lookups are efficient and the storage adapter remains portable. Lists perform
one read per item, which is acceptable for the initial control-plane scale.
Horizontal writes require a distributed lock or a backend with native unique
constraints before increasing either writer's replica count.
