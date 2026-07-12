# Control-plane APIs

All registered `/v1` operations require `Authorization: Bearer <admin-token>`.
JSON request bodies reject unknown fields. Application errors from those
operations use `application/problem+json`; unmatched routes use Go's standard
HTTP 404/405 response.

| Service | Resource | Collection | Item |
| --- | --- | --- | --- |
| `gwai-control-plane` | Users | `POST, GET /v1/users` | `GET, PUT, DELETE /v1/users/{id}` |
| `gwai-control-plane` | Providers | `POST, GET /v1/providers` | `GET, PUT, DELETE /v1/providers/{id}` |
| `gwai-virtual-key-control-plane` | Virtual keys | `POST, GET /v1/virtual-keys` | `GET, PUT, DELETE /v1/virtual-keys/{id}` |

The services have independent Kubernetes Services and do not mirror each
other's routes. The default local port forwards in the getting-started guide use
`8081` for users/providers and `8082` for virtual keys.

## Administrative WebUI

`gwai-admin-webui` is an HTML backend-for-frontend over these APIs, not a third
owner of lifecycle data. Its route groups are `/users`, `/providers` and
`/virtual-keys`; the dashboard is `/`. It sends user/provider commands to
`gwai-control-plane` and key commands to
`gwai-virtual-key-control-plane` through Dapr service invocation. API clients
can continue to use the JSON interfaces above directly.

The browser authenticates at `/login` using the existing admin token. A signed,
short-lived challenge keyed independently from that credential avoids
allocating an anonymous server-side session. On success the backend creates an
opaque, expiring session cookie and keeps the control-plane bearer credential
server-side. `/logout` invalidates that session. Unauthenticated pages do not
disclose administrative data and direct browser calls to the two JSON APIs are
unnecessary.

Every state-changing HTML form requires a CSRF token bound to the session. Key
creation also consumes a single-use operation token, so retrying the same form
does not create another credential. Administrative responses use
`Cache-Control: no-store`; the plaintext key from `POST /v1/virtual-keys` is
rendered directly in that response and is not retained or persisted by the
WebUI. Browser forms present API validation and conflict details without
logging credentials or secrets.

Successful `POST`, item `GET` and `PUT` responses include a strong `ETag` for
the returned resource. A `PUT` or `DELETE` may send it verbatim as `If-Match`;
a stale or weak validator returns `409 Conflict`, while malformed syntax,
including an explicitly empty header, returns `400 Bad Request`. Omitting
`If-Match` preserves the unconditional API contract for existing clients. The
WebUI always uses the conditional form for edits, status changes and deletion,
preventing a stale confirmation from silently changing or removing newer data.
For virtual-key creation, the validator identifies the nested `virtual_key`,
not the one-time-secret envelope.

If delivery of a key-creation response fails after the request was sent, the
outcome is inherently ambiguous because the secret is never recoverable. The
WebUI does not offer an immediate retry in that case: it directs the operator
to inspect the key list and delete any matching key before deliberately
creating a replacement. All mutating BFF calls use unknown-length streaming
bodies so Dapr does not automatically replay POST, PUT or DELETE operations;
read-only calls remain retryable. A lost mutation response produces an
"outcome unknown" page without a repeat action, requiring the operator to
reload and inspect current state first.

`PUT` is a complete replacement of editable fields. IDs and timestamps remain
server-owned. Status is `active` or `disabled`. A user's monotonic `revision` is
also server-owned and coordinates its authorization projection.

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

The virtual-key service validates the user against a local, revisioned subject
projection. An active key requires an active, non-deleted subject. Missing or
invalid subject state fails closed. Changing the user to `disabled` revokes all
of that user's keys without rewriting each key record; re-enabling the user with
a newer revision makes otherwise-active keys usable again.

User creation stores the canonical private record before synchronizing its
subject. If that cross-service response is ambiguous, creation returns an error
but retains the user; authorization remains denied until synchronization is
known. Repeating a complete `PUT /v1/users/{id}` advances the revision and
repairs the subject projection.

Updates use the same fail-closed ordering. Activation writes the canonical
record before subject synchronization; if that step fails, a PUT can report an
error after private state changed while authorization remains disabled.
Repeating the complete PUT repairs the projection. Disablement writes the
subject first, so a partial failure can only revoke access early.

User deletion uses a fence rather than a cross-store scan. The virtual-key
service atomically verifies its per-user key index is empty and stores a
non-reversible tombstone before the private user record is removed. Concurrent
key creation or reassignment therefore conflicts with deletion instead of
creating an orphan.

## Runtime boundary

Neither control plane exposes an authorization or route-resolution API to the
data plane. Gateways read key/subject state and provider state directly;
adapters read only provider state.

The resource service invokes two non-public virtual-key operations through Dapr:

- `POST /internal/v1/subjects/sync` for idempotent, ordered status revisions;
- `POST /internal/v1/subjects/fence` for deletion fencing.

They require the Dapr application token and an ACL allowing only the
`gwai-control-plane` identity. They are not admin APIs and must not be exposed
through an ingress.

## State compatibility

This 0.x layout replaces the former single `gwai-state` registry with
`gwai-control-state`, `gwai-provider-state` and `gwai-virtual-key-state`. There
is no automatic migration. Upgrade using a fresh installation or explicitly
reset and reprovision the old pre-release users, providers and virtual keys.
