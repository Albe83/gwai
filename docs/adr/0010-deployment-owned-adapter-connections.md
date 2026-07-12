# ADR 0010: Deployment-owned adapter connections and app-ID binding

- Status: accepted
- Date: 2026-07-12
- Amends: ADR 0005, ADR 0006 and ADR 0009

## Context

An adapter instance is infrastructure: a cluster administrator chooses where it
runs, which Dapr identity it receives, which upstream account it can reach and
which Secret it may read. Storing the upstream base URL, API version and Secret
reference in the GWAI Provider resource gave a GWAI administrator a second,
potentially conflicting connection configuration. Resolving the adapter's
Provider by a separately configured slug also coupled the deployment to two
identifiers even though gateways already dispatch through Dapr app ID.

The Model catalog must still support a public name that differs from the real
provider model name. That mapping cannot allow the upstream name to leak back
through OpenAI, Anthropic or Gemini client responses.

## Decision

The cluster administrator deploys each adapter with:

- a unique Dapr app ID;
- the adapter binary, which fixes its Provider `kind`;
- the upstream base URL and API version;
- a Dapr Secret Store reference (`store`, `name`, `key` and optional
  `namespace`); and
- adapter-local timeout and output-token policy.

The process validates these deployment settings before serving traffic. The
credential value remains in the Secret Store and neither connection metadata
nor credential material enters IR or GWAI state.

The GWAI administrator creates a Provider containing only `slug`, `name`,
`kind`, `adapter_app_id` and `status` plus server-owned metadata. `slug` is a
GWAI identifier, not adapter configuration. `adapter_app_id` is unique and
immutable and must match one logical deployed adapter workload; its replicas
share that identity.

A gateway resolves the client alias through Model → Provider, obtains the
Provider's `adapter_app_id` and invokes only `/v1/generate` through Dapr. An
adapter uses its own app ID to resolve the Provider record and verifies that the
IR Provider ID, Provider kind, active status and app ID all match before reading
the Secret or contacting the upstream. It does not resolve by slug and does not
invoke the control plane.

`Model.upstream_model` is optional. The gateway puts the non-empty override in
IR when configured and otherwise uses the immutable public Model alias as the
effective upstream name. On the return path every gateway writes the requested
public alias into its client protocol response; provider-returned model names
remain internal.

## Consequences

GWAI administrators can manage routing and lifecycle without gaining control of
provider endpoints or credentials. Changing a base URL, API version or Secret
reference is an infrastructure rollout. Because app IDs are immutable in
Provider state, changing one requires a coordinated new deployment and Provider
binding rather than a Provider update. Changing a Model's Provider or upstream
override remains a GWAI administrative operation.

A missing adapter, duplicate/mismatched app ID, wrong kind, disabled Provider or
invalid deployment setting fails closed. The Provider slug can differ from the
Helm adapter name and has no effect on Dapr dispatch. Gateways remain independent
from provider protocols, adapters remain independent from client protocols and
both continue to read runtime state directly.

Existing Helm values must replace `providerSlug` and `secretNames` with
`upstream.baseURL`, `upstream.apiVersion` and `upstream.secretRef`. Existing
Provider connection fields are no longer read; operators must preserve and
verify the Provider/deployment app-ID binding during rollout. Existing Models
with a non-empty `upstream_model` retain their explicit mapping, while an empty
value now deliberately falls back to the alias.

This supersedes ADR 0005's adapter lookup by configured Provider slug, clarifies
ADR 0006's route validation and amends ADR 0009 by making
`upstream_model` optional with an alias fallback.
