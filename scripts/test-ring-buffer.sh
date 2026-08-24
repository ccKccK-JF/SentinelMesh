#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
AGENT_BINARY=${AGENT_BINARY:-"${ROOT_DIR}/build/agent/sentinel-agent"}
OUTPUT_FILE=$(mktemp /tmp/sentinelmesh-ring-output.XXXXXX)
WORKLOAD_LOG=$(mktemp /tmp/sentinelmesh-ring-workload.XXXXXX)
WORKLOAD_PID=""

cleanup() {
  if [[ -n "${WORKLOAD_PID}" ]]; then
    kill "${WORKLOAD_PID}" 2>/dev/null || true
    wait "${WORKLOAD_PID}" 2>/dev/null || true
  fi
  rm -f "${OUTPUT_FILE}" "${WORKLOAD_LOG}"
}
trap cleanup EXIT

if [[ ${EUID} -ne 0 ]]; then
  echo "Ring Buffer eBPF test requires root or equivalent BPF capabilities" >&2
  exit 1
fi
for command in python3 timeout; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "${command} is required for Ring Buffer pressure testing" >&2
    exit 1
  fi
done

# Start after the Agent's initial collection, then overflow a five-second
# collection window with reset events. This validates both BPF reservation
# failures and the userspace 1024-event batch bound.
python3 - <<'PY' >"${WORKLOAD_LOG}" 2>&1 &
import socket
import struct
import threading
import time

time.sleep(1)
address = ("127.0.0.1", 19392)
stop = threading.Event()

def server():
    listener = socket.socket()
    listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    listener.bind(address)
    listener.listen(1024)
    listener.settimeout(0.05)
    while not stop.is_set():
        try:
            connection, _ = listener.accept()
        except socket.timeout:
            continue
        try:
            connection.recv(1)
        except OSError:
            pass
        connection.close()
    listener.close()

def reset_client(deadline):
    linger = struct.pack("ii", 1, 0)
    while time.monotonic() < deadline:
        try:
            client = socket.create_connection(address, timeout=0.2)
            client.setsockopt(socket.SOL_SOCKET, socket.SO_LINGER, linger)
            client.close()
        except OSError:
            pass

server_thread = threading.Thread(target=server, daemon=True)
server_thread.start()
time.sleep(0.1)
deadline = time.monotonic() + 5
workers = [threading.Thread(target=reset_client, args=(deadline,))
           for _ in range(64)]
for worker in workers:
    worker.start()
for worker in workers:
    worker.join()
stop.set()
server_thread.join(timeout=1)
PY
WORKLOAD_PID=$!

set +e
timeout 7 "${AGENT_BINARY}" --enable-tcp --stdout --interval 5 \
  >"${OUTPUT_FILE}"
AGENT_STATUS=$?
set -e
wait "${WORKLOAD_PID}" || true
WORKLOAD_PID=""

if [[ ${AGENT_STATUS} -ne 0 && ${AGENT_STATUS} -ne 124 ]]; then
  cat "${OUTPUT_FILE}" >&2
  cat "${WORKLOAD_LOG}" >&2
  exit "${AGENT_STATUS}"
fi

python3 - "${OUTPUT_FILE}" <<'PY'
import json
import sys

maximum_dropped = 0
maximum_events = 0
with open(sys.argv[1], encoding="utf-8") as output:
    for line in output:
        snapshot = json.loads(line)
        maximum_dropped = max(
            maximum_dropped,
            snapshot["metrics"].get("kernel.ring_buffer.dropped", 0),
        )
        maximum_events = max(maximum_events, len(snapshot["kernel_events"]))

if maximum_dropped <= 0:
    raise SystemExit("Ring Buffer pressure did not report dropped events")
if maximum_events != 1024:
    raise SystemExit(f"expected bounded 1024-event batch, got {maximum_events}")
print("Ring Buffer overflow accounting test passed",
      {"events": maximum_events, "dropped": maximum_dropped})
PY
