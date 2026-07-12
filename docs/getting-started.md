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
kubectl -n gwai port-forward service/gwai-openai-gateway 8080:8080
# alternatives: gwai-openai-responses-gateway, gwai-anthropic-gateway,
#               gwai-gemini-gateway
```

Load the generated admin token without printing it:

```bash
GWAI_ADMIN_TOKEN=$(kubectl -n gwai get secret gwai-admin \
  -o jsonpath='{.data.admin-token}' | base64 -d)
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

GWAI_API_KEY=$(curl -fsS http://127.0.0.1:8081/v1/virtual-keys \
  -H "Authorization: Bearer $GWAI_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"local\",\"user_id\":\"$USER_ID\",\"allowed_models\":[\"anthropic/claude-sonnet-4-6\"]}" \
  | jq -r .key)
```

The virtual key is returned once. To configure another provider, use the same
`providerSlug`, `kind` and `appID` as its Helm entry. Omitted endpoints and API
versions receive the defaults in [the control-plane API](control-plane-api.md).

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

`make e2e-k3s` provisions temporary resources and a fake Anthropic provider,
sends all four public client protocols through that one adapter, scales the
control plane to zero, restarts the adapter, and cleans up. It never requires a
real provider key.

This pre-release schema does not migrate earlier provider records, virtual keys
or IR payloads; recreate them after upgrading.
