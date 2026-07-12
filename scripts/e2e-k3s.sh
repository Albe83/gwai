#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
NAMESPACE=${GWAI_NAMESPACE:-gwai}
RELEASE=${GWAI_RELEASE:-gwai}
CONTROL_PORT=${GWAI_E2E_CONTROL_PORT:-28081}
VIRTUAL_KEY_CONTROL_PORT=${GWAI_E2E_VIRTUAL_KEY_CONTROL_PORT:-28086}
ADMIN_WEBUI_PORT=${GWAI_E2E_ADMIN_WEBUI_PORT:-28087}
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
admin_webui_forward_pid=""
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
model_id=""
model_alias=""
key_id=""
independent_key_id=""
independent_key_name=""
independent_provider_id=""
independent_provider_slug=""
ui_user_id=""
ui_provider_id=""
ui_model_id=""
ui_key_id=""
ui_user_email=""
ui_provider_slug=""
ui_model_alias=""
ui_key_name=""
ui_key_name_updated=""
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

hidden_input_value() {
  local file=$1
  local name=$2
  sed -n -E "s/.*name=\"${name}\" value=\"([^\"]*)\".*/\1/p" "$file" \
    | head -n 1 \
    | sed 's/&#34;/"/g; s/&quot;/"/g'
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
    if [[ -z "$ui_key_id" && -n "$ui_key_name" ]]; then
      ui_key_id=$(curl -fsS "http://127.0.0.1:${VIRTUAL_KEY_CONTROL_PORT}/v1/virtual-keys" -H "Authorization: Bearer ${admin_token}" \
        | jq -r --arg name "$ui_key_name" --arg updated "$ui_key_name_updated" '.data | map(select(.name == $name or .name == $updated)) | first | .id // empty')
    fi
    if [[ -z "$independent_key_id" && -n "$independent_key_name" ]]; then
      independent_key_id=$(curl -fsS "http://127.0.0.1:${VIRTUAL_KEY_CONTROL_PORT}/v1/virtual-keys" -H "Authorization: Bearer ${admin_token}" \
        | jq -r --arg name "$independent_key_name" '.data | map(select(.name == $name)) | first | .id // empty')
    fi
    if [[ -z "$ui_provider_id" && -n "$ui_provider_slug" ]]; then
      ui_provider_id=$(curl -fsS "http://127.0.0.1:${CONTROL_PORT}/v1/providers" -H "Authorization: Bearer ${admin_token}" \
        | jq -r --arg slug "$ui_provider_slug" '.data | map(select(.slug == $slug)) | first | .id // empty')
    fi
    if [[ -z "$ui_model_id" && -n "$ui_model_alias" ]]; then
      ui_model_id=$(curl -fsS "http://127.0.0.1:${CONTROL_PORT}/v1/models" -H "Authorization: Bearer ${admin_token}" \
        | jq -r --arg alias "$ui_model_alias" '.data | map(select(.alias == $alias)) | first | .id // empty')
    fi
    if [[ -z "$model_id" && -n "$model_alias" ]]; then
      model_id=$(curl -fsS "http://127.0.0.1:${CONTROL_PORT}/v1/models" -H "Authorization: Bearer ${admin_token}" \
        | jq -r --arg alias "$model_alias" '.data | map(select(.alias == $alias)) | first | .id // empty')
    fi
    if [[ -z "$independent_provider_id" && -n "$independent_provider_slug" ]]; then
      independent_provider_id=$(curl -fsS "http://127.0.0.1:${CONTROL_PORT}/v1/providers" -H "Authorization: Bearer ${admin_token}" \
        | jq -r --arg slug "$independent_provider_slug" '.data | map(select(.slug == $slug)) | first | .id // empty')
    fi
    if [[ -z "$ui_user_id" && -n "$ui_user_email" ]]; then
      ui_user_id=$(curl -fsS "http://127.0.0.1:${CONTROL_PORT}/v1/users" -H "Authorization: Bearer ${admin_token}" \
        | jq -r --arg email "$ui_user_email" '.data | map(select(.email == $email)) | first | .id // empty')
    fi
    [[ -n "$ui_key_id" ]] && curl -fsS -X DELETE "http://127.0.0.1:${VIRTUAL_KEY_CONTROL_PORT}/v1/virtual-keys/${ui_key_id}" -H "Authorization: Bearer ${admin_token}" >/dev/null
    [[ -n "$independent_key_id" ]] && curl -fsS -X DELETE "http://127.0.0.1:${VIRTUAL_KEY_CONTROL_PORT}/v1/virtual-keys/${independent_key_id}" -H "Authorization: Bearer ${admin_token}" >/dev/null
    [[ -n "$key_id" ]] && curl -fsS -X DELETE "http://127.0.0.1:${VIRTUAL_KEY_CONTROL_PORT}/v1/virtual-keys/${key_id}" -H "Authorization: Bearer ${admin_token}" >/dev/null
    [[ -n "$ui_model_id" ]] && curl -fsS -X DELETE "http://127.0.0.1:${CONTROL_PORT}/v1/models/${ui_model_id}" -H "Authorization: Bearer ${admin_token}" >/dev/null
    [[ -n "$model_id" ]] && curl -fsS -X DELETE "http://127.0.0.1:${CONTROL_PORT}/v1/models/${model_id}" -H "Authorization: Bearer ${admin_token}" >/dev/null
    [[ -n "$ui_provider_id" ]] && curl -fsS -X DELETE "http://127.0.0.1:${CONTROL_PORT}/v1/providers/${ui_provider_id}" -H "Authorization: Bearer ${admin_token}" >/dev/null
    [[ -n "$independent_provider_id" ]] && curl -fsS -X DELETE "http://127.0.0.1:${CONTROL_PORT}/v1/providers/${independent_provider_id}" -H "Authorization: Bearer ${admin_token}" >/dev/null
    [[ -n "$provider_id" ]] && curl -fsS -X DELETE "http://127.0.0.1:${CONTROL_PORT}/v1/providers/${provider_id}" -H "Authorization: Bearer ${admin_token}" >/dev/null
    [[ -n "$ui_user_id" ]] && curl -fsS -X DELETE "http://127.0.0.1:${CONTROL_PORT}/v1/users/${ui_user_id}" -H "Authorization: Bearer ${admin_token}" >/dev/null
    [[ -n "$user_id" ]] && curl -fsS -X DELETE "http://127.0.0.1:${CONTROL_PORT}/v1/users/${user_id}" -H "Authorization: Bearer ${admin_token}" >/dev/null
  fi

  for pid in "$control_forward_pid" "$virtual_key_forward_pid" "$admin_webui_forward_pid" "$openai_chat_forward_pid" "$openai_responses_forward_pid" "$anthropic_forward_pid" "$gemini_forward_pid" "$provider_pid"; do
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
kubectl -n "$NAMESPACE" get deployment "${RELEASE}-admin-webui" >/dev/null
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
[[ " $provider_scopes " == *" ${RELEASE}-control-plane "* && " $provider_scopes " == *" ${ADAPTER_APP_ID} "* ]]
[[ " $provider_scopes " != *" ${RELEASE}-virtual-key-control-plane "* ]]
[[ " $key_scopes " == *" ${RELEASE}-virtual-key-control-plane "* && " $key_scopes " == *" ${RELEASE}-openai-gateway "* ]]
[[ " $key_scopes " != *" ${RELEASE}-control-plane "* && " $key_scopes " != *" ${ADAPTER_APP_ID} "* ]]
[[ " $control_scopes $provider_scopes $key_scopes " != *" ${RELEASE}-admin-webui "* ]]

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
kubectl -n "$NAMESPACE" port-forward "service/${RELEASE}-admin-webui" "${ADMIN_WEBUI_PORT}:8080" >"$TMP_DIR/admin-webui-forward.log" 2>&1 &
admin_webui_forward_pid=$!
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
wait_for_url "http://127.0.0.1:${ADMIN_WEBUI_PORT}/readyz"
wait_for_url "http://127.0.0.1:${OPENAI_CHAT_PORT}/readyz"
wait_for_url "http://127.0.0.1:${OPENAI_RESPONSES_PORT}/readyz"
wait_for_url "http://127.0.0.1:${ANTHROPIC_GATEWAY_PORT}/readyz"
wait_for_url "http://127.0.0.1:${GEMINI_GATEWAY_PORT}/readyz"

admin_token=$(kubectl -n "$NAMESPACE" get secret "${RELEASE}-admin" -o jsonpath='{.data.admin-token}' | base64 -d)
[[ -n "$admin_token" ]] || { echo "admin token Secret is empty" >&2; exit 1; }
node_ip=$(kubectl get nodes -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}')

# The browser boundary owns opaque sessions and CSRF; the control-plane bearer
# token must never be returned in HTML, headers or the cookie jar.
ui_base="http://127.0.0.1:${ADMIN_WEBUI_PORT}"
ui_cookie_jar="$TMP_DIR/admin-webui.cookies"
status=$(curl -sS -D "$TMP_DIR/ui-unauth.headers" -o "$TMP_DIR/ui-unauth.body" -w '%{http_code}' "$ui_base/")
[[ "$status" == 303 ]] || { echo "unauthenticated WebUI dashboard did not redirect ($status)" >&2; exit 1; }
grep -Eiq '^location: /login' "$TMP_DIR/ui-unauth.headers"
grep -Eiq '^cache-control: no-store' "$TMP_DIR/ui-unauth.headers"

curl -fsS -c "$ui_cookie_jar" -D "$TMP_DIR/ui-login.headers" -o "$TMP_DIR/ui-login.html" "$ui_base/login"
grep -Eiq '^cache-control: no-store' "$TMP_DIR/ui-login.headers"
grep -Eiq "^content-security-policy: .*frame-ancestors 'none'" "$TMP_DIR/ui-login.headers"
grep -Eiq '^x-content-type-options: nosniff' "$TMP_DIR/ui-login.headers"
grep -Eiq '^set-cookie: gwai_admin_session=[^;]+; Path=/; .*HttpOnly; SameSite=Strict' "$TMP_DIR/ui-login.headers"
login_csrf=$(grep -m 1 -Eo 'name="_csrf" value="[^"]+"' "$TMP_DIR/ui-login.html" | sed -E 's/.*value="([^"]+)"/\1/')
[[ -n "$login_csrf" ]] || { echo "WebUI login form has no CSRF token" >&2; exit 1; }

status=$(curl -sS -b "$ui_cookie_jar" -c "$ui_cookie_jar" -o "$TMP_DIR/ui-invalid-login.html" -w '%{http_code}' \
  -X POST "$ui_base/login" --data-urlencode "_csrf=${login_csrf}" --data-urlencode 'admin_token=invalid-e2e-token')
[[ "$status" == 401 ]] || { echo "WebUI accepted an invalid admin token ($status)" >&2; exit 1; }
status=$(curl -sS -b "$ui_cookie_jar" -c "$ui_cookie_jar" -o /dev/null -w '%{http_code}' "$ui_base/")
[[ "$status" == 303 ]] || { echo "invalid WebUI login created an authenticated session ($status)" >&2; exit 1; }
curl -fsS -b "$ui_cookie_jar" -c "$ui_cookie_jar" -o "$TMP_DIR/ui-login-retry.html" "$ui_base/login"
login_csrf=$(grep -m 1 -Eo 'name="_csrf" value="[^"]+"' "$TMP_DIR/ui-login-retry.html" | sed -E 's/.*value="([^"]+)"/\1/')
[[ -n "$login_csrf" ]] || { echo "WebUI login retry has no CSRF token" >&2; exit 1; }

status=$(curl -sS -b "$ui_cookie_jar" -c "$ui_cookie_jar" -D "$TMP_DIR/ui-valid-login.headers" -o "$TMP_DIR/ui-valid-login.body" -w '%{http_code}' \
  -X POST "$ui_base/login" --data-urlencode "_csrf=${login_csrf}" --data-urlencode "admin_token=${admin_token}")
[[ "$status" == 303 ]] || { echo "WebUI rejected the valid admin token ($status)" >&2; exit 1; }
grep -Eiq '^location: /[[:space:]]*$' "$TMP_DIR/ui-valid-login.headers"
grep -Eiq '^set-cookie: gwai_admin_session=[^;]+; Path=/; .*HttpOnly; SameSite=Strict' "$TMP_DIR/ui-valid-login.headers"
if grep -Fq -- "$admin_token" "$TMP_DIR/ui-unauth.headers" "$TMP_DIR/ui-unauth.body" "$TMP_DIR/ui-login.headers" "$TMP_DIR/ui-login.html" "$TMP_DIR/ui-valid-login.headers" "$TMP_DIR/ui-valid-login.body" "$ui_cookie_jar"; then
  echo "WebUI exposed the admin token to the browser" >&2
  exit 1
fi

status=$(curl -sS -b "$ui_cookie_jar" -D "$TMP_DIR/ui-dashboard.headers" -o "$TMP_DIR/ui-dashboard.html" -w '%{http_code}' "$ui_base/")
[[ "$status" == 200 ]] || { echo "authenticated WebUI dashboard failed ($status)" >&2; exit 1; }
grep -Fq 'Dashboard' "$TMP_DIR/ui-dashboard.html"
grep -Eiq '^cache-control: no-store' "$TMP_DIR/ui-dashboard.headers"
dashboard_csrf=$(grep -m 1 -Eo 'name="_csrf" value="[^"]+"' "$TMP_DIR/ui-dashboard.html" | sed -E 's/.*value="([^"]+)"/\1/')
[[ -n "$dashboard_csrf" ]] || { echo "WebUI dashboard has no CSRF token" >&2; exit 1; }

status=$(curl -sS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-missing-csrf.html" -w '%{http_code}' \
  -X POST "$ui_base/users" --data-urlencode 'name=must-not-exist' --data-urlencode 'email=must-not-exist@example.com')
[[ "$status" == 403 ]] || { echo "WebUI accepted a mutation without CSRF ($status)" >&2; exit 1; }

# Exercise every lifecycle domain through HTML form actions. IDs are resolved
# through the JSON APIs only for deterministic assertions and cleanup.
ui_user_name="WebUI ${RUN_ID}"
ui_user_email="webui-${RUN_ID}@example.com"
curl -fsS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-new-user.html" "$ui_base/users/new"
grep -Fq 'action="/users"' "$TMP_DIR/ui-new-user.html"
ui_user_create_csrf=$(hidden_input_value "$TMP_DIR/ui-new-user.html" _csrf)
[[ -n "$ui_user_create_csrf" ]] || { echo "WebUI new-user form lacks CSRF" >&2; exit 1; }
status=$(curl -sS -b "$ui_cookie_jar" -D "$TMP_DIR/ui-create-user.headers" -o "$TMP_DIR/ui-create-user.body" -w '%{http_code}' \
  -X POST "$ui_base/users" --data-urlencode "_csrf=${ui_user_create_csrf}" \
  --data-urlencode "name=${ui_user_name}" --data-urlencode "email=${ui_user_email}" --data-urlencode 'status=active')
[[ "$status" == 303 ]] || { echo "WebUI user creation failed ($status)" >&2; exit 1; }
grep -Eiq '^location: /users' "$TMP_DIR/ui-create-user.headers"
ui_user_id=$(curl -fsS "http://127.0.0.1:${CONTROL_PORT}/v1/users" -H "Authorization: Bearer ${admin_token}" \
  | jq -er --arg email "$ui_user_email" '.data | map(select(.email == $email)) | first | .id')
curl -fsS -b "$ui_cookie_jar" -G --data-urlencode "q=${ui_user_email}" --data-urlencode 'status=active' \
  -o "$TMP_DIR/ui-filter-users.html" "$ui_base/users"
grep -Fq "href=\"/users/${ui_user_id}/edit\"" "$TMP_DIR/ui-filter-users.html"
curl -fsS -b "$ui_cookie_jar" -G --data-urlencode "q=missing-${RUN_ID}" --data-urlencode 'status=active' \
  -o "$TMP_DIR/ui-filter-users-query-miss.html" "$ui_base/users"
if grep -Fq "href=\"/users/${ui_user_id}/edit\"" "$TMP_DIR/ui-filter-users-query-miss.html"; then
  echo "WebUI user search filter retained a nonmatching row" >&2
  exit 1
fi
curl -fsS -b "$ui_cookie_jar" -G --data-urlencode "q=${ui_user_email}" --data-urlencode 'status=disabled' \
  -o "$TMP_DIR/ui-filter-users-status-miss.html" "$ui_base/users"
if grep -Fq "href=\"/users/${ui_user_id}/edit\"" "$TMP_DIR/ui-filter-users-status-miss.html"; then
  echo "WebUI user status filter retained a nonmatching row" >&2
  exit 1
fi

curl -fsS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-edit-user.html" "$ui_base/users/${ui_user_id}/edit"
ui_user_csrf=$(hidden_input_value "$TMP_DIR/ui-edit-user.html" _csrf)
ui_user_etag=$(hidden_input_value "$TMP_DIR/ui-edit-user.html" _etag)
[[ -n "$ui_user_csrf" && -n "$ui_user_etag" ]] || { echo "WebUI user edit form lacks CSRF or ETag" >&2; exit 1; }

ui_user_name_updated="WebUI updated ${RUN_ID}"
status=$(curl -sS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-update-user.body" -w '%{http_code}' \
	-X POST "$ui_base/users/${ui_user_id}" --data-urlencode "_csrf=${ui_user_csrf}" --data-urlencode "_etag=${ui_user_etag}" \
  --data-urlencode "name=${ui_user_name_updated}" --data-urlencode "email=${ui_user_email}" --data-urlencode 'status=active')
[[ "$status" == 303 ]] || { echo "WebUI user update failed ($status)" >&2; exit 1; }
curl -fsS "http://127.0.0.1:${CONTROL_PORT}/v1/users/${ui_user_id}" -H "Authorization: Bearer ${admin_token}" \
  | jq -e --arg name "$ui_user_name_updated" '.name == $name and .status == "active"' >/dev/null
for lifecycle_status in disabled active; do
	curl -fsS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-user-status-${lifecycle_status}.html" \
	  "$ui_base/users/${ui_user_id}/status?to=${lifecycle_status}"
	grep -Fq 'Review the impact' "$TMP_DIR/ui-user-status-${lifecycle_status}.html"
	lifecycle_csrf=$(hidden_input_value "$TMP_DIR/ui-user-status-${lifecycle_status}.html" _csrf)
	lifecycle_etag=$(hidden_input_value "$TMP_DIR/ui-user-status-${lifecycle_status}.html" _etag)
	[[ -n "$lifecycle_csrf" && -n "$lifecycle_etag" ]] || { echo "WebUI user status confirmation lacks CSRF or ETag" >&2; exit 1; }
  status=$(curl -sS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-user-status-${lifecycle_status}.body" -w '%{http_code}' \
	  -X POST "$ui_base/users/${ui_user_id}/status" --data-urlencode "_csrf=${lifecycle_csrf}" \
	  --data-urlencode "_etag=${lifecycle_etag}" \
	  --data-urlencode "status=${lifecycle_status}")
  [[ "$status" == 303 ]] || { echo "WebUI user ${lifecycle_status} lifecycle failed ($status)" >&2; exit 1; }
  curl -fsS "http://127.0.0.1:${CONTROL_PORT}/v1/users/${ui_user_id}" -H "Authorization: Bearer ${admin_token}" \
    | jq -e --arg status "$lifecycle_status" '.status == $status' >/dev/null
done

ui_provider_slug="ui-${RUN_ID}"
ui_provider_name="WebUI provider ${RUN_ID}"
ui_adapter_app_id="gwai-${ui_provider_slug}"
curl -fsS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-new-provider.html" "$ui_base/providers/new"
grep -Fq 'action="/providers"' "$TMP_DIR/ui-new-provider.html"
ui_provider_create_csrf=$(hidden_input_value "$TMP_DIR/ui-new-provider.html" _csrf)
[[ -n "$ui_provider_create_csrf" ]] || { echo "WebUI new-provider form lacks CSRF" >&2; exit 1; }
status=$(curl -sS -b "$ui_cookie_jar" -D "$TMP_DIR/ui-create-provider.headers" -o "$TMP_DIR/ui-create-provider.body" -w '%{http_code}' \
  -X POST "$ui_base/providers" --data-urlencode "_csrf=${ui_provider_create_csrf}" \
  --data-urlencode "slug=${ui_provider_slug}" --data-urlencode "name=${ui_provider_name}" \
  --data-urlencode 'kind=anthropic' --data-urlencode "base_url=http://${node_ip}:${PROVIDER_PORT}" \
  --data-urlencode 'api_version=2023-06-01' --data-urlencode "adapter_app_id=${ui_adapter_app_id}" \
  --data-urlencode 'secret_store=kubernetes' --data-urlencode "secret_name=${PROVIDER_SECRET}" \
  --data-urlencode 'secret_key=api-key' --data-urlencode 'status=active')
[[ "$status" == 303 ]] || { echo "WebUI provider creation failed ($status)" >&2; exit 1; }
grep -Eiq '^location: /providers' "$TMP_DIR/ui-create-provider.headers"
ui_provider_id=$(curl -fsS "http://127.0.0.1:${CONTROL_PORT}/v1/providers" -H "Authorization: Bearer ${admin_token}" \
  | jq -er --arg slug "$ui_provider_slug" '.data | map(select(.slug == $slug)) | first | .id')
curl -fsS -b "$ui_cookie_jar" -G --data-urlencode "q=${ui_provider_slug}" --data-urlencode 'status=active' \
  -o "$TMP_DIR/ui-filter-providers.html" "$ui_base/providers"
grep -Fq "href=\"/providers/${ui_provider_id}/edit\"" "$TMP_DIR/ui-filter-providers.html"
curl -fsS -b "$ui_cookie_jar" -G --data-urlencode "q=missing-${RUN_ID}" --data-urlencode 'status=active' \
  -o "$TMP_DIR/ui-filter-providers-query-miss.html" "$ui_base/providers"
if grep -Fq "href=\"/providers/${ui_provider_id}/edit\"" "$TMP_DIR/ui-filter-providers-query-miss.html"; then
  echo "WebUI provider search filter retained a nonmatching row" >&2
  exit 1
fi
curl -fsS -b "$ui_cookie_jar" -G --data-urlencode "q=${ui_provider_slug}" --data-urlencode 'status=disabled' \
  -o "$TMP_DIR/ui-filter-providers-status-miss.html" "$ui_base/providers"
if grep -Fq "href=\"/providers/${ui_provider_id}/edit\"" "$TMP_DIR/ui-filter-providers-status-miss.html"; then
  echo "WebUI provider status filter retained a nonmatching row" >&2
  exit 1
fi

curl -fsS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-edit-provider.html" "$ui_base/providers/${ui_provider_id}/edit"
ui_provider_csrf=$(hidden_input_value "$TMP_DIR/ui-edit-provider.html" _csrf)
ui_provider_etag=$(hidden_input_value "$TMP_DIR/ui-edit-provider.html" _etag)
[[ -n "$ui_provider_csrf" && -n "$ui_provider_etag" ]] || { echo "WebUI provider edit form lacks CSRF or ETag" >&2; exit 1; }

ui_provider_name_updated="WebUI provider updated ${RUN_ID}"
status=$(curl -sS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-update-provider.body" -w '%{http_code}' \
	-X POST "$ui_base/providers/${ui_provider_id}" --data-urlencode "_csrf=${ui_provider_csrf}" --data-urlencode "_etag=${ui_provider_etag}" \
  --data-urlencode "slug=${ui_provider_slug}" --data-urlencode "name=${ui_provider_name_updated}" \
  --data-urlencode 'kind=anthropic' --data-urlencode "base_url=http://${node_ip}:${PROVIDER_PORT}" \
  --data-urlencode 'api_version=2023-06-01' --data-urlencode "adapter_app_id=${ui_adapter_app_id}" \
  --data-urlencode 'secret_store=kubernetes' --data-urlencode "secret_name=${PROVIDER_SECRET}" \
  --data-urlencode 'secret_key=api-key' --data-urlencode 'status=active')
[[ "$status" == 303 ]] || { echo "WebUI provider update failed ($status)" >&2; exit 1; }
curl -fsS "http://127.0.0.1:${CONTROL_PORT}/v1/providers/${ui_provider_id}" -H "Authorization: Bearer ${admin_token}" \
  | jq -e --arg name "$ui_provider_name_updated" '.name == $name and .status == "active"' >/dev/null
for lifecycle_status in disabled active; do
	curl -fsS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-provider-status-${lifecycle_status}.html" \
	  "$ui_base/providers/${ui_provider_id}/status?to=${lifecycle_status}"
	grep -Fq 'Review the impact' "$TMP_DIR/ui-provider-status-${lifecycle_status}.html"
	lifecycle_csrf=$(hidden_input_value "$TMP_DIR/ui-provider-status-${lifecycle_status}.html" _csrf)
	lifecycle_etag=$(hidden_input_value "$TMP_DIR/ui-provider-status-${lifecycle_status}.html" _etag)
	[[ -n "$lifecycle_csrf" && -n "$lifecycle_etag" ]] || { echo "WebUI provider status confirmation lacks CSRF or ETag" >&2; exit 1; }
  status=$(curl -sS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-provider-status-${lifecycle_status}.body" -w '%{http_code}' \
	  -X POST "$ui_base/providers/${ui_provider_id}/status" --data-urlencode "_csrf=${lifecycle_csrf}" \
	  --data-urlencode "_etag=${lifecycle_etag}" \
    --data-urlencode "status=${lifecycle_status}")
  [[ "$status" == 303 ]] || { echo "WebUI provider ${lifecycle_status} lifecycle failed ($status)" >&2; exit 1; }
  curl -fsS "http://127.0.0.1:${CONTROL_PORT}/v1/providers/${ui_provider_id}" -H "Authorization: Bearer ${admin_token}" \
    | jq -e --arg status "$lifecycle_status" '.status == $status' >/dev/null
done

ui_model_alias="ui-model-${RUN_ID}"
ui_upstream_model="upstream-ui-${RUN_ID}"
curl -fsS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-new-model.html" "$ui_base/models/new"
grep -Fq 'action="/models"' "$TMP_DIR/ui-new-model.html"
ui_model_create_csrf=$(hidden_input_value "$TMP_DIR/ui-new-model.html" _csrf)
[[ -n "$ui_model_create_csrf" ]] || { echo "WebUI new-model form lacks CSRF" >&2; exit 1; }
status=$(curl -sS -b "$ui_cookie_jar" -D "$TMP_DIR/ui-create-model.headers" -o "$TMP_DIR/ui-create-model.body" -w '%{http_code}' \
  -X POST "$ui_base/models" --data-urlencode "_csrf=${ui_model_create_csrf}" \
  --data-urlencode "alias=${ui_model_alias}" --data-urlencode "provider_id=${ui_provider_id}" \
  --data-urlencode "upstream_model=${ui_upstream_model}" --data-urlencode 'status=active')
[[ "$status" == 303 ]] || { echo "WebUI model creation failed ($status)" >&2; exit 1; }
grep -Eiq '^location: /models' "$TMP_DIR/ui-create-model.headers"
ui_model_id=$(curl -fsS "http://127.0.0.1:${CONTROL_PORT}/v1/models" -H "Authorization: Bearer ${admin_token}" \
  | jq -er --arg alias "$ui_model_alias" '.data | map(select(.alias == $alias)) | first | .id')
curl -fsS -b "$ui_cookie_jar" -G --data-urlencode "q=${ui_model_alias}" --data-urlencode 'status=active' \
  --data-urlencode "provider_id=${ui_provider_id}" -o "$TMP_DIR/ui-filter-models.html" "$ui_base/models"
grep -Fq "href=\"/models/${ui_model_id}/edit\"" "$TMP_DIR/ui-filter-models.html"
curl -fsS -b "$ui_cookie_jar" -G --data-urlencode "q=missing-${RUN_ID}" --data-urlencode 'status=active' \
  --data-urlencode "provider_id=${ui_provider_id}" -o "$TMP_DIR/ui-filter-models-miss.html" "$ui_base/models"
if grep -Fq "href=\"/models/${ui_model_id}/edit\"" "$TMP_DIR/ui-filter-models-miss.html"; then
  echo "WebUI model search filter retained a nonmatching row" >&2
  exit 1
fi

curl -fsS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-edit-model.html" "$ui_base/models/${ui_model_id}/edit"
ui_model_csrf=$(hidden_input_value "$TMP_DIR/ui-edit-model.html" _csrf)
ui_model_etag=$(hidden_input_value "$TMP_DIR/ui-edit-model.html" _etag)
[[ -n "$ui_model_csrf" && -n "$ui_model_etag" ]] || { echo "WebUI model edit form lacks CSRF or ETag" >&2; exit 1; }
ui_upstream_model_updated="upstream-ui-updated-${RUN_ID}"
status=$(curl -sS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-update-model.body" -w '%{http_code}' \
  -X POST "$ui_base/models/${ui_model_id}" --data-urlencode "_csrf=${ui_model_csrf}" --data-urlencode "_etag=${ui_model_etag}" \
  --data-urlencode "alias=${ui_model_alias}" --data-urlencode "provider_id=${ui_provider_id}" \
  --data-urlencode "upstream_model=${ui_upstream_model_updated}" --data-urlencode 'status=active')
[[ "$status" == 303 ]] || { echo "WebUI model update failed ($status)" >&2; exit 1; }
curl -fsS "http://127.0.0.1:${CONTROL_PORT}/v1/models/${ui_model_id}" -H "Authorization: Bearer ${admin_token}" \
  | jq -e --arg upstream "$ui_upstream_model_updated" '.upstream_model == $upstream and .revision >= 2' >/dev/null
for lifecycle_status in disabled active; do
	curl -fsS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-model-status-${lifecycle_status}.html" \
	  "$ui_base/models/${ui_model_id}/status?to=${lifecycle_status}"
	grep -Fq 'Review the impact' "$TMP_DIR/ui-model-status-${lifecycle_status}.html"
	lifecycle_csrf=$(hidden_input_value "$TMP_DIR/ui-model-status-${lifecycle_status}.html" _csrf)
	lifecycle_etag=$(hidden_input_value "$TMP_DIR/ui-model-status-${lifecycle_status}.html" _etag)
	[[ -n "$lifecycle_csrf" && -n "$lifecycle_etag" ]] || { echo "WebUI model status confirmation lacks CSRF or ETag" >&2; exit 1; }
  status=$(curl -sS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-model-status-${lifecycle_status}.body" -w '%{http_code}' \
	  -X POST "$ui_base/models/${ui_model_id}/status" --data-urlencode "_csrf=${lifecycle_csrf}" \
	  --data-urlencode "_etag=${lifecycle_etag}" --data-urlencode "status=${lifecycle_status}")
  [[ "$status" == 303 ]] || { echo "WebUI model ${lifecycle_status} lifecycle failed ($status)" >&2; exit 1; }
  curl -fsS "http://127.0.0.1:${CONTROL_PORT}/v1/models/${ui_model_id}" -H "Authorization: Bearer ${admin_token}" \
    | jq -e --arg status "$lifecycle_status" '.status == $status' >/dev/null
done

ui_key_name="WebUI key ${RUN_ID}"
curl -fsS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-new-key.html" "$ui_base/virtual-keys/new"
ui_key_csrf=$(hidden_input_value "$TMP_DIR/ui-new-key.html" _csrf)
ui_operation=$(hidden_input_value "$TMP_DIR/ui-new-key.html" _operation)
[[ -n "$ui_key_csrf" && -n "$ui_operation" ]] || { echo "WebUI key form lacks CSRF or one-time operation token" >&2; exit 1; }
status=$(curl -sS -b "$ui_cookie_jar" -D "$TMP_DIR/ui-create-key.headers" -o "$TMP_DIR/ui-create-key.body" -w '%{http_code}' \
	-X POST "$ui_base/virtual-keys" --data-urlencode "_csrf=${ui_key_csrf}" --data-urlencode "_operation=${ui_operation}" \
  --data-urlencode "name=${ui_key_name}" --data-urlencode "user_id=${ui_user_id}" \
  --data-urlencode "model_ids=${ui_model_id}" --data-urlencode 'status=active')
[[ "$status" == 201 ]] || { echo "WebUI virtual-key creation failed ($status)" >&2; exit 1; }
ui_key_id=$(curl -fsS "http://127.0.0.1:${VIRTUAL_KEY_CONTROL_PORT}/v1/virtual-keys" -H "Authorization: Bearer ${admin_token}" \
  | jq -er --arg name "$ui_key_name" '.data | map(select(.name == $name)) | first | .id')
grep -Eiq '^cache-control: no-store, max-age=0' "$TMP_DIR/ui-create-key.headers"
grep -Eiq '^pragma: no-cache' "$TMP_DIR/ui-create-key.headers"
grep -Eq 'gwai_[A-Za-z0-9_-]{40,}' "$TMP_DIR/ui-create-key.body"
if grep -Fq -- "$admin_token" "$TMP_DIR/ui-create-key.body"; then
  echo "WebUI key reveal exposed the admin token" >&2
  exit 1
fi
status=$(curl -sS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-key-resubmit.html" -w '%{http_code}' \
	-X POST "$ui_base/virtual-keys" --data-urlencode "_csrf=${ui_key_csrf}" --data-urlencode "_operation=${ui_operation}" \
	--data-urlencode "name=${ui_key_name}" --data-urlencode "user_id=${ui_user_id}" \
	--data-urlencode "model_ids=${ui_model_id}" --data-urlencode 'status=active')
[[ "$status" == 409 ]] || { echo "WebUI reused a consumed key-creation form ($status)" >&2; exit 1; }
if grep -Eq 'gwai_[A-Za-z0-9_-]{40,}' "$TMP_DIR/ui-key-resubmit.html"; then
  echo "WebUI repeated a one-time key secret" >&2
  exit 1
fi
key_name_count=$(curl -fsS "http://127.0.0.1:${VIRTUAL_KEY_CONTROL_PORT}/v1/virtual-keys" -H "Authorization: Bearer ${admin_token}" \
  | jq -er --arg name "$ui_key_name" '[.data[] | select(.name == $name)] | length')
[[ "$key_name_count" == 1 ]] || { echo "WebUI duplicate submit created $key_name_count keys" >&2; exit 1; }
curl -fsS -b "$ui_cookie_jar" -G --data-urlencode "q=${ui_key_name}" --data-urlencode 'status=active' --data-urlencode "user_id=${ui_user_id}" \
  -o "$TMP_DIR/ui-filter-keys.html" "$ui_base/virtual-keys"
grep -Fq "href=\"/virtual-keys/${ui_key_id}/edit\"" "$TMP_DIR/ui-filter-keys.html"
curl -fsS -b "$ui_cookie_jar" -G --data-urlencode "q=missing-${RUN_ID}" --data-urlencode 'status=active' --data-urlencode "user_id=${ui_user_id}" \
  -o "$TMP_DIR/ui-filter-keys-query-miss.html" "$ui_base/virtual-keys"
if grep -Fq "href=\"/virtual-keys/${ui_key_id}/edit\"" "$TMP_DIR/ui-filter-keys-query-miss.html"; then
  echo "WebUI virtual-key search filter retained a nonmatching row" >&2
  exit 1
fi
curl -fsS -b "$ui_cookie_jar" -G --data-urlencode "q=${ui_key_name}" --data-urlencode 'status=disabled' --data-urlencode "user_id=${ui_user_id}" \
  -o "$TMP_DIR/ui-filter-keys-status-miss.html" "$ui_base/virtual-keys"
if grep -Fq "href=\"/virtual-keys/${ui_key_id}/edit\"" "$TMP_DIR/ui-filter-keys-status-miss.html"; then
  echo "WebUI virtual-key status filter retained a nonmatching row" >&2
  exit 1
fi
curl -fsS -b "$ui_cookie_jar" -G --data-urlencode "q=${ui_key_name}" --data-urlencode 'status=active' --data-urlencode "user_id=missing-${RUN_ID}" \
  -o "$TMP_DIR/ui-filter-keys-owner-miss.html" "$ui_base/virtual-keys"
if grep -Fq "href=\"/virtual-keys/${ui_key_id}/edit\"" "$TMP_DIR/ui-filter-keys-owner-miss.html"; then
  echo "WebUI virtual-key owner filter retained a nonmatching row" >&2
  exit 1
fi
for lifecycle_status in disabled active; do
	curl -fsS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-key-status-${lifecycle_status}.html" \
	  "$ui_base/virtual-keys/${ui_key_id}/status?to=${lifecycle_status}"
	grep -Fq 'Review the impact' "$TMP_DIR/ui-key-status-${lifecycle_status}.html"
	lifecycle_csrf=$(hidden_input_value "$TMP_DIR/ui-key-status-${lifecycle_status}.html" _csrf)
	lifecycle_etag=$(hidden_input_value "$TMP_DIR/ui-key-status-${lifecycle_status}.html" _etag)
	[[ -n "$lifecycle_csrf" && -n "$lifecycle_etag" ]] || { echo "WebUI key status confirmation lacks CSRF or ETag" >&2; exit 1; }
  status=$(curl -sS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-key-status-${lifecycle_status}.body" -w '%{http_code}' \
	  -X POST "$ui_base/virtual-keys/${ui_key_id}/status" --data-urlencode "_csrf=${lifecycle_csrf}" \
	  --data-urlencode "_etag=${lifecycle_etag}" \
	  --data-urlencode "status=${lifecycle_status}")
  [[ "$status" == 303 ]] || { echo "WebUI virtual-key ${lifecycle_status} lifecycle failed ($status)" >&2; exit 1; }
  curl -fsS "http://127.0.0.1:${VIRTUAL_KEY_CONTROL_PORT}/v1/virtual-keys/${ui_key_id}" -H "Authorization: Bearer ${admin_token}" \
    | jq -e --arg status "$lifecycle_status" '.status == $status' >/dev/null
done
curl -fsS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-edit-key.html" "$ui_base/virtual-keys/${ui_key_id}/edit"
ui_key_edit_csrf=$(hidden_input_value "$TMP_DIR/ui-edit-key.html" _csrf)
ui_key_etag=$(hidden_input_value "$TMP_DIR/ui-edit-key.html" _etag)
[[ -n "$ui_key_edit_csrf" && -n "$ui_key_etag" ]] || { echo "WebUI key edit form lacks CSRF or ETag" >&2; exit 1; }
ui_key_name_updated="WebUI key updated ${RUN_ID}"
status=$(curl -sS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-update-key.body" -w '%{http_code}' \
	-X POST "$ui_base/virtual-keys/${ui_key_id}" --data-urlencode "_csrf=${ui_key_edit_csrf}" --data-urlencode "_etag=${ui_key_etag}" \
  --data-urlencode "name=${ui_key_name_updated}" --data-urlencode "user_id=${ui_user_id}" \
  --data-urlencode "model_ids=${ui_model_id}" --data-urlencode 'status=disabled')
[[ "$status" == 303 ]] || { echo "WebUI virtual-key update failed ($status)" >&2; exit 1; }
curl -fsS "http://127.0.0.1:${VIRTUAL_KEY_CONTROL_PORT}/v1/virtual-keys/${ui_key_id}" -H "Authorization: Bearer ${admin_token}" \
  | jq -e --arg name "$ui_key_name_updated" --arg model "$ui_model_id" \
    '.name == $name and .status == "disabled" and .model_ids == [$model]' >/dev/null

status=$(curl -sS -o /dev/null -w '%{http_code}' -X DELETE "http://127.0.0.1:${CONTROL_PORT}/v1/models/${ui_model_id}" -H "Authorization: Bearer ${admin_token}")
[[ "$status" == 409 ]] || { echo "model deletion with a live virtual-key reference did not conflict ($status)" >&2; exit 1; }
status=$(curl -sS -o /dev/null -w '%{http_code}' -X DELETE "http://127.0.0.1:${CONTROL_PORT}/v1/providers/${ui_provider_id}" -H "Authorization: Bearer ${admin_token}")
[[ "$status" == 409 ]] || { echo "provider deletion with a live model did not conflict ($status)" >&2; exit 1; }

curl -fsS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-delete-key-confirm.html" "$ui_base/virtual-keys/${ui_key_id}/delete"
grep -Fq 'Delete virtual key' "$TMP_DIR/ui-delete-key-confirm.html"
delete_csrf=$(hidden_input_value "$TMP_DIR/ui-delete-key-confirm.html" _csrf)
delete_etag=$(hidden_input_value "$TMP_DIR/ui-delete-key-confirm.html" _etag)
[[ -n "$delete_csrf" && -n "$delete_etag" ]] || { echo "WebUI key deletion confirmation lacks CSRF or ETag" >&2; exit 1; }
status=$(curl -sS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-delete-key.body" -w '%{http_code}' \
	-X POST "$ui_base/virtual-keys/${ui_key_id}/delete" --data-urlencode "_csrf=${delete_csrf}" --data-urlencode "_etag=${delete_etag}")
[[ "$status" == 303 ]] || { echo "WebUI virtual-key deletion failed ($status)" >&2; exit 1; }
status=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${VIRTUAL_KEY_CONTROL_PORT}/v1/virtual-keys/${ui_key_id}" -H "Authorization: Bearer ${admin_token}")
[[ "$status" == 404 ]] || { echo "WebUI-deleted virtual key remains available ($status)" >&2; exit 1; }
ui_key_id=""

curl -fsS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-delete-model-confirm.html" "$ui_base/models/${ui_model_id}/delete"
grep -Fq 'Delete model' "$TMP_DIR/ui-delete-model-confirm.html"
delete_csrf=$(hidden_input_value "$TMP_DIR/ui-delete-model-confirm.html" _csrf)
delete_etag=$(hidden_input_value "$TMP_DIR/ui-delete-model-confirm.html" _etag)
[[ -n "$delete_csrf" && -n "$delete_etag" ]] || { echo "WebUI model deletion confirmation lacks CSRF or ETag" >&2; exit 1; }
status=$(curl -sS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-delete-model.body" -w '%{http_code}' \
	-X POST "$ui_base/models/${ui_model_id}/delete" --data-urlencode "_csrf=${delete_csrf}" --data-urlencode "_etag=${delete_etag}")
[[ "$status" == 303 ]] || { echo "WebUI model deletion failed ($status)" >&2; exit 1; }
status=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${CONTROL_PORT}/v1/models/${ui_model_id}" -H "Authorization: Bearer ${admin_token}")
[[ "$status" == 404 ]] || { echo "WebUI-deleted model remains available ($status)" >&2; exit 1; }
ui_model_id=""

curl -fsS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-delete-provider-confirm.html" "$ui_base/providers/${ui_provider_id}/delete"
grep -Fq 'Delete provider' "$TMP_DIR/ui-delete-provider-confirm.html"
delete_csrf=$(hidden_input_value "$TMP_DIR/ui-delete-provider-confirm.html" _csrf)
delete_etag=$(hidden_input_value "$TMP_DIR/ui-delete-provider-confirm.html" _etag)
[[ -n "$delete_csrf" && -n "$delete_etag" ]] || { echo "WebUI provider deletion confirmation lacks CSRF or ETag" >&2; exit 1; }
status=$(curl -sS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-delete-provider.body" -w '%{http_code}' \
	-X POST "$ui_base/providers/${ui_provider_id}/delete" --data-urlencode "_csrf=${delete_csrf}" --data-urlencode "_etag=${delete_etag}")
[[ "$status" == 303 ]] || { echo "WebUI provider deletion failed ($status)" >&2; exit 1; }
status=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${CONTROL_PORT}/v1/providers/${ui_provider_id}" -H "Authorization: Bearer ${admin_token}")
[[ "$status" == 404 ]] || { echo "WebUI-deleted provider remains available ($status)" >&2; exit 1; }
ui_provider_id=""

curl -fsS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-delete-user-confirm.html" "$ui_base/users/${ui_user_id}/delete"
grep -Fq 'Delete user' "$TMP_DIR/ui-delete-user-confirm.html"
delete_csrf=$(hidden_input_value "$TMP_DIR/ui-delete-user-confirm.html" _csrf)
delete_etag=$(hidden_input_value "$TMP_DIR/ui-delete-user-confirm.html" _etag)
[[ -n "$delete_csrf" && -n "$delete_etag" ]] || { echo "WebUI user deletion confirmation lacks CSRF or ETag" >&2; exit 1; }
status=$(curl -sS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-delete-user.body" -w '%{http_code}' \
	-X POST "$ui_base/users/${ui_user_id}/delete" --data-urlencode "_csrf=${delete_csrf}" --data-urlencode "_etag=${delete_etag}")
[[ "$status" == 303 ]] || { echo "WebUI user deletion failed ($status)" >&2; exit 1; }
status=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${CONTROL_PORT}/v1/users/${ui_user_id}" -H "Authorization: Bearer ${admin_token}")
[[ "$status" == 404 ]] || { echo "WebUI-deleted user remains available ($status)" >&2; exit 1; }
ui_user_id=""

# The public admin API is split: each service must reject the other domain.
status=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${CONTROL_PORT}/v1/virtual-keys" -H "Authorization: Bearer ${admin_token}")
[[ "$status" == 404 ]] || { echo "resource control plane unexpectedly exposes virtual keys ($status)" >&2; exit 1; }
status=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${VIRTUAL_KEY_CONTROL_PORT}/v1/users" -H "Authorization: Bearer ${admin_token}")
[[ "$status" == 404 ]] || { echo "virtual-key control plane unexpectedly exposes users ($status)" >&2; exit 1; }
status=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${VIRTUAL_KEY_CONTROL_PORT}/v1/models" -H "Authorization: Bearer ${admin_token}")
[[ "$status" == 404 ]] || { echo "virtual-key control plane unexpectedly exposes models ($status)" >&2; exit 1; }
status=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:${VIRTUAL_KEY_CONTROL_PORT}/internal/v1/subjects/sync" -H 'Content-Type: application/json' -d '{}')
[[ "$status" == 401 ]] || { echo "subject sync is reachable without the Dapr app token ($status)" >&2; exit 1; }
status=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:${VIRTUAL_KEY_CONTROL_PORT}/internal/v1/model-subjects/sync" -H 'Content-Type: application/json' -d '{}')
[[ "$status" == 401 ]] || { echo "model-subject sync is reachable without the Dapr app token ($status)" >&2; exit 1; }

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

model_alias="model-${RUN_ID}"
upstream_model="claude-e2e"
model=$(curl -fsS -D "$TMP_DIR/model-create.headers" "http://127.0.0.1:${CONTROL_PORT}/v1/models" \
  -H "Authorization: Bearer ${admin_token}" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg alias "$model_alias" --arg provider "$provider_id" --arg upstream "$upstream_model" \
    '{alias:$alias,provider_id:$provider,upstream_model:$upstream}')")
model_id=$(jq -er .id <<<"$model")
jq -e --arg alias "$model_alias" --arg provider "$provider_id" --arg upstream "$upstream_model" \
  '.alias == $alias and .provider_id == $provider and .upstream_model == $upstream and .status == "active" and .revision == 1' <<<"$model" >/dev/null
grep -Eiq '^etag: "[a-f0-9]+"' "$TMP_DIR/model-create.headers"

status=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${VIRTUAL_KEY_CONTROL_PORT}/v1/virtual-keys" \
  -H "Authorization: Bearer ${admin_token}" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg user "$user_id" '{name:"invalid empty models",user_id:$user,model_ids:[]}')")
[[ "$status" == 400 ]] || { echo "virtual key accepted an empty model_ids array ($status)" >&2; exit 1; }

created_key=$(curl -fsS "http://127.0.0.1:${VIRTUAL_KEY_CONTROL_PORT}/v1/virtual-keys" \
  -H "Authorization: Bearer ${admin_token}" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg user "$user_id" --arg model "$model_id" '{name:"E2E key",user_id:$user,model_ids:[$model]}')")
key_id=$(jq -er .virtual_key.id <<<"$created_key")
virtual_key=$(jq -er .key <<<"$created_key")
jq -e --arg model "$model_id" '.virtual_key.model_ids == [$model]' <<<"$created_key" >/dev/null

# Deletion crosses the service boundary: the remote fence must map its nonempty
# per-user key index to the public conflict contract.
status=$(curl -sS -o /dev/null -w '%{http_code}' -X DELETE "http://127.0.0.1:${CONTROL_PORT}/v1/users/${user_id}" -H "Authorization: Bearer ${admin_token}")
[[ "$status" == 409 ]] || { echo "user deletion with a live virtual key did not conflict ($status)" >&2; exit 1; }
status=$(curl -sS -o /dev/null -w '%{http_code}' -X DELETE "http://127.0.0.1:${CONTROL_PORT}/v1/models/${model_id}" -H "Authorization: Bearer ${admin_token}")
[[ "$status" == 409 ]] || { echo "model deletion with a live virtual key did not conflict ($status)" >&2; exit 1; }
status=$(curl -sS -o /dev/null -w '%{http_code}' -X DELETE "http://127.0.0.1:${CONTROL_PORT}/v1/providers/${provider_id}" -H "Authorization: Bearer ${admin_token}")
[[ "$status" == 409 ]] || { echo "provider deletion with a live model did not conflict ($status)" >&2; exit 1; }

call_openai_chat() {
  curl -fsS "http://127.0.0.1:${OPENAI_CHAT_PORT}/v1/chat/completions" \
    -H "Authorization: Bearer ${virtual_key}" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg model "$model_alias" '{model:$model,messages:[{role:"system",content:"Be concise"},{role:"user",content:"Say ok"}],max_completion_tokens:32}')"
}

call_openai_responses() {
  curl -fsS "http://127.0.0.1:${OPENAI_RESPONSES_PORT}/v1/responses" \
    -H "Authorization: Bearer ${virtual_key}" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg model "$model_alias" '{model:$model,input:"Say ok",max_output_tokens:32,store:false}')"
}

call_anthropic() {
  curl -fsS "http://127.0.0.1:${ANTHROPIC_GATEWAY_PORT}/v1/messages" \
    -H "x-api-key: ${virtual_key}" -H 'anthropic-version: 2023-06-01' -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg model "$model_alias" '{model:$model,max_tokens:32,system:"Be concise",messages:[{role:"user",content:"Say ok"}]}')"
}

call_gemini() {
  curl -fsS "http://127.0.0.1:${GEMINI_GATEWAY_PORT}/v1beta/models/${model_alias}:generateContent" \
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
    -d "$(jq -nc --arg model "$model_alias" '{model:$model,messages:[{role:"user",content:"Say ok"}]}')")
  [[ "$status" == 401 ]] || { echo "OpenAI Chat accepted a revoked user ($status)" >&2; return 1; }
  status=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${OPENAI_RESPONSES_PORT}/v1/responses" \
    -H "Authorization: Bearer ${virtual_key}" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg model "$model_alias" '{model:$model,input:"Say ok",store:false}')")
  [[ "$status" == 401 ]] || { echo "OpenAI Responses accepted a revoked user ($status)" >&2; return 1; }
  status=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${ANTHROPIC_GATEWAY_PORT}/v1/messages" \
    -H "x-api-key: ${virtual_key}" -H 'anthropic-version: 2023-06-01' -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg model "$model_alias" '{model:$model,max_tokens:32,messages:[{role:"user",content:"Say ok"}]}')")
  [[ "$status" == 401 ]] || { echo "Anthropic gateway accepted a revoked user ($status)" >&2; return 1; }
  status=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${GEMINI_GATEWAY_PORT}/v1beta/models/${model_alias}:generateContent" \
    -H "x-goog-api-key: ${virtual_key}" -H 'Content-Type: application/json' \
    -d '{"contents":[{"role":"user","parts":[{"text":"Say ok"}]}]}' )
  [[ "$status" == 401 ]] || { echo "Gemini gateway accepted a revoked user ($status)" >&2; return 1; }
}

assert_all_gateways_model_forbidden() {
  local status
  status=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${OPENAI_CHAT_PORT}/v1/chat/completions" \
    -H "Authorization: Bearer ${virtual_key}" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg model "$model_alias" '{model:$model,messages:[{role:"user",content:"Say ok"}]}')")
  [[ "$status" == 403 ]] || { echo "OpenAI Chat routed a disabled model ($status)" >&2; return 1; }
  status=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${OPENAI_RESPONSES_PORT}/v1/responses" \
    -H "Authorization: Bearer ${virtual_key}" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg model "$model_alias" '{model:$model,input:"Say ok",store:false}')")
  [[ "$status" == 403 ]] || { echo "OpenAI Responses routed a disabled model ($status)" >&2; return 1; }
  status=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${ANTHROPIC_GATEWAY_PORT}/v1/messages" \
    -H "x-api-key: ${virtual_key}" -H 'anthropic-version: 2023-06-01' -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg model "$model_alias" '{model:$model,max_tokens:32,messages:[{role:"user",content:"Say ok"}]}')")
  [[ "$status" == 403 ]] || { echo "Anthropic gateway routed a disabled model ($status)" >&2; return 1; }
  status=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${GEMINI_GATEWAY_PORT}/v1beta/models/${model_alias}:generateContent" \
    -H "x-goog-api-key: ${virtual_key}" -H 'Content-Type: application/json' \
    -d '{"contents":[{"role":"user","parts":[{"text":"Say ok"}]}]}' )
  [[ "$status" == 403 ]] || { echo "Gemini gateway routed a disabled model ($status)" >&2; return 1; }
}

set_user_status() {
  local new_status=$1
  curl -fsS -X PUT "http://127.0.0.1:${CONTROL_PORT}/v1/users/${user_id}" \
    -H "Authorization: Bearer ${admin_token}" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg name "$user_name" --arg email "$user_email" --arg status "$new_status" '{name:$name,email:$email,status:$status}')"
}

set_model_status() {
  local new_status=$1
  curl -fsS -X PUT "http://127.0.0.1:${CONTROL_PORT}/v1/models/${model_id}" \
    -H "Authorization: Bearer ${admin_token}" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg alias "$model_alias" --arg provider "$provider_id" --arg upstream "$upstream_model" --arg status "$new_status" \
      '{alias:$alias,provider_id:$provider,upstream_model:$upstream,status:$status}')"
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

# Model status is projected into the key domain. Every gateway uses the alias
# but authorizes the stable Model ID before resolving its Provider route.
disabled_model=$(set_model_status disabled)
jq -e '.status == "disabled" and .revision >= 2' <<<"$disabled_model" >/dev/null
assert_all_gateways_model_forbidden
enabled_model=$(set_model_status active)
jq -e '.status == "active" and .revision >= 3' <<<"$enabled_model" >/dev/null
assert_all_gateways

# The virtual-key service remains independently useful while the resource
# control plane is down. The BFF accepts already-synchronized user and Model IDs
# while its rendered form reports that both choice catalogs are unavailable.
scale_down_control_plane
status=$(curl -sS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-resource-outage-dashboard.html" -w '%{http_code}' "$ui_base/")
[[ "$status" == 200 ]] || { echo "WebUI dashboard failed during resource outage ($status)" >&2; exit 1; }
grep -Fq 'Degraded' "$TMP_DIR/ui-resource-outage-dashboard.html"
curl -fsS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-resource-outage-key-form.html" "$ui_base/virtual-keys/new"
grep -Fq 'User choices are unavailable' "$TMP_DIR/ui-resource-outage-key-form.html"
grep -Fq 'Model choices are unavailable' "$TMP_DIR/ui-resource-outage-key-form.html"
grep -Fq 'A known ID remains usable' "$TMP_DIR/ui-resource-outage-key-form.html"
outage_csrf=$(hidden_input_value "$TMP_DIR/ui-resource-outage-key-form.html" _csrf)
outage_operation=$(hidden_input_value "$TMP_DIR/ui-resource-outage-key-form.html" _operation)
independent_key_name="independence ${RUN_ID}"
status=$(curl -sS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-resource-outage-key.html" -w '%{http_code}' \
  -X POST "$ui_base/virtual-keys" --data-urlencode "_csrf=${outage_csrf}" --data-urlencode "_operation=${outage_operation}" \
  --data-urlencode "name=${independent_key_name}" --data-urlencode "user_id=${user_id}" \
  --data-urlencode "model_ids=${model_id}" --data-urlencode 'status=active')
[[ "$status" == 201 ]] || { echo "WebUI key creation failed during resource outage ($status)" >&2; exit 1; }
independent_key_id=$(curl -fsS "http://127.0.0.1:${VIRTUAL_KEY_CONTROL_PORT}/v1/virtual-keys" -H "Authorization: Bearer ${admin_token}" \
  | jq -er --arg name "$independent_key_name" '.data | map(select(.name == $name)) | first | .id')
curl -fsS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-resource-outage-key-delete.html" "$ui_base/virtual-keys/${independent_key_id}/delete"
delete_csrf=$(hidden_input_value "$TMP_DIR/ui-resource-outage-key-delete.html" _csrf)
delete_etag=$(hidden_input_value "$TMP_DIR/ui-resource-outage-key-delete.html" _etag)
status=$(curl -sS -b "$ui_cookie_jar" -o /dev/null -w '%{http_code}' -X POST \
  "$ui_base/virtual-keys/${independent_key_id}/delete" --data-urlencode "_csrf=${delete_csrf}" --data-urlencode "_etag=${delete_etag}")
[[ "$status" == 303 ]] || { echo "WebUI key deletion failed during resource outage ($status)" >&2; exit 1; }
independent_key_id=""
assert_all_gateways
restore_control_plane
ensure_control_forward

# Provider lifecycle remains available from the resource control plane while
# the virtual-key service is down. The WebUI reports the key-domain outage but
# leaves provider forms operational. User writes intentionally need subject sync.
scale_down_virtual_key_control_plane
status=$(curl -sS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-key-outage-dashboard.html" -w '%{http_code}' "$ui_base/")
[[ "$status" == 200 ]] || { echo "WebUI dashboard failed during key-control outage ($status)" >&2; exit 1; }
grep -Fq 'Virtual keys are temporarily unavailable' "$TMP_DIR/ui-key-outage-dashboard.html"
independent_provider_slug="probe-${RUN_ID}"
curl -fsS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-key-outage-provider-form.html" "$ui_base/providers/new"
outage_csrf=$(hidden_input_value "$TMP_DIR/ui-key-outage-provider-form.html" _csrf)
status=$(curl -sS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-key-outage-provider.body" -w '%{http_code}' \
  -X POST "$ui_base/providers" --data-urlencode "_csrf=${outage_csrf}" \
  --data-urlencode "slug=${independent_provider_slug}" --data-urlencode "name=independence ${RUN_ID}" \
  --data-urlencode 'kind=anthropic' --data-urlencode "base_url=http://${node_ip}:${PROVIDER_PORT}" \
  --data-urlencode 'api_version=2023-06-01' --data-urlencode "adapter_app_id=gwai-${independent_provider_slug}" \
  --data-urlencode 'secret_store=kubernetes' --data-urlencode "secret_name=${PROVIDER_SECRET}" \
  --data-urlencode 'secret_key=api-key' --data-urlencode 'status=active')
[[ "$status" == 303 ]] || { echo "WebUI provider creation failed during key-control outage ($status)" >&2; exit 1; }
independent_provider_id=$(curl -fsS "http://127.0.0.1:${CONTROL_PORT}/v1/providers" -H "Authorization: Bearer ${admin_token}" \
  | jq -er --arg slug "$independent_provider_slug" '.data | map(select(.slug == $slug)) | first | .id')
curl -fsS "http://127.0.0.1:${CONTROL_PORT}/v1/users/${user_id}" -H "Authorization: Bearer ${admin_token}" | jq -e --arg id "$user_id" '.id == $id' >/dev/null
curl -fsS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-key-outage-provider-delete.html" "$ui_base/providers/${independent_provider_id}/delete"
delete_csrf=$(hidden_input_value "$TMP_DIR/ui-key-outage-provider-delete.html" _csrf)
delete_etag=$(hidden_input_value "$TMP_DIR/ui-key-outage-provider-delete.html" _etag)
status=$(curl -sS -b "$ui_cookie_jar" -o /dev/null -w '%{http_code}' -X POST \
  "$ui_base/providers/${independent_provider_id}/delete" --data-urlencode "_csrf=${delete_csrf}" --data-urlencode "_etag=${delete_etag}")
[[ "$status" == 303 ]] || { echo "WebUI provider deletion failed during key-control outage ($status)" >&2; exit 1; }
independent_provider_id=""
independent_provider_slug=""
assert_all_gateways
restore_virtual_key_control_plane
ensure_virtual_key_forward

# Inference must remain available with both administrative services absent.
# Gateways read key/subject plus provider state; adapters read provider state.
scale_down_control_plane
scale_down_virtual_key_control_plane
status=$(curl -sS -b "$ui_cookie_jar" -o "$TMP_DIR/ui-both-controls-outage.html" -w '%{http_code}' "$ui_base/")
[[ "$status" == 200 ]] || { echo "WebUI dashboard failed while both control planes were down ($status)" >&2; exit 1; }
[[ $(grep -o 'Degraded' "$TMP_DIR/ui-both-controls-outage.html" | wc -l) -ge 3 ]] || { echo "WebUI did not report every unavailable admin domain" >&2; exit 1; }
assert_all_gateways

restore_virtual_key_control_plane
restore_control_plane
ensure_virtual_key_forward
ensure_control_forward

# Adapter restart verifies provider-specific Dapr discovery after endpoint rotation.
kubectl -n "$NAMESPACE" rollout restart "deployment/${ADAPTER_DEPLOYMENT}" >/dev/null
kubectl -n "$NAMESPACE" rollout status "deployment/${ADAPTER_DEPLOYMENT}" --timeout=60s >/dev/null
assert_all_gateways

status=$(curl -sS -b "$ui_cookie_jar" -c "$ui_cookie_jar" -D "$TMP_DIR/ui-logout.headers" -o "$TMP_DIR/ui-logout.body" -w '%{http_code}' \
  -X POST "$ui_base/logout" --data-urlencode "_csrf=${dashboard_csrf}")
[[ "$status" == 303 ]] || { echo "WebUI logout failed ($status)" >&2; exit 1; }
grep -Eiq '^set-cookie: gwai_admin_session=; .*Max-Age=0; .*HttpOnly; SameSite=Strict' "$TMP_DIR/ui-logout.headers"
status=$(curl -sS -b "$ui_cookie_jar" -o /dev/null -w '%{http_code}' "$ui_base/")
[[ "$status" == 303 ]] || { echo "WebUI session remained authenticated after logout ($status)" >&2; exit 1; }

echo "k3s e2e passed: WebUI user/model/provider/key lifecycle; Model-ID references and deletion fences; split control planes; fail-closed user/model revocation; three state domains; four gateway protocols; control-plane outage; provider invocation; secrets; and IR translation"
