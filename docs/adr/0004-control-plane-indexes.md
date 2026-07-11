# ADR 0004: Transactional control-plane indexes

- Status: accepted
- Date: 2026-07-11

## Decision

Persist resources independently and maintain collection and unique lookup
indexes in the same Dapr state transaction. Use ETags for updates. Hold a local
mutex around compound mutations and deploy one control-plane replica.

## Consequences

Lookups are efficient and the storage adapter remains portable. Lists perform
one read per item, which is acceptable for the initial control-plane scale.
Horizontal writes require a distributed lock or a backend with native unique
constraints before increasing the replica count.

