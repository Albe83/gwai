# ADR 0009: Model catalog and virtual-key references

- Status: accepted; amended by ADR 0010
- Date: 2026-07-12

## Context

ADR 0005 removed the first Model catalog and let clients select an arbitrary
`provider-slug/upstream-model`. That kept gateways independent from provider
protocols, but it made the virtual-key allowlist a set of ungoverned strings.
There was no lifecycle endpoint for a routable model and no stable foreign key
between authorization policy and routing configuration.

The required domain chain is User → VKey → Model → Provider. It must preserve
the existing split control planes, direct data-plane state reads and the
gateway/adapter IR boundary.

## Decision

Reintroduce canonical Models in `gwai-provider-state`, owned by
`gwai-control-plane` alongside Providers. Expose `POST, GET /v1/models` and
`GET, PUT, DELETE /v1/models/{id}`. A Model contains a server ID, globally
unique immutable `alias`, `provider_id`, `upstream_model`, status, monotonic
revision and timestamps. Provider assignment and upstream model are editable;
the upstream value is optional and falls back to the alias. Creation and
activation require an active Provider. ADR 0010 formalizes that fallback.

Model does not duplicate an output-token default or cap. Those limits remain
adapter-owned policy, so routing metadata and provider safety policy cannot
drift into two competing sources of truth.

Clients send the Model alias. Gateways resolve alias → Model → Provider, then
put only `provider_id` and the effective upstream name (override or alias) in
the provider-neutral IR and invoke the Provider's `adapter_app_id`. As amended
by ADR 0010, adapters resolve and verify their Provider by their own app ID and
take connection settings only from their deployment. No gateway imports a
provider protocol or addresses a hard-coded adapter.

Replace virtual-key string allowlists with a required, non-empty `model_ids`
array. The virtual-key service validates each ID against its synchronized Model
subject; it receives no provider-state scope. There is no wildcard: a key does
not gain access when another Model is created.

Because canonical Models and keys occupy different state components, project a
minimal revisioned `ModelSubject` into `gwai-virtual-key-state`. Key mutations
atomically maintain per-Model reference indexes and touch the affected
Model-subject ETags. The resource control plane synchronizes lifecycle state at
`POST /internal/v1/model-subjects/sync`. Before deleting a Model it calls
`POST /internal/v1/model-subjects/fence`; that transaction verifies the Model's
key index is empty and writes a deletion tombstone. A concurrent key assignment
and fence cannot both commit.

Model disablement is projected before the canonical update; activation is
projected afterwards. Missing, mismatched, disabled or deleted projection state
fails authorization closed. Replaying an identical subject revision or fence is
idempotent, stale/conflicting revisions fail, and a later complete PUT repairs
an ambiguous synchronization result.

Keep Model-to-Provider integrity inside `gwai-provider-state`. Model mutations
maintain a per-Provider index, and Provider deletion is rejected while that
index is non-empty. All deletion policies are `RESTRICT`; the system never
cascades or silently rewrites keys, Models or Providers.

## Consequences

The two administrative services and all three State Stores remain. Resource
control-plane Model lifecycle now depends on the virtual-key control plane for
projection/fencing, just as user lifecycle does. Provider-only administration
and direct data-plane reads retain their existing ownership boundaries.

Inference still does not invoke a control plane and remains available when both
administrative Deployments are absent, provided the two runtime state
components, Dapr, the selected adapter and upstream Provider are healthy.

This is a breaking pre-release state and public API change. Existing keys carry
qualified strings and lack Model subjects/reference indexes. There is no
automatic or permissive fallback migration; operators must use a fresh 0.x
installation or explicitly reset and reprovision users, Providers, Models and
virtual keys.
