#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
NAMESPACE=${GWAI_NAMESPACE:-gwai}
RELEASE=${GWAI_RELEASE:-gwai}
CONTROL_PORT=${GWAI_E2E_CONTROL_PORT:-28081}
GATEWAY_PORT=${GWAI_E2E_GATEWAY_PORT:-28080}
PROVIDER_PORT=${GWAI_E2E_PROVIDER_PORT:-28082}
PROVIDER_SECRET=${GWAI_E2E_PROVIDER_SECRET:-gwai-anthropic}
GO_BIN=${GO:-go}
TMP_DIR=$(mktemp -d)
RUN_ID="e2e-$(date +%s)-$$"

provider_pid=""
control_forward_pid=""
gateway_forward_pid=""
created_secret=false
admin_token=""
user_id=""
provider_id=""
model_id=""
key_id=""

cleanup() {
  set +e
  if [[ -n "$admin_token" && -n "$control_forward_pid" ]]; then
    [[ -n "$key_id" ]] && curl -fsS -X DELETE "http://127.0.0.1:${CONTROL_PORT}/v1/virtual-keys/${key_id}" -H "Authorization: Bearer ${admin_token}" >/dev/null
    [[ -n "$model_id" ]] && curl -fsS -X DELETE "http://127.0.0.1:${CONTROL_PORT}/v1/models/${model_id}" -H "Authorization: Bearer ${admin_token}" >/dev/null
    [[ -n "$provider_id" ]] && curl -fsS -X DELETE "http://127.0.0.1:${CONTROL_PORT}/v1/providers/${provider_id}" -H "Authorization: Bearer ${admin_token}" >/dev/null
    [[ -n "$user_id" ]] && curl -fsS -X DELETE "http://127.0.0.1:${CONTROL_PORT}/v1/users/${user_id}" -H "Authorization: Bearer ${admin_token}" >/dev/null
  fi
  for pid in "$control_forward_pid" "$gateway_forward_pid" "$provider_pid"; do
    [[ -n "$pid" ]] && kill "$pid" 2>/dev/null
  done
  wait 2>/dev/null
  if [[ "$created_secret" == true ]]; then
    kubectl -n "$NAMESPACE" delete secret "$PROVIDER_SECRET" --ignore-not-found >/dev/null
  fi
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

for command in "$GO_BIN" curl jq kubectl; do
  command -v "$command" >/dev/null || { echo "required command not found: $command" >&2; exit 1; }
done

kubectl -n "$NAMESPACE" get deployment "${RELEASE}-control-plane" >/dev/null
kubectl -n "$NAMESPACE" get deployment "${RELEASE}-openai-gateway" >/dev/null
kubectl -n "$NAMESPACE" get deployment "${RELEASE}-anthropic-adapter" >/dev/null

if ! kubectl -n "$NAMESPACE" get secret "$PROVIDER_SECRET" >/dev/null 2>&1; then
  kubectl -n "$NAMESPACE" create secret generic "$PROVIDER_SECRET" --from-literal=api-key=e2e-secret >/dev/null
  created_secret=true
fi

(cd "$ROOT_DIR" && "$GO_BIN" run ./test/e2e/fakeprovider -listen "0.0.0.0:${PROVIDER_PORT}") >"$TMP_DIR/provider.log" 2>&1 &
provider_pid=$!
kubectl -n "$NAMESPACE" port-forward "service/${RELEASE}-control-plane" "${CONTROL_PORT}:8080" >"$TMP_DIR/control-forward.log" 2>&1 &
control_forward_pid=$!
kubectl -n "$NAMESPACE" port-forward "service/${RELEASE}-openai-gateway" "${GATEWAY_PORT}:8080" >"$TMP_DIR/gateway-forward.log" 2>&1 &
gateway_forward_pid=$!

wait_for_url() {
  local url=$1
  for _ in $(seq 1 80); do
    curl -fsS "$url" >/dev/null 2>&1 && return 0
    sleep 0.25
  done
  echo "timed out waiting for $url" >&2
  return 1
}

wait_for_url "http://127.0.0.1:${PROVIDER_PORT}/healthz"
wait_for_url "http://127.0.0.1:${CONTROL_PORT}/readyz"
wait_for_url "http://127.0.0.1:${GATEWAY_PORT}/readyz"

admin_token=$(kubectl -n "$NAMESPACE" get secret "${RELEASE}-admin" -o jsonpath='{.data.admin-token}' | base64 -d)
node_ip=$(kubectl get nodes -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}')

unauthorized_status=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${CONTROL_PORT}/internal/v1/routes/resolve" -H 'Content-Type: application/json' -d '{"alias":"none"}')
[[ "$unauthorized_status" == 401 ]] || { echo "internal control-plane endpoint was not protected" >&2; exit 1; }

user=$(curl -fsS "http://127.0.0.1:${CONTROL_PORT}/v1/users" \
  -H "Authorization: Bearer ${admin_token}" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg id "$RUN_ID" '{name:("E2E " + $id),email:($id + "@example.com")}')")
user_id=$(jq -er .id <<<"$user")

provider=$(curl -fsS "http://127.0.0.1:${CONTROL_PORT}/v1/providers" \
  -H "Authorization: Bearer ${admin_token}" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg id "$RUN_ID" --arg base "http://${node_ip}:${PROVIDER_PORT}" --arg secret "$PROVIDER_SECRET" '{name:("E2E " + $id),kind:"anthropic",base_url:$base,secret_ref:{store:"kubernetes",name:$secret,key:"api-key"}}')")
provider_id=$(jq -er .id <<<"$provider")

model_alias="${RUN_ID}-model"
model=$(curl -fsS "http://127.0.0.1:${CONTROL_PORT}/v1/models" \
  -H "Authorization: Bearer ${admin_token}" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg alias "$model_alias" --arg provider "$provider_id" '{alias:$alias,provider_id:$provider,upstream_model:"claude-e2e",max_output_tokens:1024}')")
model_id=$(jq -er .id <<<"$model")

created_key=$(curl -fsS "http://127.0.0.1:${CONTROL_PORT}/v1/virtual-keys" \
  -H "Authorization: Bearer ${admin_token}" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg user "$user_id" --arg model "$model_alias" '{name:"E2E key",user_id:$user,allowed_models:[$model]}')")
key_id=$(jq -er .virtual_key.id <<<"$created_key")
virtual_key=$(jq -er .key <<<"$created_key")

call_gateway() {
  curl -fsS "http://127.0.0.1:${GATEWAY_PORT}/v1/chat/completions" \
    -H "Authorization: Bearer ${virtual_key}" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg model "$model_alias" '{model:$model,messages:[{role:"system",content:"Be concise"},{role:"user",content:"Say ok"}],max_completion_tokens:32}')"
}

completion=$(call_gateway)
jq -e '.object == "chat.completion" and .choices[0].message.content == "gwai e2e ok" and .usage.total_tokens == 11' <<<"$completion" >/dev/null

# A rolling restart proves that the Dapr state store preserves the virtual key
# and that service invocation remains usable after endpoint rotation.
kubectl -n "$NAMESPACE" rollout restart "deployment/${RELEASE}-control-plane" >/dev/null
kubectl -n "$NAMESPACE" rollout status "deployment/${RELEASE}-control-plane" --timeout=60s >/dev/null
completion=$(call_gateway)
jq -e '.choices[0].message.content == "gwai e2e ok"' <<<"$completion" >/dev/null

kill "$control_forward_pid" 2>/dev/null || true
wait "$control_forward_pid" 2>/dev/null || true
kubectl -n "$NAMESPACE" port-forward "service/${RELEASE}-control-plane" "${CONTROL_PORT}:8080" >"$TMP_DIR/control-forward-after-restart.log" 2>&1 &
control_forward_pid=$!
wait_for_url "http://127.0.0.1:${CONTROL_PORT}/readyz"

echo "k3s e2e passed: state, invocation, secret resolution, restart, and OpenAI/Anthropic translation"
