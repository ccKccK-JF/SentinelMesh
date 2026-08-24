#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
AGENT_BINARY=${AGENT_BINARY:-"${ROOT_DIR}/build/agent/sentinel-agent"}
DURATION_SECONDS=${DURATION_SECONDS:-10}
AGENT_ARGS=${AGENT_ARGS:---stdout --interval 1}
OUTPUT_FILE=$(mktemp /tmp/sentinelmesh-overhead-output.XXXXXX)
AGENT_PID=""

cleanup() {
  if [[ -n "${AGENT_PID}" ]]; then
    kill "${AGENT_PID}" 2>/dev/null || true
    wait "${AGENT_PID}" 2>/dev/null || true
  fi
  rm -f "${OUTPUT_FILE}"
}
trap cleanup EXIT

if ! [[ ${DURATION_SECONDS} =~ ^[1-9][0-9]*$ ]]; then
  echo "DURATION_SECONDS must be a positive integer" >&2
  exit 1
fi
for command in awk getconf python3; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "${command} is required for Agent overhead measurement" >&2
    exit 1
  fi
done

# AGENT_ARGS is intentionally word-split so callers can add --enable-ebpf in a
# privileged environment without maintaining a second measurement script.
# shellcheck disable=SC2086
"${AGENT_BINARY}" ${AGENT_ARGS} >"${OUTPUT_FILE}" 2>/dev/null &
AGENT_PID=$!
sleep 0.5
if ! kill -0 "${AGENT_PID}" 2>/dev/null; then
  echo "Agent exited before overhead sampling started" >&2
  exit 1
fi

TICKS_PER_SECOND=$(getconf CLK_TCK)
START_TICKS=$(python3 - "${AGENT_PID}" <<'PY'
import sys
fields = open(f"/proc/{sys.argv[1]}/stat", encoding="utf-8").read().split()
print(int(fields[13]) + int(fields[14]))
PY
)
sleep "${DURATION_SECONDS}"
END_TICKS=$(python3 - "${AGENT_PID}" <<'PY'
import sys
fields = open(f"/proc/{sys.argv[1]}/stat", encoding="utf-8").read().split()
print(int(fields[13]) + int(fields[14]))
PY
)
MAX_RSS_KIB=$(awk '/^VmHWM:/ {print $2}' "/proc/${AGENT_PID}/status")
kill "${AGENT_PID}" 2>/dev/null || true
wait "${AGENT_PID}" 2>/dev/null || true
AGENT_PID=""

python3 - "${START_TICKS}" "${END_TICKS}" "${TICKS_PER_SECOND}" \
  "${DURATION_SECONDS}" "${MAX_RSS_KIB}" "${OUTPUT_FILE}" <<'PY'
import json
import sys

start, end, ticks, duration, rss = map(float, sys.argv[1:6])
with open(sys.argv[6], encoding="utf-8") as output:
    samples = sum(1 for line in output if line.strip())
cpu_percent = (end - start) / ticks / duration * 100
resolution = 100 / ticks / duration
print(json.dumps({
    "duration_seconds": int(duration),
    "cpu_percent_one_core": round(cpu_percent, 4),
    "cpu_resolution_percent": round(resolution, 4),
    "max_rss_kib": int(rss),
    "samples": samples,
}, ensure_ascii=False))
PY
