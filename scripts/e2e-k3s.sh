#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
NAMESPACE=${GWAI_NAMESPACE:-gwai}
RELEASE=${GWAI_RELEASE:-gwai}
CONTROL_PORT=${GWAI_E2E_CONTROL_PORT:-28081}
VIRTUAL_KEY_CONTROL_PORT=${GWAI_E2E_VIRTUAL_KEY_CONTROL_PORT:-28086}
OPENAI_CHAT_PORT=${GWAI_E2E_GATEWAY_PORT:-28080}
OPENAI_RESPONSES_PORT=${GWAI_E2E_RESPONSES_PORT:-28083}
ANTHROPIC_GATEWAY_PORT=${GWAI_E2E_ANTHROPIC_PORT:-28084}
GEMINI_GATEWAY_PORT=${GWAI_E2E_GEMINI_PORT:-28085}
PROVIDER_PORT=${GWAI_E2E_PROVIDER_PORT:-28082}
PROVIDER_SECRET=${GWAI_E2E_PROVIDER_SECRET:-gwai-anthropic}
PROVIDER_SLUG=${GWAI_E2E_PROVIDER_SLUG:-anthropic}
ADAPTER_APP_ID=${GWAI_E2E_ADAPTER_APP_ID:-gwai-anthropic}
ADAPTER_DEPLOYMENT=${GWAI_E2E_ADAPTER_DEPLOYMENT:-${RELEASE}-anthropic-primary}
GO_BIN=${GO:-go}
TMP_DIR=$(mktemp -d)
RUN_ID="e2e-$(date +%s)-$$"

provider_pid=""
control_forward_pid=""
virtual_key_forward_pid=""
openai_chat_forward_pid=""
openai_responses_forward_pid=""
anthropic_forward_pid=""
gemini_forward_pid=""
created_secret=false
admin_token=""
user_name=""
user_email=""
user_id=""
provider_id=""
key_id=""
independent_key_id=""
independent_provider_id=""
control_replicas=""
virtual_key_replicas=""
control_scaled_down=false
virtual_key_scaled_down=false

wait_for_url() {
  local url=$1
  for _ in $(seq 1 80); do
    curl -fsS "$url" >/dev/null 2>&1 && return 0
    sleep 0.25
  done
  echo "timed out waiting for $url" >&2
  return 1
}

process_running() {
  local pid=${1:-}
  [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null
}

start_control_forward() {
  local log_name=$1
  kubectl -n "$NAMESPACE" port-forward "service/${RELEASE}-control-plane" "${CONTROL_PORT}:8080" >"$TMP_DIR/$log_name" 2>&1 &
  control_forward_pid=$!
}

start_virtual_key_forward() {
  local log_name=$1
  kubectl -n "$NAMESPACE" port-forward "service/${RELEASE}-virtual-key-control-plane" "${VIRTUAL_KEY_CONTROL_PORT}:8080" >"$TMP_DIR/$log_name" 2>&1 &
  virtual_key_forward_pid=$!
}

stop_control_forward() {
  if process_running "$control_forward_pid"; then
    kill "$control_forward_pid" 2>/dev/null || true
    wait "$control_forward_pid" 2>/dev/null || true
  fi
  control_forward_pid=""
}

stop_virtual_key_forward() {
  if process_running "$virtual_key_forward_pid"; then
    kill "$virtual_key_forward_pid" 2>/dev/null || true
    wait "$virtual_key_forward_pid" 2>/dev/null || true
  fi
  virtual_key_forward_pid=""
}

ensure_control_forward() {
  if ! process_running "$control_forward_pid"; then
    control_forward_pid=""
    start_control_forward control-forward-recovered.log
  fi
  wait_for_url "http://127.0.0.1:${CONTROL_PORT}/readyz"
}

ensure_virtual_key_forward() {
  if ! process_running "$virtual_key_forward_pid"; then
    virtual_key_forward_pid=""
    start_virtual_key_forward virtual-key-forward-recovered.log
  fi
  wait_for_url "http://127.0.0.1:${VIRTUAL_KEY_CONTROL_PORT}/readyz"
}

scale_down_control_plane() {
  stop_control_forward
  kubectl -n "$NAMESPACE" scale "deployment/${RELEASE}-control-plane" --replicas=0 >/dev/null
  control_scaled_down=true
  kubectl -n "$NAMESPACE" wait --for=delete pod \
    -l "app.kubernetes.io/instance=${RELEASE},app.kubernetes.io/component=control-plane" \
    --timeout=60s >/dev/null
}

restore_control_plane() {
  if [[ "$control_scaled_down" == true ]]; then
    kubectl -n "$NAMESPACE" scale "deployment/${RELEASE}-control-plane" --replicas="$control_replicas" >/dev/null
    kubectl -n "$NAMESPACE" rollout status "deployment/${RELEASE}-control-plane" --timeout=60s >/dev/null
    control_scaled_down=false
  fi
}

scale_down_virtual_key_control_plane() {
  stop_virtual_key_forward
  kubectl -n "$NAMESPACE" scale "deployment/${RELEASE}-virtual-key-control-plane" --replicas=0 >/dev/null
  virtual_key_scaled_down=true
  kubectl -n "$NAMESPACE" wait --for=delete pod \
    -l "app.kubernetes.io/instance=${RELEASE},app.kubernetes.io/component=virtual-key-control-plane" \
    --timeout=60s >/dev/null
}

restore_virtual_key_control_plane() {
  if [[ "$virtual_key_scaled_down" == true ]]; then
    kubectl -n "$NAMESPACE" scale "deployment/${RELEASE}-virtual-key-control-plane" --replicas="$virtual_key_replicas" >/dev/null
    kubectl -n "$NAMESPACE" rollout status "deployment/${RELEASE}-virtual-key-control-plane" --timeout=60s >/dev/null
    virtual_key_scaled_down=false
  fi
}

cleanup() {
  set +e
  restore_virtual_key_control_plane
  restore_control_plane

  if [[ -n "$admin_token" ]]; then
    ensure_virtual_key_forward
    ensure_control_forward
    if [[ -z "$user_id" && -n "$user_email" ]]; then
      user_id=$(curl -fsS "http://127.0.0.1:${CONTROL_PORT}/v1/users" -H "Authorization: Bearer ${admin_token}" | jq -r --arg email "$user_email" '.data[] | select(.email == $email) | .id' | head -n 1)
    fi
    [[ -n "$independent_key_id" ]] && curl -fsS -X DELETE "http://127.0.0.1:${VIRTUAL_KEY_CONTROL_PORT}/v1/virtual-keys/${independent_key_id}" -H "Authorization: Bearer ${admin_token}" >/dev/null
    [[ -n "$key_id" ]] && curl -fsS -X DELETE "http://127.0.0.1:${VIRTUAL_KEY_CONTROL_PORT}/v1/virtual-keys/${key_id}" -H "Authorization: Bearer ${admin_token}" >/dev/null
    [[ -n "$independent_provider_id" ]] && curl -fsS -X DELETE "http://127.0.0.1:${CONTROL_PORT}/v1/providers/${independent_provider_id}" -H "Authorization: Bearer ${admin_token}" >/dev/null
    [[ -n "$provider_id" ]] && curl -fsS -X DELETE "http://127.0.0.1:${CONTROL_PORT}/v1/providers/${provider_id}" -H "Authorization: Bearer ${admin_token}" >/dev/null
    [[ -n "$user_id" ]] && curl -fsS -X DELETE "http://127.0.0.1:${CONTROL_PORT}/v1/users/${user_id}" -H "Authorization: Bearer ${admin_token}" >/dev/null
  fi

  for pid in "$control_forward_pid" "$virtual_key_forward_pid" "$openai_chat_forward_pid" "$openai_responses_forward_pid" "$anthropic_forward_pid" "$gemini_forward_pid" "$provider_pid"; do
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
kubectl -n "$NAMESPACE" get deployment "${RELEASE}-virtual-key-control-plane" >/dev/null
kubectl -n "$NAMESPACE" get deployment "${RELEASE}-openai-gateway" >/dev/null
kubectl -n "$NAMESPACE" get deployment "${RELEASE}-openai-responses-gateway" >/dev/null
kubectl -n "$NAMESPACE" get deployment "${RELEASE}-anthropic-gateway" >/dev/null
kubectl -n "$NAMESPACE" get deployment "${RELEASE}-gemini-gateway" >/dev/null
kubectl -n "$NAMESPACE" get deployment "$ADAPTER_DEPLOYMENT" >/dev/null

for component_and_db in \
  "gwai-control-state:0" \
  "gwai-provider-state:1" \
  "gwai-virtual-key-state:2"; do
  component=${component_and_db%%:*}
  expected_db=${component_and_db##*:}
  actual_db=$(kubectl -n "$NAMESPACE" get component.dapr.io "$component" -o jsonpath='{.spec.metadata[?(@.name=="redisDB")].value}')
  [[ "$actual_db" == "$expected_db" ]] || { echo "$component uses Redis DB $actual_db instead of $expected_db" >&2; exit 1; }
done

control_scopes=$(kubectl -n "$NAMESPACE" get component.dapr.io gwai-control-state -o jsonpath='{.scopes[*]}')
provider_scopes=$(kubectl -n "$NAMESPACE" get component.dapr.io gwai-provider-state -o jsonpath='{.scopes[*]}')
key_scopes=$(kubectl -n "$NAMESPACE" get component.dapr.io gwai-virtual-key-state -o jsonpath='{.scopes[*]}')
[[ " $control_scopes " == *" ${RELEASE}-control-plane "* ]]
[[ " $control_scopes " != *" ${RELEASE}-openai-gateway "* ]]
[[ " $provider_scopes " == *" ${RELEASE}-control-plane "* && " $provider_scopes " == *" ${RELEASE}-virtual-key-control-plane "* && " $provider_scopes " == *" ${ADAPTER_APP_ID} "* ]]
[[ " $key_scopes " == *" ${RELEASE}-virtual-key-control-plane "* && " $key_scopes " == *" ${RELEASE}-openai-gateway "* ]]
[[ " $key_scopes " != *" ${RELEASE}-control-plane "* && " $key_scopes " != *" ${ADAPTER_APP_ID} "* ]]

control_replicas=$(kubectl -n "$NAMESPACE" get "deployment/${RELEASE}-control-plane" -o jsonpath='{.spec.replicas}')
virtual_key_replicas=$(kubectl -n "$NAMESPACE" get "deployment/${RELEASE}-virtual-key-control-plane" -o jsonpath='{.spec.replicas}')

if ! kubectl -n "$NAMESPACE" get secret "$PROVIDER_SECRET" >/dev/null 2>&1; then
  kubectl -n "$NAMESPACE" create secret generic "$PROVIDER_SECRET" --from-literal=api-key=e2e-secret >/dev/null
  created_secret=true
fi

(cd "$ROOT_DIR" && "$GO_BIN" run ./test/e2e/fakeprovider -listen "0.0.0.0:${PROVIDER_PORT}") >"$TMP_DIR/provider.log" 2>&1 &
provider_pid=$!
start_control_forward control-forward.log
start_virtual_key_forward virtual-key-forward.log
kubectl -n "$NAMESPACE" port-forward "service/${RELEASE}-openai-gateway" "${OPENAI_CHAT_PORT}:8080" >"$TMP_DIR/openai-chat-forward.log" 2>&1 &
openai_chat_forward_pid=$!
kubectl -n "$NAMESPACE" port-forward "service/${RELEASE}-openai-responses-gateway" "${OPENAI_RESPONSES_PORT}:8080" >"$TMP_DIR/openai-responses-forward.log" 2>&1 &
openai_responses_forward_pid=$!
kubectl -n "$NAMESPACE" port-forward "service/${RELEASE}-anthropic-gateway" "${ANTHROPIC_GATEWAY_PORT}:8080" >"$TMP_DIR/anthropic-forward.log" 2>&1 &
anthropic_forward_pid=$!
kubectl -n "$NAMESPACE" port-forward "service/${RELEASE}-gemini-gateway" "${GEMINI_GATEWAY_PORT}:8080" >"$TMP_DIR/gemini-forward.log" 2>&1 &
gemini_forward_pid=$!

wait_for_url "http://127.0.0.1:${PROVIDER_PORT}/healthz"
wait_for_url "http://127.0.0.1:${CONTROL_PORT}/readyz"
wait_for_url "http://127.0.0.1:${VIRTUAL_KEY_CONTROL_PORT}/readyz"
wait_for_url "http://127.0.0.1:${OPENAI_CHAT_PORT}/readyz"
wait_for_url "http://127.0.0.1:${OPENAI_RESPONSES_PORT}/readyz"
wait_for_url "http://127.0.0.1:${ANTHROPIC_GATEWAY_PORT}/readyz"
wait_for_url "http://127.0.0.1:${GEMINI_GATEWAY_PORT}/readyz"

admin_token=$(kubectl -n "$NAMESPACE" get secret "${RELEASE}-admin" -o jsonpath='{.data.admin-token}' | base64 -d)
node_ip=$(kubectl get nodes -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}')

# The public admin API is split: each service must reject the other domain.
status=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${CONTROL_PORT}/v1/virtual-keys" -H "Authorization: Bearer ${admin_token}")
[[ "$status" == 404 ]] || { echo "resource control plane unexpectedly exposes virtual keys ($status)" >&2; exit 1; }
status=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${VIRTUAL_KEY_CONTROL_PORT}/v1/users" -H "Authorization: Bearer ${admin_token}")
[[ "$status" == 404 ]] || { echo "virtual-key control plane unexpectedly exposes users ($status)" >&2; exit 1; }
status=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:${VIRTUAL_KEY_CONTROL_PORT}/internal/v1/subjects/sync" -H 'Content-Type: application/json' -d '{}')
[[ "$status" == 401 ]] || { echo "subject sync is reachable without the Dapr app token ($status)" >&2; exit 1; }

user_name="E2E ${RUN_ID}"
user_email="${RUN_ID}@example.com"
user=$(curl -fsS "http://127.0.0.1:${CONTROL_PORT}/v1/users" \
  -H "Authorization: Bearer ${admin_token}" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg name "$user_name" --arg email "$user_email" '{name:$name,email:$email}')")
user_id=$(jq -er .id <<<"$user")

provider=$(curl -fsS "http://127.0.0.1:${CONTROL_PORT}/v1/providers" \
  -H "Authorization: Bearer ${admin_token}" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg name "E2E ${RUN_ID}" --arg slug "$PROVIDER_SLUG" --arg app "$ADAPTER_APP_ID" --arg base "http://${node_ip}:${PROVIDER_PORT}" --arg secret "$PROVIDER_SECRET" '{slug:$slug,name:$name,kind:"anthropic",adapter_app_id:$app,base_url:$base,secret_ref:{store:"kubernetes",name:$secret,key:"api-key"}}')")
provider_id=$(jq -er .id <<<"$provider")

qualified_model="${PROVIDER_SLUG}/claude-e2e"

created_key=$(curl -fsS "http://127.0.0.1:${VIRTUAL_KEY_CONTROL_PORT}/v1/virtual-keys" \
  -H "Authorization: Bearer ${admin_token}" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg user "$user_id" --arg model "$qualified_model" '{name:"E2E key",user_id:$user,allowed_models:[$model]}')")
key_id=$(jq -er .virtual_key.id <<<"$created_key")
virtual_key=$(jq -er .key <<<"$created_key")

# Deletion crosses the service boundary: the remote fence must map its nonempty
# per-user key index to the public conflict contract.
status=$(curl -sS -o /dev/null -w '%{http_code}' -X DELETE "http://127.0.0.1:${CONTROL_PORT}/v1/users/${user_id}" -H "Authorization: Bearer ${admin_token}")
[[ "$status" == 409 ]] || { echo "user deletion with a live virtual key did not conflict ($status)" >&2; exit 1; }

call_openai_chat() {
  curl -fsS "http://127.0.0.1:${OPENAI_CHAT_PORT}/v1/chat/completions" \
    -H "Authorization: Bearer ${virtual_key}" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg model "$qualified_model" '{model:$model,messages:[{role:"system",content:"Be concise"},{role:"user",content:"Say ok"}],max_completion_tokens:32}')"
}

call_openai_responses() {
  curl -fsS "http://127.0.0.1:${OPENAI_RESPONSES_PORT}/v1/responses" \
    -H "Authorization: Bearer ${virtual_key}" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg model "$qualified_model" '{model:$model,input:"Say ok",max_output_tokens:32,store:false}')"
}

call_anthropic() {
  curl -fsS "http://127.0.0.1:${ANTHROPIC_GATEWAY_PORT}/v1/messages" \
    -H "x-api-key: ${virtual_key}" -H 'anthropic-version: 2023-06-01' -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg model "$qualified_model" '{model:$model,max_tokens:32,system:"Be concise",messages:[{role:"user",content:"Say ok"}]}')"
}

call_gemini() {
  curl -fsS "http://127.0.0.1:${GEMINI_GATEWAY_PORT}/v1beta/models/${qualified_model}:generateContent" \
    -H "x-goog-api-key: ${virtual_key}" -H 'Content-Type: application/json' \
    -d '{"contents":[{"role":"user","parts":[{"text":"Say ok"}]}],"generationConfig":{"maxOutputTokens":32}}'
}

assert_all_gateways() {
  local completion response
  completion=$(call_openai_chat)
  jq -e '.object == "chat.completion" and .choices[0].message.content == "gwai e2e ok" and .usage.total_tokens == 11' <<<"$completion" >/dev/null
  response=$(call_openai_responses)
  jq -e '.object == "response" and .output[0].content[0].text == "gwai e2e ok" and .usage.total_tokens == 11' <<<"$response" >/dev/null
  response=$(call_anthropic)
  jq -e '.type == "message" and .content[0].text == "gwai e2e ok" and (.usage.input_tokens + .usage.output_tokens) == 11' <<<"$response" >/dev/null
  response=$(call_gemini)
  jq -e '.candidates[0].content.parts[0].text == "gwai e2e ok" and .usageMetadata.totalTokenCount == 11' <<<"$response" >/dev/null
}

assert_all_gateways_unauthorized() {
  local status
  status=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${OPENAI_CHAT_PORT}/v1/chat/completions" \
    -H "Authorization: Bearer ${virtual_key}" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg model "$qualified_model" '{model:$model,messages:[{role:"user",content:"Say ok"}]}')")
  [[ "$status" == 401 ]] || { echo "OpenAI Chat accepted a revoked user ($status)" >&2; return 1; }
  status=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${OPENAI_RESPONSES_PORT}/v1/responses" \
    -H "Authorization: Bearer ${virtual_key}" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg model "$qualified_model" '{model:$model,input:"Say ok",store:false}')")
  [[ "$status" == 401 ]] || { echo "OpenAI Responses accepted a revoked user ($status)" >&2; return 1; }
  status=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${ANTHROPIC_GATEWAY_PORT}/v1/messages" \
    -H "x-api-key: ${virtual_key}" -H 'anthropic-version: 2023-06-01' -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg model "$qualified_model" '{model:$model,max_tokens:32,messages:[{role:"user",content:"Say ok"}]}')")
  [[ "$status" == 401 ]] || { echo "Anthropic gateway accepted a revoked user ($status)" >&2; return 1; }
  status=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${GEMINI_GATEWAY_PORT}/v1beta/models/${qualified_model}:generateContent" \
    -H "x-goog-api-key: ${virtual_key}" -H 'Content-Type: application/json' \
    -d '{"contents":[{"role":"user","parts":[{"text":"Say ok"}]}]}' )
  [[ "$status" == 401 ]] || { echo "Gemini gateway accepted a revoked user ($status)" >&2; return 1; }
}

set_user_status() {
  local new_status=$1
  curl -fsS -X PUT "http://127.0.0.1:${CONTROL_PORT}/v1/users/${user_id}" \
    -H "Authorization: Bearer ${admin_token}" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg name "$user_name" --arg email "$user_email" --arg status "$new_status" '{name:$name,email:$email,status:$status}')"
}

assert_all_gateways

# A user status revision is synchronized into the virtual-key domain. Gateways
# read that projection and fail closed without consulting either control plane.
disabled_user=$(set_user_status disabled)
jq -e '.status == "disabled" and .revision >= 2' <<<"$disabled_user" >/dev/null
assert_all_gateways_unauthorized
enabled_user=$(set_user_status active)
jq -e '.status == "active" and .revision >= 3' <<<"$enabled_user" >/dev/null
assert_all_gateways

# The virtual-key service remains independently useful while the resource
# control plane is down: it can manage a key for an already synchronized user.
scale_down_control_plane
independent_key=$(curl -fsS "http://127.0.0.1:${VIRTUAL_KEY_CONTROL_PORT}/v1/virtual-keys" \
  -H "Authorization: Bearer ${admin_token}" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg user "$user_id" --arg model "$qualified_model" '{name:"independence probe",user_id:$user,allowed_models:[$model]}')")
independent_key_id=$(jq -er .virtual_key.id <<<"$independent_key")
curl -fsS -X DELETE "http://127.0.0.1:${VIRTUAL_KEY_CONTROL_PORT}/v1/virtual-keys/${independent_key_id}" -H "Authorization: Bearer ${admin_token}" >/dev/null
independent_key_id=""
assert_all_gateways
restore_control_plane
ensure_control_forward

# Provider lifecycle remains available from the resource control plane while
# the virtual-key service is down. User writes intentionally need subject sync.
scale_down_virtual_key_control_plane
probe_slug="probe-${RUN_ID}"
independent_provider=$(curl -fsS "http://127.0.0.1:${CONTROL_PORT}/v1/providers" \
  -H "Authorization: Bearer ${admin_token}" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg name "independence ${RUN_ID}" --arg slug "$probe_slug" --arg app "gwai-${probe_slug}" --arg base "http://${node_ip}:${PROVIDER_PORT}" --arg secret "$PROVIDER_SECRET" '{slug:$slug,name:$name,kind:"anthropic",adapter_app_id:$app,base_url:$base,secret_ref:{store:"kubernetes",name:$secret,key:"api-key"}}')")
independent_provider_id=$(jq -er .id <<<"$independent_provider")
curl -fsS "http://127.0.0.1:${CONTROL_PORT}/v1/users/${user_id}" -H "Authorization: Bearer ${admin_token}" | jq -e --arg id "$user_id" '.id == $id' >/dev/null
curl -fsS -X DELETE "http://127.0.0.1:${CONTROL_PORT}/v1/providers/${independent_provider_id}" -H "Authorization: Bearer ${admin_token}" >/dev/null
independent_provider_id=""
assert_all_gateways
restore_virtual_key_control_plane
ensure_virtual_key_forward

# Inference must remain available with both administrative services absent.
# Gateways read key/subject plus provider state; adapters read provider state.
scale_down_control_plane
scale_down_virtual_key_control_plane
assert_all_gateways

restore_virtual_key_control_plane
restore_control_plane
ensure_virtual_key_forward
ensure_control_forward

# Adapter restart verifies provider-specific Dapr discovery after endpoint rotation.
kubectl -n "$NAMESPACE" rollout restart "deployment/${ADAPTER_DEPLOYMENT}" >/dev/null
kubectl -n "$NAMESPACE" rollout status "deployment/${ADAPTER_DEPLOYMENT}" --timeout=60s >/dev/null
assert_all_gateways

echo "k3s e2e passed: split control planes, fail-closed revocation, three state domains, four gateway protocols, control-plane outage, provider invocation, secrets, and IR translation"
