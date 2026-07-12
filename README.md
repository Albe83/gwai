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
| Client gateway | Gemini GenerateContent | `POST /v1beta/models/{qualified-model}:generateContent` |
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
    Clients[OpenAI / Anthropic / Gemini clients] --> Gateways[Four protocol gateways]
    CP[Resource control plane<br/>users + providers] -->|private users| CS[(Control state)]
    CP -->|provider lifecycle| PS[(Provider state)]
    CP -->|Dapr subject sync / fence| KP[Virtual-key control plane]
    KP -->|key lifecycle + subject projection| KS[(Virtual-key state)]
    KP -->|validate provider slugs| PS
    Gateways -->|read key + subject| KS
    Gateways -->|resolve route| PS
    Gateways -->|Dapr invocation: IR 2026-07-12| Adapters[Provider-specific adapter instance]
    Adapters -->|read own provider| PS
    Adapters -->|Dapr Secret Store| Secrets[Kubernetes Secret]
    Adapters --> Providers[OpenAI / Anthropic / Gemini APIs]
```

Gateways contain no provider HTTP client and adapters contain no client-gateway
logic. The selected `adapter_app_id` is resolved from state and invoked through
Dapr at `/v1/generate`. Neither control-plane service is on the inference path.
Users/providers and virtual keys have independently deployable administrative
services. Three scoped state components keep private user data, provider runtime
configuration and key authorization records in separate Valkey logical
databases. The contract is
[`2026-07-12.schema.json`](api/ir/2026-07-12.schema.json).

## What works

- Separate CRUD services for users/providers and virtual keys.
- One-time virtual-key disclosure with exact `provider/model` allowlists,
  expiry and user/key/provider disablement.
- Revisioned user-subject projection, atomic deletion fencing and fail-closed
  gateway authorization.
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
make e2e-k3s
```

The default chart exposes all four gateways and deploys one Anthropic adapter
with provider slug `anthropic` and Dapr app ID `gwai-anthropic`. Follow
[getting started](docs/getting-started.md) for real credentials and additional
providers.

## Development

```bash
make check       # formatting, vet, race tests, contract checks, Helm lint
make build       # both control-plane, all gateway and adapter binaries
make images      # all ten OCI images
make helm-lint
```

Runtime Go code has no third-party modules. Infrastructure dependencies are
recorded in [dependencies](docs/dependencies.md).

## Project status

This is pre-release software. Before public exposure, add streaming, quotas,
audit events, external observability, provider failover and a production-grade
high-availability state store. IR `2026-07-12` is intentionally incompatible
with the earlier pre-release IR. The control-plane decomposition also replaces
the former `gwai-state` registry with three state domains. There is no automatic
0.x state migration: use a fresh installation or deliberately reset the old
pre-release state before upgrading.
