#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
BUILD_DIR=${BUILD_DIR:-"${ROOT_DIR}/build/routing-e2e"}
GRPC_ADDRESS=${GRPC_ADDRESS:-"127.0.0.1:65051"}
HTTP_ADDRESS=${HTTP_ADDRESS:-"127.0.0.1:62080"}
CONTROL_PID=""
GOOD_PID=""
HOT_PID=""

cleanup() {
  for process in "${GOOD_PID}" "${HOT_PID}" "${CONTROL_PID}"; do
    if [[ -n "${process}" ]]; then
      kill "${process}" 2>/dev/null || true
      wait "${process}" 2>/dev/null || true
    fi
  done
}
trap cleanup EXIT

for command in curl go python3; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "${command} is required for routing e2e" >&2
    exit 1
  fi
done

mkdir -p "${BUILD_DIR}"
go build -o "${BUILD_DIR}/control-plane" "${ROOT_DIR}/cmd/control-plane"
go build -o "${BUILD_DIR}/sim-agent" "${ROOT_DIR}/cmd/sim-agent"
go build -o "${BUILD_DIR}/fake-gateway" "${ROOT_DIR}/cmd/fake-gateway"

"${BUILD_DIR}/control-plane" \
  --grpc-address "${GRPC_ADDRESS}" \
  --http-address "${HTTP_ADDRESS}" \
  >"${BUILD_DIR}/control-plane.log" 2>&1 &
CONTROL_PID=$!
for _ in $(seq 1 50); do
  if curl --fail --silent "http://${HTTP_ADDRESS}/healthz" >/dev/null; then
    break
  fi
  sleep 0.1
done
curl --fail --silent "http://${HTTP_ADDRESS}/healthz" >/dev/null

"${BUILD_DIR}/sim-agent" --address "${GRPC_ADDRESS}" --node-id game-good \
  --count 50 --interval 100ms --cpu 20 --memory 30 --load 0.2 --disk 10 \
  >"${BUILD_DIR}/good.log" 2>&1 &
GOOD_PID=$!
"${BUILD_DIR}/sim-agent" --address "${GRPC_ADDRESS}" --node-id game-hot \
  --count 50 --interval 100ms --cpu 100 --memory 30 --load 0.2 --disk 10 \
  >"${BUILD_DIR}/hot.log" 2>&1 &
HOT_PID=$!

ROUTING_FILE="${BUILD_DIR}/routing.json"
for _ in $(seq 1 50); do
  curl --fail --silent "http://${HTTP_ADDRESS}/v1/routing" >"${ROUTING_FILE}"
  if python3 - "${ROUTING_FILE}" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    nodes = {node["node_id"]: node for node in (json.load(source)["nodes"] or [])}
raise SystemExit(0 if nodes.get("game-good", {}).get("eligible") and
                 nodes.get("game-hot", {}).get("reason") == "health_unhealthy"
                 else 1)
PY
  then
    break
  fi
  sleep 0.1
done

"${BUILD_DIR}/fake-gateway" \
  --routing-url "http://${HTTP_ADDRESS}/v1/routing" \
  --requests 10000 >"${BUILD_DIR}/allocations.json"

python3 - "${ROUTING_FILE}" "${BUILD_DIR}/allocations.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    routing = json.load(source)
with open(sys.argv[2], encoding="utf-8") as source:
    allocation = json.load(source)
nodes = {node["node_id"]: node for node in routing["nodes"]}
assert routing["version"] > 0, routing
assert nodes["game-good"]["eligible"], nodes
assert nodes["game-good"]["weight"] == 10000, nodes
assert not nodes["game-hot"]["eligible"], nodes
assert nodes["game-hot"]["weight"] == 0, nodes
assert allocation["version"] == routing["version"], (routing, allocation)
assert allocation["allocations"] == {"game-good": 10000}, allocation
print("routing e2e passed", {
    "version": routing["version"],
    "good_weight": nodes["game-good"]["weight"],
    "hot_reason": nodes["game-hot"]["reason"],
    "allocations": allocation["allocations"],
})
PY
