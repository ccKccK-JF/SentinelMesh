#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
BUILD_DIR=${BUILD_DIR:-"${ROOT_DIR}/build/e2e"}
AGENT_BINARY=${AGENT_BINARY:-"${ROOT_DIR}/build/agent/sentinel-agent"}
GRPC_ADDRESS=${GRPC_ADDRESS:-"127.0.0.1:55051"}
HTTP_ADDRESS=${HTTP_ADDRESS:-"127.0.0.1:58080"}
NODE_ID=${NODE_ID:-"cpp-e2e"}

mkdir -p "${BUILD_DIR}"
go build -o "${BUILD_DIR}/control-plane" "${ROOT_DIR}/cmd/control-plane"

"${BUILD_DIR}/control-plane" \
  --grpc-address "${GRPC_ADDRESS}" \
  --http-address "${HTTP_ADDRESS}" \
  >"${BUILD_DIR}/control-plane.log" 2>&1 &
CONTROL_PLANE_PID=$!
trap 'kill "${CONTROL_PLANE_PID}" 2>/dev/null || true' EXIT

for _ in $(seq 1 50); do
  if curl --fail --silent "http://${HTTP_ADDRESS}/healthz" >/dev/null; then
    break
  fi
  sleep 0.1
done
curl --fail --silent "http://${HTTP_ADDRESS}/healthz" >/dev/null

"${AGENT_BINARY}" \
  --manager-address "${GRPC_ADDRESS}" \
  --node-id "${NODE_ID}" \
  --once

# A second process uses the same kernel boot ID. The Hello ACK must return the
# previously accepted sequence so the new stream continues at sequence 2.
"${AGENT_BINARY}" \
  --manager-address "${GRPC_ADDRESS}" \
  --node-id "${NODE_ID}" \
  --once

NODE_JSON=$(curl --fail --silent "http://${HTTP_ADDRESS}/v1/nodes/${NODE_ID}")
grep -q '"last_sequence":2' <<<"${NODE_JSON}"
grep -q '"cpu.utilization.percent"' <<<"${NODE_JSON}"
grep -q '"memory.utilization.percent"' <<<"${NODE_JSON}"

echo "C++ agent -> Go control plane e2e passed"
