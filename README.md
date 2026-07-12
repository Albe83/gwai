# gwai

gwai is a provider-neutral AI gateway. Four independent client gateways and
four provider adapters meet only at a validated, versioned intermediate
representation (IR). Adding a client or provider protocol therefore adds one
translator instead of a converter for every client/provider pair.

## Supported protocol edges

| Direction | Protocol | Endpoint / provider kind |
| --- | --- | --- |
| Client gateway | OpenAI Chat Completions | `POST /v1/chat/completions` |
| Client gateway | OpenAI Responses | `POST /v1/responses` |
| Client gateway | Anthropic Messages | `POST /v1/messages` |
| Client gateway | Gemini GenerateContent | `POST /v1beta/models/{model-alias}:generateContent` |
| Provider adapter | OpenAI Chat Completions | `openai-chat` |
| Provider adapter | OpenAI Responses | `openai-responses` |
| Provider adapter | Anthropic Messages | `anthropic` |
| Provider adapter | Gemini GenerateContent | `gemini` |

Every gateway can route to every adapter when the requested semantics belong to
the portable subset. Text, system instructions, images, function tools/calls/
results, common sampling controls, stop reasons and token usage cross the IR.
Streaming, stateful conversations, hosted tools, reasoning/thinking and
structured output are rejected explicitly rather than silently discarded.
See [protocol compatibility](docs/protocol-compatibility.md) for exact limits.

## Architecture

```mermaid
flowchart LR
    Admin[Administrator browser] --> UI[Administrative WebUI]
    UI -->|authenticated Dapr BFF calls| CP
    UI -->|authenticated Dapr BFF calls| KP
    Clients[OpenAI / Anthropic / Gemini clients] --> Gateways[Four protocol gateways]
    CP[Resource control plane<br/>users + models + providers] -->|private users| CS[(Control state)]
    CP -->|model + provider lifecycle| PS[(Provider state)]
    CP -->|Dapr user/model sync + fence| KP[Virtual-key control plane]
    KP -->|key lifecycle + validate user/model projections| KS[(Virtual-key state)]
    Gateways -->|read key + user/model subjects| KS
    Gateways -->|resolve model alias + provider| PS
    Gateways -->|Dapr invocation: IR 2026-07-12| Adapters[Provider-specific adapter instance]
    Adapters -->|read own provider| PS
    Adapters -->|Dapr Secret Store| Secrets[Kubernetes Secret]
    Adapters --> Providers[OpenAI / Anthropic / Gemini APIs]
```

Gateways contain no provider HTTP client and adapters contain no client-gateway
logic. The selected `adapter_app_id` is resolved from state and invoked through
Dapr at `/v1/generate`. Neither control-plane service is on the inference path.
Users/models/providers and virtual keys have independently deployable
administrative services. Three scoped state components keep private user data,
the model/provider routing catalog and key authorization records in separate
Valkey logical databases. The contract is
[`2026-07-12.schema.json`](api/ir/2026-07-12.schema.json).

## What works

- Separate CRUD services for users/models/providers and virtual keys.
- A server-rendered administrative WebUI for user, model, provider and virtual-key
  lifecycle operations; the admin credential remains in its Go backend, and an
  optional Gateway API HTTPRoute can expose it through an existing HTTPS
  Gateway.
- One-time virtual-key disclosure with non-empty Model-ID allowlists,
  expiry and user/key/Model/Provider disablement.
- Revisioned user/model projections, atomic deletion fencing and fail-closed
  gateway authorization across the complete User → VKey → Model → Provider chain.
- Direct data-plane reads and provider-specific Dapr service invocation.
- Per-provider identities, Secret scopes, Dapr mTLS/tokens/ACLs and retries.
- Generic Helm lists for any mix of the four gateways and provider adapters.
- Non-root distroless services and persistent Valkey state for local k3s.
- Race-tested translators and an E2E path that sends all four client protocols
  through one adapter while both control-plane services are unavailable.

## Local k3s quick start

Required tools: Go 1.26, a Docker-compatible CLI, k3s, kubectl, Dapr 1.18,
Helm 3, curl and jq.

```bash
make local-deploy
kubectl -n gwai get pods
kubectl -n gwai port-forward service/gwai-admin-webui 28087:8080
make e2e-k3s
```

Open `http://127.0.0.1:28087` and sign in with the generated admin token. The
WebUI is deliberately a cluster-internal Service; use TLS before exposing it
beyond loopback or a trusted network. See
[getting started](docs/getting-started.md) for the complete login and
provisioning flow.

The default chart exposes all four gateways and deploys one Anthropic adapter
with provider slug `anthropic` and Dapr app ID `gwai-anthropic`. Follow
[getting started](docs/getting-started.md) for real credentials and additional
providers.

## Development

```bash
make check       # formatting, vet, race tests, contract checks, Helm lint
make build       # both control-plane, all gateway and adapter binaries
make images      # all service OCI images, including the administrative WebUI
make helm-lint
```

Runtime Go code has no third-party modules. Infrastructure dependencies are
recorded in [dependencies](docs/dependencies.md).

## Project status

This is pre-release software. Before public exposure, add streaming, quotas,
audit events, external observability, provider failover and a production-grade
high-availability state store. IR `2026-07-12` is intentionally incompatible
with the earlier pre-release IR. The control-plane decomposition replaces the
former `gwai-state` registry with three state domains, and the Model catalog
replaces qualified model strings in virtual keys with required Model IDs. There
is no automatic 0.x state migration: use a fresh installation or deliberately
reset and reprovision all pre-release users, providers, models and virtual keys
before upgrading.
