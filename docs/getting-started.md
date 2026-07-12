# Getting started on k3s

## 1. Build and deploy

```bash
make local-deploy
kubectl -n gwai wait --for=condition=Ready pod --all --timeout=120s
```

For a registry-backed cluster, set `REGISTRY` and `TAG`, push the images, and
run `make deploy REGISTRY=... TAG=...` instead of importing into local
containerd.

The default Helm values create one adapter instance:

```yaml
anthropicAdapters:
  - name: primary
    providerSlug: anthropic
    appID: gwai-anthropic
    secretNames: [gwai-anthropic]
```

Every additional provider account needs another list entry with a unique
`name`, `providerSlug`, `appID`, and appropriately restricted `secretNames`.

## 2. Create the provider secret

The default adapter can read only a Secret named `gwai-anthropic`. Do not put
provider keys in Helm values or Git.

```bash
kubectl -n gwai create secret generic gwai-anthropic \
  --from-literal=api-key="$ANTHROPIC_API_KEY"
```

Change `anthropicAdapters[].secretNames` and upgrade the release before a
provider references a different Secret.

## 3. Reach the APIs

Use two terminals:

```bash
kubectl -n gwai port-forward service/gwai-control-plane 8081:8080
kubectl -n gwai port-forward service/gwai-openai-gateway 8080:8080
```

Load the generated admin token without printing it:

```bash
GWAI_ADMIN_TOKEN=$(kubectl -n gwai get secret gwai-admin \
  -o jsonpath='{.data.admin-token}' | base64 -d)
```

## 4. Provision the provider and client key

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

The key is returned once and cannot be recovered from the control plane.

## 5. Call the gateway

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

If neither token-limit field is supplied, the selected adapter uses its
`defaultMaxOutputTokens`. A zero `maxOutputTokens` delegates the upper bound to
the upstream provider.

## 6. Deterministic smoke test

`make e2e-k3s` provisions temporary resources and a fake provider, exercises
direct state reads and provider-specific invocation, restarts the control plane
and adapter, then cleans up. It never requires or sends a real provider key.

This pre-release schema does not migrate provider records or virtual keys from
the earlier model-alias implementation; recreate them after upgrading.
