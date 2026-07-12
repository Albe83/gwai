# ADR 0005: Provider-specific Dapr routing and direct runtime state reads

- Status: accepted
- Date: 2026-07-11

## Decision

Gateways and provider adapters do not invoke the control plane. They use a
read-only runtime interface over Dapr State Store for current users, virtual
keys, and provider records.
The Redis-compatible component uses its component name, not the caller app ID,
as the key prefix so these scoped services share one logical registry.

Clients select a route with `provider-slug/upstream-model`. The gateway resolves
the slug and invokes the provider record's explicit `adapter_app_id` through
Dapr service invocation. Helm creates one adapter identity per configured
provider account. The adapter reads its configured provider by slug and verifies
that both provider ID and app ID match the IR before contacting the upstream.

There is no model catalog. Exact qualified names remain available for
virtual-key allowlists. Provider adapters own default and maximum output-token
policy when the IR omits or supplies the optional value.

## Consequences

Control-plane availability is no longer required for inference requests, and
gateway/adapter coupling is limited to the versioned IR plus Dapr invocation.
Adding a provider account requires both an admin provider record and a matching
Helm adapter instance. Data-plane processes now depend on the persisted entity
schema, so incompatible schema changes require migration or an explicit
pre-release state reset.
