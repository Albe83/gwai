# Getting started on k3s

## 1. Build and deploy

```bash
make local-deploy
kubectl -n gwai wait --for=condition=Ready pod --all --timeout=180s
```

For a registry-backed cluster, set `REGISTRY` and `TAG`, push the images, then
run `make deploy REGISTRY=... TAG=...`.

The default values expose all four client protocols and one Anthropic provider:

```yaml
gateways:
  - name: openai-gateway
    image: {repository: gwai-openai-gateway}
    # openai-responses-gateway, anthropic-gateway and gemini-gateway follow

providerAdapters:
  - name: primary
    kind: anthropic
    providerSlug: anthropic
    appID: gwai-anthropic
    image: {repository: gwai-anthropic-adapter}
    secretNames: [gwai-anthropic]
```

Add one `providerAdapters` entry per provider account. `name`, `providerSlug`
and `appID` must be unique. Select the image matching `kind`:

| `kind` | image repository |
| --- | --- |
| `anthropic` | `gwai-anthropic-adapter` |
| `openai-chat` | `gwai-openai-chat-adapter` |
| `openai-responses` | `gwai-openai-responses-adapter` |
| `gemini` | `gwai-gemini-adapter` |

Optional `defaultMaxOutputTokens` and `maxOutputTokens` values are passed to
adapters that enforce local token policy.

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

Change the matching `providerAdapters[].secretNames` and upgrade the release
before referencing another Secret.

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
the login form, and then manage users, providers and virtual keys from the
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

## 4. Provision a provider and client key

```bash
USER_ID=$(curl -fsS http://127.0.0.1:8081/v1/users \
  -H "Authorization: Bearer $GWAI_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Local user","email":"local@example.com"}' | jq -r .id)

curl -fsS http://127.0.0.1:8081/v1/providers \
  -H "Authorization: Bearer $GWAI_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "slug":"anthropic",
    "name":"Anthropic primary",
    "kind":"anthropic",
    "adapter_app_id":"gwai-anthropic",
    "secret_ref":{"store":"kubernetes","name":"gwai-anthropic","key":"api-key"}
  }' | jq

GWAI_API_KEY=$(curl -fsS http://127.0.0.1:8082/v1/virtual-keys \
  -H "Authorization: Bearer $GWAI_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"local\",\"user_id\":\"$USER_ID\",\"allowed_models\":[\"anthropic/claude-sonnet-4-6\"]}" \
  | jq -r .key)
```

The virtual key is returned once. The resource control plane synchronizes a
minimal revisioned user subject to the virtual-key service; gateway
authorization fails closed if that projection is absent, disabled or deleted.
To configure another provider, use the same `providerSlug`, `kind` and `appID`
as its Helm entry. Omitted endpoints and API versions receive the defaults in
[the control-plane API](control-plane-api.md).

## 5. Call any gateway

This OpenAI Chat request can target the Anthropic adapter because routing is
independent of the public protocol:

```bash
curl -fsS http://127.0.0.1:8080/v1/chat/completions \
  -H "Authorization: Bearer $GWAI_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "model":"anthropic/claude-sonnet-4-6",
    "messages":[{"role":"user","content":"Reply with one short sentence."}],
    "max_completion_tokens":128
  }' | jq
```

The corresponding request formats and authentication headers for Responses,
Anthropic and Gemini are listed in
[protocol compatibility](protocol-compatibility.md).

## 6. Deterministic smoke test

`make e2e-k3s` verifies the WebUI login/session/CSRF boundary, filters, complete
lifecycle forms, confirmation pages, single-use key creation and degraded
views. It provisions temporary resources and a fake Anthropic provider, sends
all four public client protocols through that one adapter, verifies user
revocation, exercises both admin services independently, scales both control
planes to zero, restarts the adapter, and cleans up. It never requires a real
provider key and does not print either the admin token or a one-time virtual
key.

This pre-release schema does not migrate the earlier single-store registry or
IR payloads. Use a fresh 0.x installation, or deliberately reset the persistent
state and recreate users, providers and virtual keys after upgrading. Merely
reusing a volume containing `gwai-state` is not a migration.
