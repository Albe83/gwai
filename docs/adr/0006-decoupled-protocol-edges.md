# ADR 0006: Decoupled gateway and provider protocol edges

- Status: accepted
- Date: 2026-07-12

## Context

Supporting OpenAI Chat, OpenAI Responses, Anthropic Messages and Gemini on both
sides could produce pairwise gateway-to-adapter converters. That would make a
gateway aware of providers and make every new protocol multiply existing work.

## Decision

Each public gateway translates only its client wire contract to/from the
versioned IR. It authorizes the virtual key and resolves an opaque
`adapter_app_id` through the common data-plane dispatcher.

Each provider adapter exposes only internal `POST /v1/generate`, validates the
IR route against its configured provider ID, kind and own app ID, loads its
scoped credential, and translates IR to/from one provider API.

There are no direct OpenAI↔Anthropic↔Gemini converters, no provider HTTP clients
inside gateways and no client-protocol branching inside adapters. Dapr service
invocation and IR `2026-07-12` are the only runtime boundary between them.

## Consequences

- Protocol growth is additive: four client edges plus four provider edges.
- Any gateway can select any adapter at runtime without rebuilding either side.
- Lossy features are rejected at the edge that cannot preserve them; the
  gateway does not inspect provider kind or negotiate provider capabilities.
- Shared wire DTOs for the same public/provider protocol are allowed, but
  gateway and adapter translation functions remain independent.
- Integration tests must exercise unlike gateway/adapter pairs and route
  operation while the control plane is unavailable.
