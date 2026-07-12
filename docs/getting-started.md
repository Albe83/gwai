# Getting started on k3s

## 1. Build and deploy

```bash
make local-deploy
kubectl -n gwai wait --for=condition=Ready pod --all --timeout=180s
```

For a registry-backed cluster, set `REGISTRY` and `TAG`, push the images, then
run `make deploy REGISTRY=... TAG=...`.

The default values expose all four client protocols and deploy all four
implemented adapters. The Anthropic entry illustrates their common shape:

```yaml
gateways:
  - name: openai-gateway
    image: {repository: gwai-openai-gateway}
    # openai-responses-gateway, anthropic-gateway and gemini-gateway follow

providerAdapters:
  - name: primary
    kind: anthropic
    appID: gwai-anthropic
    image: {repository: gwai-anthropic-adapter}
    upstream:
      baseURL: https://api.anthropic.com
      apiVersion: 2023-06-01
      secretRef:
        store: kubernetes
        name: gwai-anthropic
        key: api-key
        namespace: ""
```

The other default identities are `gwai-openai-chat`,
`gwai-openai-responses` and `gwai-gemini`, using the matching adapter image and
the deployment-owned Secrets of the same name. This makes every implemented
adapter discoverable after a standard deploy; inference through one still
requires its Provider registration, Model and credential.

Add one `providerAdapters` entry per provider account. `name` and `appID` must
be unique. The cluster administrator owns the app ID and the complete
`upstream` block; none of those connection settings is administered through
GWAI. Select the image matching `kind`:

| `kind` | image repository |
| --- | --- |
| `anthropic` | `gwai-anthropic-adapter` |
| `openai-chat` | `gwai-openai-chat-adapter` |
| `openai-responses` | `gwai-openai-responses-adapter` |
| `gemini` | `gwai-gemini-adapter` |

Optional `defaultMaxOutputTokens` and `maxOutputTokens` values are passed to
adapters that enforce local token policy.

`baseURL` must be an absolute HTTP(S) URL without credentials, query or
fragment, and `apiVersion` must be a path-safe version token. `secretRef.store`,
`name` and `key` are required; `namespace` is optional. Additional adapter
entries have no implicit provider connection: set every `upstream` value
explicitly.

The chart also deploys separate resource and virtual-key control planes. Their
state is divided into three Dapr components backed by Valkey logical databases:

```yaml
dapr:
  stateStores:
    controlPlane: {name: gwai-control-state, redisDB: 0}
    providers: {name: gwai-provider-state, redisDB: 1}
    virtualKeys: {name: gwai-virtual-key-state, redisDB: 2}
valkey:
  databases: 16 # bundled server; every redisDB must be below this value
```

It also deploys `gwai-admin-webui`, a cluster-internal, server-rendered UI that
coordinates lifecycle operations through those two APIs without receiving
access to any State Store.

## 2. Create a provider secret

The default adapter can read only `gwai-anthropic`. Never place provider keys in
Helm values or Git.

```bash
kubectl -n gwai create secret generic gwai-anthropic \
  --from-literal=api-key="$ANTHROPIC_API_KEY"
```

Set the matching `providerAdapters[].upstream.secretRef.name` before upgrading
the release to use another Secret. The chart derives both the adapter's
Kubernetes RBAC `resourceNames` and its Dapr Secret Store scope from that one
reference. The Secret value itself remains outside Helm and Provider state.

## 3. Reach the APIs

```bash
kubectl -n gwai port-forward service/gwai-control-plane 8081:8080
kubectl -n gwai port-forward service/gwai-virtual-key-control-plane 8082:8080
kubectl -n gwai port-forward service/gwai-admin-webui 28087:8080
kubectl -n gwai port-forward service/gwai-openai-gateway 8080:8080
# alternatives: gwai-openai-responses-gateway, gwai-anthropic-gateway,
#               gwai-gemini-gateway
```

Load the generated admin token without printing it:

```bash
GWAI_ADMIN_TOKEN=$(kubectl -n gwai get secret gwai-admin \
  -o jsonpath='{.data.admin-token}' | base64 -d)
```

When `admin.existingSecret` is managed externally, rotating its value does not
hot-reload environment variables. Restart all consumers immediately after the
Secret update:

```bash
kubectl -n gwai rollout restart deployment/gwai-control-plane \
  deployment/gwai-virtual-key-control-plane deployment/gwai-admin-webui
kubectl -n gwai rollout status deployment \
  --selector app.kubernetes.io/instance=gwai --timeout=180s
```

For the browser workflow, open `http://127.0.0.1:28087`, enter that token in
the login form, and then manage users, providers, models and virtual keys from the
dashboard. The token is checked by the Go backend and is not stored in browser
storage or forwarded to client-side JavaScript. The resulting session cookie is
HTTP-only and mutations require CSRF protection. A newly created virtual key is
shown directly in the no-store creation response and is not retained by the
WebUI; copy it before leaving that page. Edit, status and delete confirmations
use ETags to reject stale actions. If the UI reports an unknown key-creation
outcome, inspect the key list and delete any matching key before deliberately
creating a replacement.

Plain HTTP is acceptable only for this loopback port-forward. Keep the Service
cluster-internal and terminate TLS before exposing the WebUI on a network. Set
`adminWebUI.secureCookies=true` for that HTTPS deployment so the session cookie
also receives the `Secure` flag and the service emits HSTS.

### Expose the WebUI with Gateway API

The chart can attach the WebUI Service to an existing Gateway API `Gateway` by
creating an optional `gateway.networking.k8s.io/v1` `HTTPRoute`. The cluster
must already have the Gateway API CRDs, a controller and an HTTPS listener with
TLS termination. The chart does not create the Gateway or its certificate.

```yaml
adminWebUI:
  secureCookies: true
  httpRoute:
    enabled: true
    annotations: {}
    parentRefs:
      - name: edge-gateway
        namespace: gateway-system
        sectionName: https
    hostnames:
      - admin.example.com
```

`parentRefs` accepts multiple Gateways. Each reference requires `name` and
`sectionName`, can additionally select `namespace`, and must identify an HTTPS
listener. Port-based listener selection is intentionally excluded because it is
an Extended Gateway API feature and is redundant with `sectionName`. At least
one explicit administrative hostname is required.
A cross-namespace Gateway must allow routes from the gwai namespace through its
listener `allowedRoutes` policy.

The chart rejects an enabled HTTPRoute unless `secureCookies=true` and every
reference names a listener. It cannot inspect the referenced Gateway, so the
operator remains responsible for ensuring that each `sectionName` really is an
HTTPS listener with a valid certificate. After the Helm upgrade, verify that
the controller reports both `Accepted=True` and `ResolvedRefs=True` before
opening the hostname:

```bash
kubectl -n gwai describe httproute gwai-admin-webui
```

## 4. Provision a Provider, Model and client key

The cluster administrator hands the deployed adapter's `appID` and `kind` to
the GWAI administrator. The upstream URL, version and Secret reference are not
part of this handoff and are not accepted by the Provider API.

```bash
USER_ID=$(curl -fsS http://127.0.0.1:8081/v1/users \
  -H "Authorization: Bearer $GWAI_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Local user","email":"local@example.com"}' | jq -r .id)

PROVIDER_ID=$(curl -fsS http://127.0.0.1:8081/v1/providers \
  -H "Authorization: Bearer $GWAI_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "slug":"anthropic",
    "name":"Anthropic primary",
    "kind":"anthropic",
    "adapter_app_id":"gwai-anthropic",
    "status":"active"
  }' | jq -r .id)

MODEL_ID=$(curl -fsS http://127.0.0.1:8081/v1/models \
  -H "Authorization: Bearer $GWAI_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{
    \"alias\":\"claude-sonnet\",
    \"provider_id\":\"$PROVIDER_ID\",
    \"upstream_model\":\"claude-sonnet-4-6\"
  }" | jq -r .id)

GWAI_API_KEY=$(curl -fsS http://127.0.0.1:8082/v1/virtual-keys \
  -H "Authorization: Bearer $GWAI_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"local\",\"user_id\":\"$USER_ID\",\"model_ids\":[\"$MODEL_ID\"]}" \
  | jq -r .key)
```

The virtual key is returned once. `model_ids` is required and cannot be empty.
Clients use the Model's stable `alias`; a non-empty `upstream_model` remains an
internal routing detail and is replaced with the public alias in every gateway
response. Omit `upstream_model` (or send `""`) when the provider accepts the
same model name as the public alias. The gateway then uses the alias as the
upstream model name.

The resource control plane synchronizes minimal revisioned user and model
subjects to the virtual-key service; gateway authorization fails closed if
either projection is absent, disabled, stale or deleted. To configure another
provider, first deploy an adapter with its `kind`, `appID` and `upstream`
settings, then create a Provider whose `kind` and `adapter_app_id` match. The
Provider `slug` is GWAI metadata and does not configure or select the adapter.

## 5. Call any gateway

This OpenAI Chat request can target the Anthropic adapter because routing is
independent of the public protocol:

```bash
curl -fsS http://127.0.0.1:8080/v1/chat/completions \
  -H "Authorization: Bearer $GWAI_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "model":"claude-sonnet",
    "messages":[{"role":"user","content":"Reply with one short sentence."}],
    "max_completion_tokens":128
  }' | jq
```

The corresponding request formats and authentication headers for Responses,
Anthropic and Gemini are listed in
[protocol compatibility](protocol-compatibility.md).

## 6. Deterministic smoke test

`make e2e-k3s` verifies the WebUI login/session/CSRF boundary, filters, complete
user/provider/model/key lifecycle forms, confirmation pages, dependency
conflicts, single-use key creation and degraded views. It provisions temporary
resources and a fake Anthropic provider, sends
all four public client protocols through that one adapter, verifies user
revocation, exercises both admin services independently, scales both control
planes to zero, restarts the adapter, verifies explicit model-name rewriting
and alias fallback without response leakage, and cleans up. It never requires
a real provider key and does not print either the admin token or a one-time
virtual key.

## Upgrade adapter values

The deployment-owned connection format replaces the old `providerSlug` and
`secretNames` fields. Migrate every adapter entry before upgrading:

```yaml
# old
providerAdapters:
  - name: primary
    kind: anthropic
    providerSlug: anthropic
    appID: gwai-anthropic
    secretNames: [gwai-anthropic]

# new
providerAdapters:
  - name: primary
    kind: anthropic
    appID: gwai-anthropic
    upstream:
      baseURL: https://api.anthropic.com
      apiVersion: 2023-06-01
      secretRef:
        store: kubernetes
        name: gwai-anthropic
        key: api-key
        namespace: ""
```

There is no runtime fallback to the old fields and connection settings are no
longer read from Provider records. Preserve each deployed `appID`, make sure its
Provider has the same `adapter_app_id` and matching `kind`, and roll out the
adapter values before sending inference traffic. Remove `base_url`,
`api_version` and `secret_ref` from Provider API automation as well; the strict
JSON API rejects them as unknown fields.

This pre-release schema does not migrate either the earlier single-store
registry or virtual keys whose allowlist contains qualified model strings.
Use a fresh 0.x installation, or deliberately reset the persistent state and
recreate users, providers, models and virtual keys after upgrading. Merely
reusing an old Valkey volume is not a migration; legacy keys do not contain the
Model IDs, reference indexes and projections required for safe authorization.
