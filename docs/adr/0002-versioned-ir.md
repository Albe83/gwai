# ADR 0002: Versioned provider-neutral IR

- Status: accepted
- Date: 2026-07-11

## Decision

Ingress adapters translate a client protocol to a versioned generation IR.
Provider adapters translate that IR to one provider protocol. The IR includes
multimodal content, tools, generation controls, usage, and finish reasons.

Adapters fail explicitly when semantics cannot be preserved. Credentials and
provider endpoints are resolved out-of-band and never enter the IR.
Output-token limits are optional in the IR; a provider adapter supplies its
configured default and may enforce its own upper bound.

## Consequences

Adapter growth is additive rather than Cartesian. IR evolution requires an
explicit new version and compatibility plan. Provider-only extensions need a
deliberate IR design or an explicitly namespaced extension, not opaque payload
pass-through.
