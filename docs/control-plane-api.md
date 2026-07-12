# Control-plane APIs

All registered `/v1` operations require `Authorization: Bearer <admin-token>`.
JSON request bodies reject unknown fields. Application errors from those
operations use `application/problem+json`; unmatched routes use Go's standard
HTTP 404/405 response.

| Service | Resource | Collection | Item |
| --- | --- | --- | --- |
| `gwai-control-plane` | Users | `POST, GET /v1/users` | `GET, PUT, DELETE /v1/users/{id}` |
| `gwai-control-plane` | Providers | `POST, GET /v1/providers` | `GET, PUT, DELETE /v1/providers/{id}` |
| `gwai-control-plane` | Models | `POST, GET /v1/models` | `GET, PUT, DELETE /v1/models/{id}` |
| `gwai-virtual-key-control-plane` | Virtual keys | `POST, GET /v1/virtual-keys` | `GET, PUT, DELETE /v1/virtual-keys/{id}` |

The services have independent Kubernetes Services and do not mirror each
other's routes. The default local port forwards in the getting-started guide use
`8081` for users/providers/models and `8082` for virtual keys.

## Administrative WebUI

`gwai-admin-webui` is an HTML backend-for-frontend over these APIs, not a third
owner of lifecycle data. Its route groups are `/users`, `/providers`, `/models`
and `/virtual-keys`; the dashboard is `/`. It sends user/provider/model commands
to `gwai-control-plane` and key commands to
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
server-owned. Status is `active` or `disabled`. User and Model `revision`
values are monotonic, server-owned counters used to order their authorization
projections.

## Providers and routing

A Provider is the GWAI-owned binding to an adapter that a cluster administrator
has already deployed. Its writable contract contains only `slug`, `name`,
`kind`, `adapter_app_id` and `status`:

```json
{
  "slug": "anthropic",
  "name": "Anthropic primary",
  "kind": "anthropic",
  "adapter_app_id": "gwai-anthropic",
  "status": "active"
}
```

`slug` and `adapter_app_id` are unique and immutable. The app ID must equal the
Dapr app ID assigned to one logical adapter workload; all replicas of that
workload share the identity. `name`, `kind` and `status` remain editable,
although `kind` must continue to match the adapter binary: an adapter verifies
the Provider ID, kind and app ID on every IR request and fails closed on a
mismatch. User email addresses are also unique.

Supported Provider kinds and their matching adapter binaries are:

| `kind` | adapter binary |
| --- | --- |
| `anthropic` | `anthropic-adapter` |
| `openai-chat` | `openai-chat-adapter` |
| `openai-responses` | `openai-responses-adapter` |
| `gemini` | `gemini-adapter` |

The upstream base URL, API version and Secret Store reference are deliberately
not Provider fields. They belong to the adapter deployment and cannot be read
or changed through the GWAI Admin API or WebUI. Credential material likewise
never enters Provider state.

## Models

A Model is the stable, client-facing route from an alias to one Provider. Its
provider-native model identifier is optional:

```json
{
  "id": "mdl_...",
  "alias": "claude-sonnet",
  "provider_id": "prv_...",
  "upstream_model": "",
  "status": "active",
  "revision": 1,
  "created_at": "2026-07-12T12:00:00Z",
  "updated_at": "2026-07-12T12:00:00Z"
}
```

Aliases are globally unique, immutable identifiers accepted in every client
protocol's `model` field or path. They contain 1–200 ASCII letters, digits,
`.`, `_`, `:`, `/` or `-`, start with a letter or digit, and are matched
exactly. `provider_id` and `upstream_model` are editable; moving a Model changes
its selected Provider without introducing a gateway/provider protocol coupling.
When `upstream_model` is empty or omitted on input, the gateway sends the public
`alias` upstream. A non-empty override contains 1–300 bytes and cannot contain
CR, LF or NUL. This permits an administrator to expose a stable or masked alias
while targeting a differently named provider model.

Creation and activation require an active Provider. At inference time the
Model, its synchronized model subject and its Provider must all be active.
Disabling any one fails routing closed. A Provider cannot be deleted while it
owns Models. A Model cannot be deleted while any virtual key references its ID;
dependents must be deleted or updated first. No operation cascades or silently
rewrites related resources.

## Virtual keys

`model_ids` is a required, non-empty array of Model IDs. Values are normalized,
deduplicated and sorted. Every ID must have a synchronized, non-deleted Model
subject, and an active key can reference only active Models. This is an explicit
allowlist: there is no wildcard or implicit access to Models created later.

Creation returns the plaintext once:

```json
{
  "virtual_key": {
    "id": "key_...",
    "name": "local",
    "user_id": "usr_...",
    "prefix": "gwai_...",
    "model_ids": ["mdl_..."],
    "status": "active"
  },
  "key": "gwai_one_time_secret"
}
```

The `key` member is never returned again. A user cannot be deleted while it has
virtual keys. A Model cannot be deleted while its per-model key-reference index
is non-empty, and a Provider cannot be deleted until all of its Models are gone.

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

Model lifecycle uses the same fail-closed protocol. The resource control plane
stores canonical Models with Providers, then synchronizes a minimal
`ModelSubject` into virtual-key state. Key create, update and delete operations
update per-model reference indexes and touch each affected model-subject ETag in
their transaction. A Model deletion first fences that subject and verifies its
reference index is empty. A concurrent key mutation and fence therefore cannot
both commit.

Model creation and activation publish canonical state before the active
projection; disablement publishes the disabled projection first. A missing,
stale, disabled or deleted model subject denies authorization. An ambiguous
cross-service result can consequently leave a resource more restrictive than
its canonical record, never more permissive. Repeating the complete Model PUT
advances its revision and repairs a failed synchronization; retrying deletion
completes an idempotent fence followed by canonical removal.

## Runtime boundary

Neither control plane exposes an authorization or route-resolution API to the
data plane. Gateways read key/user/model-subject state plus Model/Provider state
directly. A gateway resolves alias → Model → Provider, selects the effective
upstream name (`upstream_model` or the alias fallback), and invokes only the
Provider's `adapter_app_id`. The selected adapter looks up the Provider by its
own app ID and verifies the route, but takes its upstream connection and Secret
Store reference exclusively from deployment configuration. Gateways translate
the response back with the requested public alias, preventing an upstream model
name from leaking through any supported client protocol.

The resource service invokes four non-public virtual-key operations through Dapr:

- `POST /internal/v1/subjects/sync` for idempotent, ordered status revisions;
- `POST /internal/v1/subjects/fence` for user deletion fencing;
- `POST /internal/v1/model-subjects/sync` for Model status/revision projection;
- `POST /internal/v1/model-subjects/fence` for Model deletion fencing.

They require the Dapr application token and an ACL allowing only the
`gwai-control-plane` identity. They are not admin APIs and must not be exposed
through an ingress.

## State compatibility

The three components remain `gwai-control-state`, `gwai-provider-state` and
`gwai-virtual-key-state`, but this 0.x schema is breaking. Earlier virtual keys
contain qualified strings instead of required Model IDs and have no per-model
reference indexes or model-subject projections. There is no permissive runtime
fallback and no automatic migration. Upgrade using a fresh installation or
explicitly reset and reprovision users, providers, Models and virtual keys.

The adapter-connection ownership change is a separate Helm values migration:
move `providerSlug` and `secretNames` from every `providerAdapters` entry to its
new `upstream.baseURL`, `upstream.apiVersion` and `upstream.secretRef` fields.
Existing Provider state no longer supplies these settings; verify that each
persisted `adapter_app_id` still matches its deployed adapter before rollout.
