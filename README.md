# gwai

gwai is a provider-neutral AI gateway. It separates lifecycle management from
request translation and uses a versioned intermediate representation (IR) so a
new client API or provider needs one adapter, not a converter for every pair.

The current vertical slice accepts OpenAI-compatible Chat Completions and sends
Anthropic Messages requests. It includes a control plane for users, virtual
keys, providers, and model aliases.

## What works

- CRUD lifecycle APIs for users, virtual keys, providers, and models.
- One-time virtual-key disclosure; only a SHA-256 digest and display prefix are
  persisted.
- Per-key model allowlists, expiry, user/key/model/provider disablement, and
  dependency-safe deletion.
- OpenAI Chat Completions input for text, image URLs/data URLs, system and
  developer messages, tools, tool calls, and tool results.
- Anthropic Messages output with tool and usage translation.
- Dapr service invocation, transactional state management, Kubernetes secret
  resolution, mTLS, app/API tokens, ACLs, API allowlists, and retry policy.
- A Helm chart with non-root distroless services and a persistent Valkey state
  store for local k3s.

Streaming is intentionally rejected with an explicit OpenAI-style error in
this first slice. See [API compatibility](docs/openai-compatibility.md) for the
exact supported surface.

## Architecture

```mermaid
flowchart LR
    Client[OpenAI client] -->|POST /v1/chat/completions| Ingress[OpenAI ingress]
    Ingress -->|authorize + resolve route| CP[Control plane]
    Ingress -->|Dapr invocation: IR| Anthropic[Anthropic adapter]
    Anthropic -->|resolve provider| CP
    Anthropic -->|Dapr Secret Store| Secrets[Kubernetes Secret]
    Anthropic -->|POST /v1/messages| Provider[Anthropic API]
    CP -->|Dapr State Store| Valkey[(Valkey)]
```

The detailed boundaries, request sequence, and scaling constraints are in
[Architecture](docs/architecture.md). The wire contract is
[`2026-07-01.schema.json`](api/ir/2026-07-01.schema.json).

## Local k3s quick start

Required tools: Go 1.26, a Docker-compatible CLI, k3s, kubectl, Dapr 1.18,
Helm 3, curl, and jq.

```bash
make local-deploy
kubectl -n gwai get pods
```

The target builds the three images, imports them into k3s containerd, and
installs the Helm release. To verify the whole path without an external API
key:

```bash
make e2e-k3s
```

For a real Anthropic provider and example provisioning calls, follow
[Getting started](docs/getting-started.md).

## Development

```bash
make check       # formatting, vet, race-enabled tests, Helm lint
make build       # bin/control-plane, bin/openai-gateway, bin/anthropic-adapter
make images      # local OCI images
make helm-lint
```

Runtime code has no third-party Go modules. Infrastructure dependencies and
their rationale are recorded in [Dependencies](docs/dependencies.md).

## Project status

This is an initial, runnable vertical slice rather than a production release.
Before public exposure, add streaming, quotas/rate limits, audit events,
external observability backends, provider failover, and a production-grade
high-availability state store.
