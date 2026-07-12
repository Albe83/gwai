# Protocol compatibility

gwai exposes four unary client APIs and can target four provider APIs. Client
and provider selection are independent: a Model alias resolves a catalog record
and then the Provider whose `adapter_app_id` receives the canonical IR.

## Protocol endpoints

| Protocol | Client endpoint and authentication | Provider kind and upstream request |
| --- | --- | --- |
| OpenAI Chat | `POST /v1/chat/completions`, Bearer key | `openai-chat`, `POST /{api_version}/chat/completions`, Bearer key |
| OpenAI Responses | `POST /v1/responses`, Bearer key | `openai-responses`, `POST /{api_version}/responses`, Bearer key |
| Anthropic Messages | `POST /v1/messages`, `x-api-key` plus `anthropic-version: 2023-06-01` | `anthropic`, `POST /v1/messages`, `x-api-key` plus configured version |
| Gemini GenerateContent | `POST /v1beta/models/{model-alias}:generateContent`, `x-goog-api-key` | `gemini`, `POST /{api_version}/models/{model}:generateContent`, `x-goog-api-key` |

The implementations follow the official [OpenAI Responses migration guide](https://developers.openai.com/api/docs/guides/migrate-to-responses),
[OpenAI function-calling guide](https://developers.openai.com/api/docs/guides/function-calling),
[Anthropic Messages API](https://platform.claude.com/docs/en/api/messages/create),
and [Gemini GenerateContent API](https://ai.google.dev/api/generate-content).

## Portable request subset

- one generated candidate/choice;
- leading system/developer text instructions;
- user text and JPEG, PNG, GIF or WebP images;
- assistant text and function calls;
- function results containing text/images or a JSON object where the target
  protocol supports that representation;
- function declarations with object JSON Schema, `auto`, `none`, `required`
  and a single named choice;
- optional maximum output tokens, temperature `0..1`, top-p and stop sequences;
- cache-aware input usage, output usage and normalized finish reasons.

Tool call IDs and names are resolved before entering IR. Gemini
`thoughtSignature` is retained on the corresponding IR tool call; when a
Gemini 3 conversation arrives from a protocol that cannot carry a signature,
the adapter uses Google's documented
[`skip_thought_signature_validator`](https://ai.google.dev/gemini-api/docs/generate-content/thought-signatures)
compatibility value.

## Explicit exclusions

All gateways reject these features with their native error envelope:

- streaming and streaming options;
- multiple candidates/choices;
- stored/stateful conversations, previous response IDs and background jobs;
- reasoning/thinking input or visible chain-of-thought output;
- hosted search, code execution, retrieval and other provider-owned tools;
- structured response formats/schemas, log probabilities and seeded sampling;
- safety settings, service tiers and opaque metadata that cannot be preserved.

Unknown JSON members are rejected. Recognized unsupported members receive a
specific error instead of being silently ignored.

## Provider-specific limits

- OpenAI Responses does not expose stop sequences; its adapter rejects IR
  requests that contain them.
- Gemini accepts only inline base64 images through IR. It does not download an
  arbitrary HTTP image URL on behalf of the caller.
- Anthropic and OpenAI Chat do not expose model-generated images in this text
  generation subset.
- `strict` function schema and `disable_parallel` are rejected by a target
  adapter when that provider cannot preserve them.
- Anthropic reports uncached, cache-created and cache-read input buckets
  separately. IR stores their total plus both details, and the Anthropic
  gateway reconstructs the native fields.

## Output mapping

IR normalizes completion states to `stop`, `length`, `tool_calls` or
`content_filter`. Each gateway converts those values back to its public API.
Provider response IDs remain diagnostic metadata; the public gateway creates
an ID in its own protocol namespace. Usage always reports total input and
output tokens, with cached-input detail where the client API supports it.
