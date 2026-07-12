#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
HELM_BIN=${HELM:-helm}
render=$(mktemp)
long_render=$(mktemp)
route_render=$(mktemp)
secret_render=$(mktemp)
trap 'rm -f "$render" "$long_render" "$route_render" "$secret_render"' EXIT

"$HELM_BIN" template gwai "$ROOT_DIR/deploy/helm/gwai" >"$render"

component_doc() {
  local name=$1
  awk -v name="$name" 'BEGIN { RS="---" } $0 ~ "kind: Component" && $0 ~ "name: " name "([[:space:]]|$)" { print }' "$render"
}

deployment_doc() {
  local name=$1
  awk -v name="$name" 'BEGIN { RS="---" } $0 ~ "kind: Deployment" && $0 ~ "name: " name "([[:space:]]|$)" { print }' "$render"
}

service_doc() {
  local name=$1
  awk -v name="$name" 'BEGIN { RS="---" } $0 ~ "kind: Service" && $0 ~ "name: " name "([[:space:]]|$)" { print }' "$render"
}

configuration_doc() {
  local name=$1
  awk -v name="$name" 'BEGIN { RS="---" } $0 ~ "kind: Configuration" && $0 ~ "name: " name "([[:space:]]|$)" { print }' "$render"
}

httproute_doc() {
  local name=$1
  local source=${2:-$render}
  awk -v name="$name" 'BEGIN { RS="---" } $0 ~ "kind: HTTPRoute" && $0 ~ "name: " name "([[:space:]]|$)" { print }' "$source"
}

policy_doc() {
  local configuration=$1
  local app_id=$2
  awk -v target="      - appId: $app_id" '
    /^      - appId:/ {
      if (capturing) exit
      capturing = ($0 == target)
    }
    capturing { print }
  ' <<<"$configuration"
}

[[ $(awk '$1 == "kind:" && $2 == "Component" { count++ } END { print count+0 }' "$render") -eq 3 ]]
[[ $(awk '$1 == "kind:" && $2 == "HTTPRoute" { count++ } END { print count+0 }' "$render") -eq 0 ]]

control=$(component_doc gwai-control-state)
providers=$(component_doc gwai-provider-state)
keys=$(component_doc gwai-virtual-key-state)

grep -A1 'name: redisDB' <<<"$control" | grep -q 'value: "0"'
grep -A1 'name: redisDB' <<<"$providers" | grep -q 'value: "1"'
grep -A1 'name: redisDB' <<<"$keys" | grep -q 'value: "2"'
for component in "$control" "$providers" "$keys"; do
  grep -A1 'name: keyPrefix' <<<"$component" | grep -q 'value: "name"'
done

[[ $(grep -c '^  - ' <<<"$control") -eq 1 ]]
grep -q '^  - gwai-control-plane$' <<<"$control"

grep -q '^  - gwai-control-plane$' <<<"$providers"
grep -q '^  - "gwai-anthropic"$' <<<"$providers"
! grep -q '^  - gwai-virtual-key-control-plane$' <<<"$providers"
! grep -q 'gwai-admin-webui' <<<"$providers"

grep -q '^  - gwai-virtual-key-control-plane$' <<<"$keys"
! grep -q '^  - gwai-control-plane$' <<<"$keys"
! grep -q 'gwai-anthropic"$' <<<"$keys"
! grep -q 'gwai-admin-webui' <<<"$keys"

adapter=$(deployment_doc gwai-anthropic-primary)
grep -q 'GWAI_PROVIDER_STATE_STORE' <<<"$adapter"
grep -q 'GWAI_ADAPTER_APP_ID' <<<"$adapter"
grep -q 'GWAI_PROVIDER_BASE_URL' <<<"$adapter"
grep -q 'GWAI_PROVIDER_API_VERSION' <<<"$adapter"
grep -q 'GWAI_PROVIDER_SECRET_STORE' <<<"$adapter"
grep -q 'GWAI_PROVIDER_SECRET_NAME' <<<"$adapter"
grep -q 'GWAI_PROVIDER_SECRET_KEY' <<<"$adapter"
! grep -q 'GWAI_PROVIDER_SLUG' <<<"$adapter"
! grep -Eq 'GWAI_VIRTUAL_KEY_STATE_STORE|GWAI_CONTROL_STATE_STORE' <<<"$adapter"
grep -q 'name: gwai-app-api-token' <<<"$adapter"
! grep -q 'name: gwai-virtual-key-app-api-token' <<<"$adapter"

"$HELM_BIN" template gwai "$ROOT_DIR/deploy/helm/gwai" \
  --namespace gwai \
  --set-json 'providerAdapters=[{"name":"primary","kind":"anthropic","appID":"gwai-anthropic","image":{"repository":"gwai-anthropic-adapter"},"replicas":1,"port":8080,"maxBodyBytes":10485760,"providerTimeout":"5m","defaultMaxOutputTokens":4096,"maxOutputTokens":0,"upstream":{"baseURL":"https://api.anthropic.com","apiVersion":"2023-06-01","secretRef":{"store":"kubernetes","name":"gwai-anthropic","key":"api-key","namespace":"provider-secrets"}},"resources":{}}]' \
  >"$secret_render"
secret_role=$(awk 'BEGIN { RS="---" } /kind: Role/ && /name: gwai-anthropic-primary-secrets/ { print }' "$secret_render")
secret_binding=$(awk 'BEGIN { RS="---" } /kind: RoleBinding/ && /name: gwai-anthropic-primary-secrets/ { print }' "$secret_render")
grep -q '^  namespace: "provider-secrets"$' <<<"$secret_role"
grep -q '^  namespace: "provider-secrets"$' <<<"$secret_binding"
grep -A3 '^subjects:' <<<"$secret_binding" | grep -q '^    namespace: gwai$'

gateway=$(deployment_doc gwai-openai-gateway)
grep -q 'GWAI_PROVIDER_STATE_STORE' <<<"$gateway"
grep -q 'GWAI_VIRTUAL_KEY_STATE_STORE' <<<"$gateway"
! grep -q 'GWAI_CONTROL_STATE_STORE' <<<"$gateway"

resource_control=$(deployment_doc gwai-control-plane)
grep -q 'type: Recreate' <<<"$resource_control"
grep -q 'dapr.io/app-port: "8080"' <<<"$resource_control"
grep -q 'dapr.io/app-protocol: http' <<<"$resource_control"
grep -q 'GWAI_CONTROL_STATE_STORE' <<<"$resource_control"
grep -q 'GWAI_PROVIDER_STATE_STORE' <<<"$resource_control"
! grep -q 'GWAI_VIRTUAL_KEY_STATE_STORE' <<<"$resource_control"

key_control=$(deployment_doc gwai-virtual-key-control-plane)
grep -q 'type: Recreate' <<<"$key_control"
grep -q 'GWAI_VIRTUAL_KEY_STATE_STORE' <<<"$key_control"
! grep -q 'GWAI_PROVIDER_STATE_STORE' <<<"$key_control"
! grep -q 'GWAI_CONTROL_STATE_STORE' <<<"$key_control"
grep -q 'name: gwai-virtual-key-app-api-token' <<<"$key_control"
! grep -q 'name: gwai-app-api-token' <<<"$key_control"

admin_webui=$(deployment_doc gwai-admin-webui)
grep -q 'type: Recreate' <<<"$admin_webui"
grep -q 'replicas: 1' <<<"$admin_webui"
grep -q 'dapr.io/app-id: gwai-admin-webui' <<<"$admin_webui"
grep -q 'dapr.io/app-port: "8080"' <<<"$admin_webui"
grep -q 'dapr.io/disable-builtin-k8s-secret-store: "true"' <<<"$admin_webui"
grep -q 'automountServiceAccountToken: false' <<<"$admin_webui"
grep -q 'GWAI_RESOURCE_CONTROL_APP_ID' <<<"$admin_webui"
grep -q 'GWAI_VIRTUAL_KEY_CONTROL_APP_ID' <<<"$admin_webui"
grep -q 'GWAI_ADMIN_UI_SESSION_TTL' <<<"$admin_webui"
grep -q 'GWAI_ADMIN_UI_SECURE_COOKIES' <<<"$admin_webui"
grep -q 'GWAI_ADMIN_TOKEN' <<<"$admin_webui"
grep -q 'DAPR_API_TOKEN' <<<"$admin_webui"
! grep -Eq 'GWAI_(CONTROL_STATE_STORE|PROVIDER_STATE_STORE|VIRTUAL_KEY_STATE_STORE)' <<<"$admin_webui"
! grep -Eq 'APP_API_TOKEN|gwai-app-api-token|gwai-virtual-key-app-api-token' <<<"$admin_webui"
! grep -q 'dapr.io/app-token-secret' <<<"$admin_webui"

admin_service=$(service_doc gwai-admin-webui)
grep -q '^  type: ClusterIP$' <<<"$admin_service"

"$HELM_BIN" template gwai "$ROOT_DIR/deploy/helm/gwai" \
  --set 'adminWebUI.httpRoute.enabled=true' \
  --set 'adminWebUI.secureCookies=true' \
  --set-string 'adminWebUI.httpRoute.parentRefs[0].name=edge-gateway' \
  --set-string 'adminWebUI.httpRoute.parentRefs[0].namespace=gateway-system' \
  --set-string 'adminWebUI.httpRoute.parentRefs[0].sectionName=https' \
  --set-string 'adminWebUI.httpRoute.hostnames[0]=admin.example.com' \
  --set-string 'adminWebUI.httpRoute.annotations.test\.gwai\.dev/exposure=https' \
  >"$route_render"
[[ $(awk '$1 == "kind:" && $2 == "HTTPRoute" { count++ } END { print count+0 }' "$route_render") -eq 1 ]]
admin_route=$(httproute_doc gwai-admin-webui "$route_render")
grep -q '^apiVersion: gateway.networking.k8s.io/v1$' <<<"$admin_route"
grep -q '^kind: HTTPRoute$' <<<"$admin_route"
grep -q '"test.gwai.dev/exposure": "https"' <<<"$admin_route"
grep -q '^    - group: gateway.networking.k8s.io$' <<<"$admin_route"
grep -q '^      kind: Gateway$' <<<"$admin_route"
grep -q '^      name: "edge-gateway"$' <<<"$admin_route"
grep -q '^      namespace: "gateway-system"$' <<<"$admin_route"
grep -q '^      sectionName: "https"$' <<<"$admin_route"
grep -q '^    - "admin.example.com"$' <<<"$admin_route"
grep -q '^            type: PathPrefix$' <<<"$admin_route"
grep -q '^            value: /$' <<<"$admin_route"
grep -q '^          kind: Service$' <<<"$admin_route"
grep -q '^          name: gwai-admin-webui$' <<<"$admin_route"
grep -q '^          port: 8080$' <<<"$admin_route"

# A custom retry policy must not turn domain 409 responses from user/Model
# fencing into long retries and an upstream timeout.
resiliency=$(awk 'BEGIN { RS="---" } /kind: Resiliency/ { print }' "$render")
! grep -q '"gwai-virtual-key-control-plane":' <<<"$resiliency"
! grep -q 'gwai-admin-webui' <<<"$resiliency"

resource_configuration=$(configuration_doc gwai-control-plane)
resource_ui_policy=$(policy_doc "$resource_configuration" gwai-admin-webui)
grep -q 'name: /v1/users$' <<<"$resource_ui_policy"
grep -q 'name: /v1/users/\*$' <<<"$resource_ui_policy"
grep -q 'name: /v1/providers$' <<<"$resource_ui_policy"
grep -q 'name: /v1/providers/\*$' <<<"$resource_ui_policy"
grep -q 'name: /v1/models$' <<<"$resource_ui_policy"
grep -q 'name: /v1/models/\*$' <<<"$resource_ui_policy"
[[ $(grep -Fc 'httpVerb: ["GET", "POST", "PUT", "DELETE"]' <<<"$resource_ui_policy") -eq 3 ]]
[[ $(grep -Fc 'httpVerb: ["GET", "PUT", "DELETE"]' <<<"$resource_ui_policy") -eq 3 ]]
[[ $(grep -c 'action: allow' <<<"$resource_ui_policy") -eq 6 ]]
! grep -q '/internal/' <<<"$resource_ui_policy"

key_configuration=$(configuration_doc gwai-virtual-key-control-plane)
resource_subject_policy=$(policy_doc "$key_configuration" gwai-control-plane)
grep -q 'name: /internal/v1/subjects/sync' <<<"$resource_subject_policy"
grep -q 'name: /internal/v1/subjects/fence' <<<"$resource_subject_policy"
grep -q 'name: /internal/v1/model-subjects/sync' <<<"$resource_subject_policy"
grep -q 'name: /internal/v1/model-subjects/fence' <<<"$resource_subject_policy"
[[ $(grep -c 'action: allow' <<<"$resource_subject_policy") -eq 4 ]]

key_ui_policy=$(policy_doc "$key_configuration" gwai-admin-webui)
grep -q 'name: /v1/virtual-keys$' <<<"$key_ui_policy"
grep -q 'name: /v1/virtual-keys/\*$' <<<"$key_ui_policy"
[[ $(grep -Fc 'httpVerb: ["GET", "POST", "PUT", "DELETE"]' <<<"$key_ui_policy") -eq 1 ]]
[[ $(grep -Fc 'httpVerb: ["GET", "PUT", "DELETE"]' <<<"$key_ui_policy") -eq 1 ]]
[[ $(grep -c 'action: allow' <<<"$key_ui_policy") -eq 2 ]]
! grep -q '/internal/' <<<"$key_ui_policy"

admin_configuration=$(configuration_doc gwai-admin-webui)
grep -q 'name: invoke' <<<"$admin_configuration"
[[ $(grep -c '^      - name:' <<<"$admin_configuration") -eq 1 ]]
! grep -Eq 'name: (state|secrets)' <<<"$admin_configuration"
grep -q 'defaultAction: deny' <<<"$admin_configuration"

# Long valid release/fullname inputs must not collapse distinct app IDs after
# Kubernetes' 63-character name limit.
long_release=$(printf 'a%.0s' {1..53})
"$HELM_BIN" template "$long_release" "$ROOT_DIR/deploy/helm/gwai" >"$long_render"
for kind in Deployment Service Configuration; do
  duplicates=$(awk -v wanted="$kind" 'BEGIN { RS="---"; FS="\n" } $0 ~ "kind: " wanted { for (i=1; i<=NF; i++) if ($i ~ /^  name: /) { sub(/^  name: /, "", $i); print $i; break } }' "$long_render" | sort | uniq -d)
  [[ -z "$duplicates" ]]
done
duplicate_app_ids=$(sed -n 's/^[[:space:]]*dapr.io\/app-id: *//p' "$long_render" | sort | uniq -d)
[[ -z "$duplicate_app_ids" ]]

long_fullname=$(printf 'b%.0s' {1..63})
"$HELM_BIN" template gwai "$ROOT_DIR/deploy/helm/gwai" --set-string fullnameOverride="$long_fullname" >"$long_render"
for kind in Deployment Service Configuration Secret; do
  duplicates=$(awk -v wanted="$kind" 'BEGIN { RS="---"; FS="\n" } $0 ~ "kind: " wanted { for (i=1; i<=NF; i++) if ($i ~ /^  name: /) { sub(/^  name: /, "", $i); print $i; break } }' "$long_render" | sort | uniq -d)
  [[ -z "$duplicates" ]]
done
duplicate_app_ids=$(sed -n 's/^[[:space:]]*dapr.io\/app-id: *//p' "$long_render" | sort | uniq -d)
[[ -z "$duplicate_app_ids" ]]

if "$HELM_BIN" template gwai "$ROOT_DIR/deploy/helm/gwai" \
  --set-string 'providerAdapters[0].appID=gwai-control-plane' >/dev/null 2>&1; then
  echo "reserved Dapr app ID was accepted" >&2
  exit 1
fi
if "$HELM_BIN" template gwai "$ROOT_DIR/deploy/helm/gwai" \
  --set-string 'providerAdapters[0].appID=gwai-admin-webui' >/dev/null 2>&1; then
  echo "admin WebUI Dapr app ID was accepted for an adapter" >&2
  exit 1
fi
if "$HELM_BIN" template gwai "$ROOT_DIR/deploy/helm/gwai" \
  --set-string 'providerAdapters[0].upstream.baseURL=' >/dev/null 2>&1; then
  echo "empty adapter upstream base URL was accepted" >&2
  exit 1
fi
if "$HELM_BIN" template gwai "$ROOT_DIR/deploy/helm/gwai" \
  --set-string 'providerAdapters[0].upstream.secretRef.name=' >/dev/null 2>&1; then
  echo "empty adapter upstream secret name was accepted" >&2
  exit 1
fi
if "$HELM_BIN" template gwai "$ROOT_DIR/deploy/helm/gwai" \
  --set-string 'providerAdapters[0].upstream.secretRef.namespace=INVALID_NAMESPACE' >/dev/null 2>&1; then
  echo "invalid adapter Secret namespace was accepted" >&2
  exit 1
fi
if "$HELM_BIN" template gwai "$ROOT_DIR/deploy/helm/gwai" \
  --set 'adminWebUI.replicas=2' >/dev/null 2>&1; then
  echo "multiple admin WebUI replicas were accepted" >&2
  exit 1
fi
if "$HELM_BIN" template gwai "$ROOT_DIR/deploy/helm/gwai" \
  --set 'adminWebUI.sessionTTLSeconds=299' >/dev/null 2>&1; then
  echo "too-short admin WebUI session TTL was accepted" >&2
  exit 1
fi
if "$HELM_BIN" template gwai "$ROOT_DIR/deploy/helm/gwai" \
  --set 'adminWebUI.requestTimeoutSeconds=0' >/dev/null 2>&1; then
  echo "non-positive admin WebUI request timeout was accepted" >&2
  exit 1
fi
if "$HELM_BIN" template gwai "$ROOT_DIR/deploy/helm/gwai" \
  --set 'adminWebUI.requestTimeoutSeconds=20' \
  --set 'adminWebUI.terminationGracePeriodSeconds=29' >/dev/null 2>&1; then
  echo "admin WebUI termination grace shorter than its safe request window was accepted" >&2
  exit 1
fi
if "$HELM_BIN" template gwai "$ROOT_DIR/deploy/helm/gwai" \
  --set 'adminWebUI.port=70000' >/dev/null 2>&1; then
  echo "out-of-range admin WebUI port was accepted" >&2
  exit 1
fi
if "$HELM_BIN" template gwai "$ROOT_DIR/deploy/helm/gwai" \
  --set-string 'adminWebUI.service.type=LoadBalancer' >/dev/null 2>&1; then
  echo "externally exposed admin WebUI service was accepted" >&2
  exit 1
fi
if "$HELM_BIN" template gwai "$ROOT_DIR/deploy/helm/gwai" \
  --set 'adminWebUI.httpRoute.enabled=true' \
  --set-string 'adminWebUI.httpRoute.parentRefs[0].name=edge-gateway' >/dev/null 2>&1; then
  echo "HTTPRoute without secure WebUI cookies was accepted" >&2
  exit 1
fi
if "$HELM_BIN" template gwai "$ROOT_DIR/deploy/helm/gwai" \
  --set 'adminWebUI.httpRoute.enabled=true' \
  --set 'adminWebUI.secureCookies=true' >/dev/null 2>&1; then
  echo "HTTPRoute without a parent Gateway was accepted" >&2
  exit 1
fi
if "$HELM_BIN" template gwai "$ROOT_DIR/deploy/helm/gwai" \
  --set 'adminWebUI.httpRoute.enabled=true' \
  --set 'adminWebUI.secureCookies=true' \
  --set-string 'adminWebUI.httpRoute.parentRefs[0].name=' >/dev/null 2>&1; then
  echo "HTTPRoute with an empty parent Gateway name was accepted" >&2
  exit 1
fi
if "$HELM_BIN" template gwai "$ROOT_DIR/deploy/helm/gwai" \
  --set 'adminWebUI.httpRoute.enabled=true' \
  --set 'adminWebUI.secureCookies=true' \
  --set-string 'adminWebUI.httpRoute.parentRefs[0].name=edge-gateway' \
  --set-string 'adminWebUI.httpRoute.hostnames[0]=admin.example.com' >/dev/null 2>&1; then
  echo "HTTPRoute without an explicit HTTPS listener section was accepted" >&2
  exit 1
fi
if "$HELM_BIN" template gwai "$ROOT_DIR/deploy/helm/gwai" \
  --set 'adminWebUI.httpRoute.enabled=true' \
  --set 'adminWebUI.secureCookies=true' \
  --set-string 'adminWebUI.httpRoute.parentRefs[0].name=edge-gateway' \
  --set-string 'adminWebUI.httpRoute.parentRefs[0].sectionName=https' >/dev/null 2>&1; then
  echo "HTTPRoute without an explicit hostname was accepted" >&2
  exit 1
fi
if "$HELM_BIN" template gwai "$ROOT_DIR/deploy/helm/gwai" \
  --set 'adminWebUI.httpRoute.enabled=true' \
  --set 'adminWebUI.secureCookies=true' \
  --set-string 'adminWebUI.httpRoute.parentRefs[0].name=edge-gateway' \
  --set-string 'adminWebUI.httpRoute.parentRefs[0].sectionName=https' \
  --set-string 'adminWebUI.httpRoute.hostnames[0]=admin.example.com' \
  --set 'adminWebUI.httpRoute.parentRefs[0].port=443' >/dev/null 2>&1; then
  echo "HTTPRoute with unsupported port-based listener selection was accepted" >&2
  exit 1
fi
if "$HELM_BIN" template gwai "$ROOT_DIR/deploy/helm/gwai" \
  --set 'adminWebUI.httpRoute.enabled=true' \
  --set 'adminWebUI.secureCookies=true' \
  --set-string 'adminWebUI.httpRoute.parentRefs[0].name=edge-gateway' \
  --set-string 'adminWebUI.httpRoute.parentRefs[0].sectionName=https' \
  --set-string 'adminWebUI.httpRoute.hostnames[0]=' >/dev/null 2>&1; then
  echo "HTTPRoute with an empty hostname was accepted" >&2
  exit 1
fi
if "$HELM_BIN" template gwai "$ROOT_DIR/deploy/helm/gwai" \
  --set 'adminWebUI.httpRoute.enabled=true' \
  --set 'adminWebUI.secureCookies=true' \
  --set-string 'adminWebUI.httpRoute.parentRefs[0].name=edge-gateway' \
  --set-string 'adminWebUI.httpRoute.parentRefs[0].sectionName=https' \
  --set-string 'adminWebUI.httpRoute.hostnames[0]=admin.example.com:443' >/dev/null 2>&1; then
  echo "HTTPRoute with an invalid hostname was accepted" >&2
  exit 1
fi
if "$HELM_BIN" template gwai "$ROOT_DIR/deploy/helm/gwai" \
  --set 'adminWebUI.httpRoute.enabled=true' \
  --set 'adminWebUI.secureCookies=true' \
  --set-string 'adminWebUI.httpRoute.parentRefs[0].name=edge-gateway' \
  --set-string 'adminWebUI.httpRoute.parentRefs[0].sectionName=https' \
  --set-string 'adminWebUI.httpRoute.parentRefs[1].name=edge-gateway' \
  --set-string 'adminWebUI.httpRoute.parentRefs[1].sectionName=https' \
  --set-string 'adminWebUI.httpRoute.hostnames[0]=admin.example.com' >/dev/null 2>&1; then
  echo "HTTPRoute with a duplicate parent Gateway listener was accepted" >&2
  exit 1
fi
if "$HELM_BIN" template gwai "$ROOT_DIR/deploy/helm/gwai" \
  --set 'adminWebUI.httpRoute.enabled=true' \
  --set 'adminWebUI.secureCookies=true' \
  --set-string 'adminWebUI.httpRoute.parentRefs[0].name=edge-gateway' \
  --set-string 'adminWebUI.httpRoute.parentRefs[0].sectionName=https' \
  --set-string 'adminWebUI.httpRoute.hostnames[0]=admin.example.com' \
  --set-string 'adminWebUI.httpRoute.hostnames[1]=admin.example.com' >/dev/null 2>&1; then
  echo "HTTPRoute with a duplicate hostname was accepted" >&2
  exit 1
fi
if "$HELM_BIN" template gwai "$ROOT_DIR/deploy/helm/gwai" \
  --set 'adminWebUI.httpRoute.enabled=true' \
  --set 'adminWebUI.secureCookies=true' \
  --set-string 'adminWebUI.httpRoute.parentRefs[32].name=edge-gateway' \
  --set-string 'adminWebUI.httpRoute.parentRefs[32].sectionName=https' \
  --set-string 'adminWebUI.httpRoute.hostnames[0]=admin.example.com' >/dev/null 2>&1; then
  echo "HTTPRoute with more than 32 parent Gateways was accepted" >&2
  exit 1
fi
if "$HELM_BIN" template gwai "$ROOT_DIR/deploy/helm/gwai" \
  --set 'adminWebUI.httpRoute.enabled=true' \
  --set 'adminWebUI.secureCookies=true' \
  --set-string 'adminWebUI.httpRoute.parentRefs[0].name=edge-gateway' \
  --set-string 'adminWebUI.httpRoute.parentRefs[0].sectionName=https' \
  --set-string 'adminWebUI.httpRoute.hostnames[16]=admin.example.com' >/dev/null 2>&1; then
  echo "HTTPRoute with more than 16 hostnames was accepted" >&2
  exit 1
fi
if "$HELM_BIN" template gwai "$ROOT_DIR/deploy/helm/gwai" \
  --set 'adminWebUI.httpRoute.enabled=true' \
  --set 'adminWebUI.secureCookies=true' \
  --set-string 'adminWebUI.httpRoute.parentRefs[0].name=edge-gateway' \
  --set-string 'adminWebUI.httpRoute.parentRefs[0].sectionName=https' \
  --set-string 'adminWebUI.httpRoute.hostnames[0]=admin.example.com' \
  --set 'adminWebUI.httpRoute.annotations.example=1' >/dev/null 2>&1; then
  echo "HTTPRoute with a non-string annotation value was accepted" >&2
  exit 1
fi
if "$HELM_BIN" template gwai "$ROOT_DIR/deploy/helm/gwai" \
  --set 'dapr.stateStores.virtualKeys.redisDB=16' >/dev/null 2>&1; then
  echo "out-of-range bundled Valkey database was accepted" >&2
  exit 1
fi

echo "Helm state boundaries verified"
