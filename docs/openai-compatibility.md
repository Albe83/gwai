# OpenAI Chat Completions compatibility

The ingress exposes `POST /v1/chat/completions` and uses standard Bearer API-key
authentication. Responses use the `chat.completion` object and OpenAI-style
error envelope.

## Supported request semantics

- `model`
- `messages` roles: `system`, `developer`, `user`, `assistant`, `tool`
- string content and `text` / `image_url` content arrays
- HTTPS image URLs and JPEG, PNG, GIF, or WebP base64 data URLs
- `max_completion_tokens` and legacy `max_tokens`
- `temperature`, `top_p`, and string/array `stop`
- function `tools`, assistant `tool_calls`, tool results, and `tool_choice`
- strict function schemas and `parallel_tool_calls=false`
- `n=1`

Token usage and stop reasons are translated back to OpenAI names. A logical
model alias is returned in `model`; upstream provider IDs do not leak into the
client contract.

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
future JSON members are currently ignored for forward compatibility.

Anthropic thinking blocks are not exposed as chain-of-thought. Their tokens
remain included in usage. A multi-turn adaptive-thinking tool loop cannot yet
round-trip signed thinking blocks through Chat Completions, so use a route
without default adaptive thinking until reasoning is added to the IR.
