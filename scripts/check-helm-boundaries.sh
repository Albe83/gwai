#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
HELM_BIN=${HELM:-helm}
render=$(mktemp)
long_render=$(mktemp)
trap 'rm -f "$render" "$long_render"' EXIT

"$HELM_BIN" template gwai "$ROOT_DIR/deploy/helm/gwai" >"$render"

component_doc() {
  local name=$1
  awk -v name="$name" 'BEGIN { RS="---" } $0 ~ "kind: Component" && $0 ~ "name: " name "([[:space:]]|$)" { print }' "$render"
}

deployment_doc() {
  local name=$1
  awk -v name="$name" 'BEGIN { RS="---" } $0 ~ "kind: Deployment" && $0 ~ "name: " name "([[:space:]]|$)" { print }' "$render"
}

configuration_doc() {
  local name=$1
  awk -v name="$name" 'BEGIN { RS="---" } $0 ~ "kind: Configuration" && $0 ~ "name: " name "([[:space:]]|$)" { print }' "$render"
}

[[ $(awk '$1 == "kind:" && $2 == "Component" { count++ } END { print count+0 }' "$render") -eq 3 ]]

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
grep -q '^  - gwai-virtual-key-control-plane$' <<<"$providers"
grep -q '^  - "gwai-anthropic"$' <<<"$providers"

grep -q '^  - gwai-virtual-key-control-plane$' <<<"$keys"
! grep -q '^  - gwai-control-plane$' <<<"$keys"
! grep -q 'gwai-anthropic"$' <<<"$keys"

adapter=$(deployment_doc gwai-anthropic-primary)
grep -q 'GWAI_PROVIDER_STATE_STORE' <<<"$adapter"
! grep -Eq 'GWAI_VIRTUAL_KEY_STATE_STORE|GWAI_CONTROL_STATE_STORE' <<<"$adapter"
grep -q 'name: gwai-app-api-token' <<<"$adapter"
! grep -q 'name: gwai-virtual-key-app-api-token' <<<"$adapter"

gateway=$(deployment_doc gwai-openai-gateway)
grep -q 'GWAI_PROVIDER_STATE_STORE' <<<"$gateway"
grep -q 'GWAI_VIRTUAL_KEY_STATE_STORE' <<<"$gateway"
! grep -q 'GWAI_CONTROL_STATE_STORE' <<<"$gateway"

resource_control=$(deployment_doc gwai-control-plane)
grep -q 'type: Recreate' <<<"$resource_control"
grep -q 'GWAI_CONTROL_STATE_STORE' <<<"$resource_control"
grep -q 'GWAI_PROVIDER_STATE_STORE' <<<"$resource_control"
! grep -q 'GWAI_VIRTUAL_KEY_STATE_STORE' <<<"$resource_control"

key_control=$(deployment_doc gwai-virtual-key-control-plane)
grep -q 'type: Recreate' <<<"$key_control"
grep -q 'GWAI_VIRTUAL_KEY_STATE_STORE' <<<"$key_control"
grep -q 'GWAI_PROVIDER_STATE_STORE' <<<"$key_control"
! grep -q 'GWAI_CONTROL_STATE_STORE' <<<"$key_control"
grep -q 'name: gwai-virtual-key-app-api-token' <<<"$key_control"
! grep -q 'name: gwai-app-api-token' <<<"$key_control"

# A custom retry policy must not turn domain 409 responses from subject fencing
# into long retries and an upstream timeout.
resiliency=$(awk 'BEGIN { RS="---" } /kind: Resiliency/ { print }' "$render")
! grep -q '"gwai-virtual-key-control-plane":' <<<"$resiliency"

key_configuration=$(configuration_doc gwai-virtual-key-control-plane)
grep -q 'appId: gwai-control-plane' <<<"$key_configuration"
grep -q 'name: /internal/v1/subjects/sync' <<<"$key_configuration"
grep -q 'name: /internal/v1/subjects/fence' <<<"$key_configuration"
[[ $(grep -c 'action: allow' <<<"$key_configuration") -eq 2 ]]

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
  --set 'dapr.stateStores.virtualKeys.redisDB=16' >/dev/null 2>&1; then
  echo "out-of-range bundled Valkey database was accepted" >&2
  exit 1
fi

echo "Helm state boundaries verified"
