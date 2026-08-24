#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
AGENT_BINARY=${AGENT_BINARY:-"${ROOT_DIR}/build/agent/sentinel-agent"}
OUTPUT_FILE=$(mktemp /tmp/sentinelmesh-pressure-output.XXXXXX)
STRESS_LOG=$(mktemp /tmp/sentinelmesh-pressure-stress.XXXXXX)
STRESS_PID=""

cleanup() {
  if [[ -n "${STRESS_PID}" ]]; then
    kill "${STRESS_PID}" 2>/dev/null || true
    wait "${STRESS_PID}" 2>/dev/null || true
  fi
  rm -f "${OUTPUT_FILE}" "${STRESS_LOG}"
}
trap cleanup EXIT

for command in python3 stress-ng timeout; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "${command} is required for resource pressure testing" >&2
    exit 1
  fi
done

BASELINE=$("${AGENT_BINARY}" --stdout --once)
stress-ng --cpu 0 --cpu-load 50 --vm 1 --vm-bytes 256M --timeout 6s \
  >"${STRESS_LOG}" 2>&1 &
STRESS_PID=$!
set +e
timeout 5 "${AGENT_BINARY}" --stdout --interval 1 >"${OUTPUT_FILE}"
AGENT_STATUS=$?
set -e
wait "${STRESS_PID}"
STRESS_PID=""
if [[ ${AGENT_STATUS} -ne 0 && ${AGENT_STATUS} -ne 124 ]]; then
  cat "${OUTPUT_FILE}" >&2
  exit "${AGENT_STATUS}"
fi

python3 - "${BASELINE}" "${OUTPUT_FILE}" <<'PY'
import json
import sys

baseline = json.loads(sys.argv[1])["metrics"]["cpu.utilization.percent"]
with open(sys.argv[2], encoding="utf-8") as output:
    snapshots = [json.loads(line) for line in output]
maximum = max(item["metrics"]["cpu.utilization.percent"] for item in snapshots)
delta = maximum - baseline
if maximum < 20 or delta < 10:
    raise SystemExit(
        f"CPU pressure was not observable: baseline={baseline:.2f} max={maximum:.2f}"
    )
print("resource pressure test passed", {
    "baseline_cpu_percent": round(baseline, 2),
    "max_cpu_percent": round(maximum, 2),
    "delta_percent": round(delta, 2),
    "samples": len(snapshots),
})
PY
