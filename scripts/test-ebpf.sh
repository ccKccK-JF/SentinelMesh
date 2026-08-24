#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
AGENT_BINARY=${AGENT_BINARY:-"${ROOT_DIR}/build/agent/sentinel-agent"}

if [[ ${EUID} -ne 0 ]]; then
  echo "eBPF load test requires root or equivalent BPF capabilities" >&2
  exit 1
fi

OUTPUT=$("${AGENT_BINARY}" --enable-ebpf --stdout --once)
grep -q '"scheduler.run_queue.latency.p95.microseconds"' <<<"${OUTPUT}"
grep -q '"scheduler.run_queue.latency.p99.microseconds"' <<<"${OUTPUT}"
grep -q '"scheduler.run_queue.events"' <<<"${OUTPUT}"

echo "runqlat eBPF load and collection test passed"
