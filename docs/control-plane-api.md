# Control-plane API

All `/v1` endpoints require `Authorization: Bearer <admin-token>`. JSON request
bodies reject unknown fields. Errors use `application/problem+json`.

| Resource | Collection | Item |
| --- | --- | --- |
| Users | `POST, GET /v1/users` | `GET, PUT, DELETE /v1/users/{id}` |
| Providers | `POST, GET /v1/providers` | `GET, PUT, DELETE /v1/providers/{id}` |
| Models | `POST, GET /v1/models` | `GET, PUT, DELETE /v1/models/{id}` |
| Virtual keys | `POST, GET /v1/virtual-keys` | `GET, PUT, DELETE /v1/virtual-keys/{id}` |

`PUT` is a complete replacement of editable fields. IDs and timestamps remain
server-owned. Status is `active` or `disabled`.

## Relationships

- A virtual key references an existing user and optionally lists allowed model
  aliases. An empty allowlist means every active model.
- User email addresses are normalized to lowercase and are unique.
- A model alias is unique and references one provider.
- A provider selects a provider kind, Dapr adapter app ID, endpoint, API
  version, and secret reference.
- A user with keys, provider with models, or model named by a key cannot be
  deleted until its dependents are removed.

## Virtual-key creation

Creation returns:

```json
{
  "virtual_key": {
    "id": "key_...",
    "name": "local",
    "user_id": "usr_...",
    "prefix": "gwai_...",
    "status": "active"
  },
  "key": "gwai_one_time_secret"
}
```

The `key` member is never returned again. List, get, and update responses expose
only metadata and the display prefix.

## Internal endpoints

The data plane uses three Dapr-only operations:

- `POST /internal/v1/authorize`
- `POST /internal/v1/routes/resolve`
- `POST /internal/v1/providers/resolve`

They require the Dapr app token, and Dapr ACLs permit only the exact calling app
IDs and methods. They are not an administrative API.
