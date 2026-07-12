# ADR 0002: Versioned provider-neutral IR

- Status: accepted
- Date: 2026-07-11
- Updated: 2026-07-12

## Decision

Ingress adapters translate a client protocol to a versioned generation IR.
Provider adapters translate that IR to one provider protocol. The IR includes
multimodal content, tools, generation controls, usage, and finish reasons.

Adapters fail explicitly when semantics cannot be preserved. Credentials and
provider endpoints are resolved out-of-band and never enter the IR.
Output-token limits are optional in the IR; a provider adapter supplies its
configured default and may enforce its own upper bound.

Version `2026-07-12` also carries structured JSON tool results, the originating
tool name, an optional Gemini thought signature on function calls, and separate
cache-creation/cache-read usage. Role/content combinations and leading system
messages are validated at both process boundaries. Sampling ranges use the
portable intersection so an IR accepted from any gateway is syntactically safe
for every adapter, though an adapter may still reject unsupported semantics.

## Consequences

Adapter growth is additive rather than Cartesian. IR evolution requires an
explicit new version and compatibility plan. Provider-only extensions need a
deliberate IR design or an explicitly namespaced extension, not opaque payload
pass-through.
