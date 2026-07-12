# OpenAI Chat Completions compatibility

OpenAI Responses and the non-OpenAI protocol surfaces are documented in the
[cross-protocol compatibility matrix](protocol-compatibility.md).

The gateway exposes `POST /v1/chat/completions` and uses Bearer virtual-key
authentication. Responses use the `chat.completion` object and OpenAI-style
error envelope.

## Model routing

`model` is a globally unique, immutable Model alias. The gateway resolves that
catalog record, verifies that both the Model and its Provider are active, then
invokes the Provider's configured `adapter_app_id`. A non-empty
`upstream_model` override enters the provider-neutral IR; otherwise the gateway
uses the public alias. Provider endpoints, API versions, Secret references and
credentials are owned by the adapter deployment and remain outside Provider
state and IR. The public response continues to identify the requested alias,
even when the upstream provider returns its real model name.

A virtual key can use only the non-empty set of Model IDs assigned to it. Model
aliases are never interpreted as provider protocol names, so adding or moving a
Model does not introduce a gateway-to-adapter dependency.

## Supported request semantics

- `messages` roles: `system`, `developer`, `user`, `assistant`, `tool`
- string content and `text` / `image_url` content arrays
- HTTPS image URLs and JPEG, PNG, GIF, or WebP base64 data URLs
- optional `max_completion_tokens` and legacy `max_tokens`; adapter default when omitted
- `temperature`, `top_p`, and string/array `stop`
- function `tools`, assistant `tool_calls`, tool results, and `tool_choice`
- strict function schemas and `parallel_tool_calls=false`
- `n=1`

Token usage and stop reasons are translated back to OpenAI names.

## Explicitly unsupported

- streaming (`stream=true`)
- multiple choices (`n` other than 1)
- non-zero frequency/presence penalties
- log probabilities and logit bias
- response/structured-output formats
- seeded generation and named messages
- metadata, stream options, and the legacy `user` safety identifier

Unsupported known parameters return HTTP 400 with code
`unsupported_parameter`; the gateway does not silently discard them. Unknown
JSON members are rejected as invalid requests.

Provider thinking/reasoning blocks are not exposed as chain-of-thought. Their
tokens remain included in usage where the provider reports them. Gemini tool
call thought signatures have a dedicated IR field, but general reasoning is
outside the portable contract.
