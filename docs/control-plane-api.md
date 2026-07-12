# Control-plane API

All `/v1` endpoints require `Authorization: Bearer <admin-token>`. JSON request
bodies reject unknown fields. Errors use `application/problem+json`.

| Resource | Collection | Item |
| --- | --- | --- |
| Users | `POST, GET /v1/users` | `GET, PUT, DELETE /v1/users/{id}` |
| Providers | `POST, GET /v1/providers` | `GET, PUT, DELETE /v1/providers/{id}` |
| Virtual keys | `POST, GET /v1/virtual-keys` | `GET, PUT, DELETE /v1/virtual-keys/{id}` |

`PUT` is a complete replacement of editable fields. IDs and timestamps remain
server-owned. Status is `active` or `disabled`.

## Providers and routing

A provider contains a unique lowercase DNS-label `slug` and an explicit Dapr
`adapter_app_id`:

```json
{
  "slug": "anthropic",
  "name": "Anthropic primary",
  "kind": "anthropic",
  "adapter_app_id": "gwai-anthropic",
  "base_url": "https://api.anthropic.com",
  "api_version": "2023-06-01",
  "secret_ref": {
    "store": "kubernetes",
    "name": "gwai-anthropic",
    "key": "api-key"
  }
}
```

`slug` and `adapter_app_id` are unique, immutable, and must match one Helm adapter
instance. Endpoint, API version, secret reference, name, and status remain
editable. User email addresses are also unique.

Supported provider kinds and defaults:

| `kind` | default `base_url` | default `api_version` | adapter binary |
| --- | --- | --- | --- |
| `anthropic` | `https://api.anthropic.com` | `2023-06-01` | `anthropic-adapter` |
| `openai-chat` | `https://api.openai.com` | `v1` | `openai-chat-adapter` |
| `openai-responses` | `https://api.openai.com` | `v1` | `openai-responses-adapter` |
| `gemini` | `https://generativelanguage.googleapis.com` | `v1beta` | `gemini-adapter` |

`base_url` must be absolute HTTP(S) without credentials, query or fragment.
`api_version` is a path-safe version token. A provider record is accepted only
for one of the listed kinds; the adapter verifies its kind again on every IR
request.

There is no model catalog. Clients address any upstream model as
`provider-slug/upstream-model`; only the first `/` is structural, so an upstream
model ID may itself contain `/`.

## Virtual keys

`allowed_models` contains exact qualified model names. Each referenced provider
slug must exist when the key is created or updated. An empty list permits every
model reachable through an active provider.

Creation returns the plaintext once:

```json
{
  "virtual_key": {
    "id": "key_...",
    "name": "local",
    "user_id": "usr_...",
    "prefix": "gwai_...",
    "allowed_models": ["anthropic/claude-sonnet"],
    "status": "active"
  },
  "key": "gwai_one_time_secret"
}
```

The `key` member is never returned again. A user cannot be deleted while it has
virtual keys. Deleting a provider makes its qualified names unroutable but does
not rewrite virtual-key allowlists.

## Runtime boundary

The control plane exposes no internal authorization or route-resolution HTTP
API. Gateway and adapter processes use a read-only runtime interface over the
Dapr State Store; only administrative mutations cross the control-plane HTTP
boundary.
