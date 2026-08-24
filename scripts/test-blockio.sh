#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
AGENT_BINARY=${AGENT_BINARY:-"${ROOT_DIR}/build/agent/sentinel-agent"}
WORKLOAD_FILE=$(mktemp /tmp/sentinelmesh-blockio.XXXXXX)
OUTPUT_FILE=$(mktemp /tmp/sentinelmesh-blockio-output.XXXXXX)
FIO_LOG=$(mktemp /tmp/sentinelmesh-fio.XXXXXX)
FIO_PID=""

cleanup() {
  if [[ -n "${FIO_PID}" ]]; then
    kill "${FIO_PID}" 2>/dev/null || true
    wait "${FIO_PID}" 2>/dev/null || true
  fi
  rm -f "${WORKLOAD_FILE}" "${OUTPUT_FILE}" "${FIO_LOG}"
}
trap cleanup EXIT

if [[ ${EUID} -ne 0 ]]; then
  echo "block I/O eBPF test requires root or equivalent BPF capabilities" >&2
  exit 1
fi
if ! command -v fio >/dev/null 2>&1; then
  echo "fio is required for block I/O fault injection" >&2
  exit 1
fi

fio \
  --name=sentinelmesh-blockio \
  --filename="${WORKLOAD_FILE}" \
  --size=64m \
  --rw=randrw \
  --rwmixread=50 \
  --bs=4k \
  --iodepth=16 \
  --direct=1 \
  --runtime=5 \
  --time_based \
  --ioengine=libaio \
  >"${FIO_LOG}" 2>&1 &
FIO_PID=$!

set +e
timeout 4 "${AGENT_BINARY}" --enable-blockio --stdout --interval 1 \
  >"${OUTPUT_FILE}"
AGENT_STATUS=$?
set -e
wait "${FIO_PID}"
FIO_PID=""

if [[ ${AGENT_STATUS} -ne 0 && ${AGENT_STATUS} -ne 124 ]]; then
  cat "${OUTPUT_FILE}" >&2
  exit "${AGENT_STATUS}"
fi

grep -q '"block.io.read.latency.p95.microseconds"' "${OUTPUT_FILE}"
grep -q '"block.io.read.latency.p99.microseconds"' "${OUTPUT_FILE}"
grep -q '"block.io.write.latency.p95.microseconds"' "${OUTPUT_FILE}"
grep -q '"block.io.write.latency.p99.microseconds"' "${OUTPUT_FILE}"
grep -q '"block.io.read.events"' "${OUTPUT_FILE}"
grep -q '"block.io.write.events"' "${OUTPUT_FILE}"

echo "block I/O eBPF read/write latency test passed"
