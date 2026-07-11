# Getting started on k3s

## 1. Build and deploy

```bash
make local-deploy
kubectl -n gwai wait --for=condition=Ready pod --all --timeout=120s
```

For a registry-backed cluster, set `REGISTRY` and `TAG`, push the images, and
run `make deploy REGISTRY=... TAG=...` instead of importing into local
containerd.

## 2. Create the provider secret

The default chart permits the Anthropic adapter to read only a Secret named
`gwai-anthropic`. Do not put provider keys in Helm values or Git.

```bash
kubectl -n gwai create secret generic gwai-anthropic \
  --from-literal=api-key="$ANTHROPIC_API_KEY"
```

To use another Secret name, add it to `anthropicAdapter.secretNames` during the
Helm upgrade before creating a provider that references it.

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

## 4. Provision a route

```bash
USER_ID=$(curl -fsS http://127.0.0.1:8081/v1/users \
  -H "Authorization: Bearer $GWAI_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Local user","email":"local@example.com"}' | jq -r .id)

PROVIDER_ID=$(curl -fsS http://127.0.0.1:8081/v1/providers \
  -H "Authorization: Bearer $GWAI_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "name":"Anthropic",
    "kind":"anthropic",
    "secret_ref":{"store":"kubernetes","name":"gwai-anthropic","key":"api-key"}
  }' | jq -r .id)

curl -fsS http://127.0.0.1:8081/v1/models \
  -H "Authorization: Bearer $GWAI_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{
    \"alias\":\"claude\",
    \"provider_id\":\"$PROVIDER_ID\",
    \"upstream_model\":\"claude-sonnet-4-6\",
    \"max_output_tokens\":8192
  }" | jq

GWAI_API_KEY=$(curl -fsS http://127.0.0.1:8081/v1/virtual-keys \
  -H "Authorization: Bearer $GWAI_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"local\",\"user_id\":\"$USER_ID\",\"allowed_models\":[\"claude\"]}" \
  | jq -r .key)
```

The last value is returned once. Store it in a secret manager; it cannot be
recovered from the control plane.

## 5. Call the gateway

```bash
curl -fsS http://127.0.0.1:8080/v1/chat/completions \
  -H "Authorization: Bearer $GWAI_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "model":"claude",
    "messages":[{"role":"user","content":"Reply with one short sentence."}],
    "max_completion_tokens":128
  }' | jq
```

## 6. Deterministic smoke test

`make e2e-k3s` provisions temporary resources and a fake provider, exercises
the complete path, restarts the control plane to prove persistence, then cleans
up. It never requires or sends a real provider key.
