# OpenAI Chat Completions compatibility

The gateway exposes `POST /v1/chat/completions` and uses Bearer virtual-key
authentication. Responses use the `chat.completion` object and OpenAI-style
error envelope.

## Model routing

`model` must use `provider-slug/upstream-model`. The gateway splits only the
first `/`, resolves the active provider directly from the Dapr State Store, and
invokes its configured `adapter_app_id`. The complete qualified name is returned
unchanged in the response. Any upstream model ID is routable; there is no model
catalog or alias lookup.

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
future JSON members are currently ignored for forward compatibility.

Anthropic thinking blocks are not exposed as chain-of-thought. Their tokens
remain included in usage. Signed thinking blocks cannot yet round-trip through
Chat Completions, so use a provider configuration without default adaptive
thinking until reasoning is added to the IR.
