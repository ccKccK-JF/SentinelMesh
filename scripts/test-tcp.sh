#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
AGENT_BINARY=${AGENT_BINARY:-"${ROOT_DIR}/build/agent/sentinel-agent"}
OUTPUT_FILE=$(mktemp /tmp/sentinelmesh-tcp-output.XXXXXX)
WORKLOAD_LOG=$(mktemp /tmp/sentinelmesh-tcp-workload.XXXXXX)
WORKLOAD_PID=""
RESET_PID=""
SERVER_PID=""

cleanup() {
  if [[ -n "${WORKLOAD_PID}" ]]; then
    kill "${WORKLOAD_PID}" 2>/dev/null || true
    wait "${WORKLOAD_PID}" 2>/dev/null || true
  fi
  if [[ -n "${RESET_PID}" ]]; then
    kill "${RESET_PID}" 2>/dev/null || true
    wait "${RESET_PID}" 2>/dev/null || true
  fi
  if [[ -n "${SERVER_PID}" ]]; then
    kill "${SERVER_PID}" 2>/dev/null || true
    wait "${SERVER_PID}" 2>/dev/null || true
  fi
  tc qdisc del dev lo root 2>/dev/null || true
  rm -f "${OUTPUT_FILE}" "${WORKLOAD_LOG}"
}
trap cleanup EXIT

if [[ ${EUID} -ne 0 ]]; then
  echo "TCP eBPF test requires root or equivalent BPF capabilities" >&2
  exit 1
fi
for command in iperf3 python3 tc timeout; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "${command} is required for TCP fault injection" >&2
    exit 1
  fi
done

# Loopback keeps this test self-contained. Delay creates observable RTT samples;
# loss forces TCP retransmissions. The Python clients also close some sockets
# with SO_LINGER=0 so both send and receive reset tracepoints are exercised.
tc qdisc replace dev lo root netem delay 15ms loss 30%
iperf3 -s >>"${WORKLOAD_LOG}" 2>&1 &
SERVER_PID=$!
sleep 0.2

# Sustained parallel streams make data-segment retransmission deterministic.
iperf3 -c 127.0.0.1 -t 7 -P 4 >>"${WORKLOAD_LOG}" 2>&1 &
WORKLOAD_PID=$!

python3 - <<'PY' >>"${WORKLOAD_LOG}" 2>&1 &
import socket
import struct
import threading
import time

address = ("127.0.0.1", 19391)
stop = threading.Event()

def server():
    listener = socket.socket()
    listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    listener.bind(address)
    listener.listen(128)
    listener.settimeout(0.1)
    while not stop.is_set():
        try:
            connection, _ = listener.accept()
        except socket.timeout:
            continue
        try:
            while connection.recv(16384):
                connection.sendall(b"a" * 16384)
        except OSError:
            pass
        finally:
            connection.close()
    listener.close()

thread = threading.Thread(target=server, daemon=True)
thread.start()
time.sleep(0.2)
deadline = time.monotonic() + 8
while time.monotonic() < deadline:
    try:
        client = socket.create_connection(address, timeout=1)
        client.sendall(b"q" * 16384)
        client.recv(16384)
        client.setsockopt(socket.SOL_SOCKET, socket.SO_LINGER,
                          struct.pack("ii", 1, 0))
        client.close()
    except OSError:
        pass
stop.set()
thread.join(timeout=1)
PY
RESET_PID=$!

set +e
timeout 7 "${AGENT_BINARY}" --enable-tcp --stdout --interval 1 \
  >"${OUTPUT_FILE}"
AGENT_STATUS=$?
set -e
wait "${WORKLOAD_PID}" || true
WORKLOAD_PID=""
wait "${RESET_PID}" || true
RESET_PID=""
kill "${SERVER_PID}" 2>/dev/null || true
wait "${SERVER_PID}" 2>/dev/null || true
SERVER_PID=""

if [[ ${AGENT_STATUS} -ne 0 && ${AGENT_STATUS} -ne 124 ]]; then
  cat "${OUTPUT_FILE}" >&2
  cat "${WORKLOAD_LOG}" >&2
  exit "${AGENT_STATUS}"
fi

python3 - "${OUTPUT_FILE}" <<'PY'
import json
import sys

maximum = {}
event_types = set()
with open(sys.argv[1], encoding="utf-8") as output:
    for line in output:
        snapshot = json.loads(line)
        metrics = snapshot["metrics"]
        for name, value in metrics.items():
            maximum[name] = max(maximum.get(name, 0), value)
        event_types.update(event["type"] for event in snapshot["kernel_events"])

required_positive = (
    "tcp.rtt.p95.microseconds",
    "tcp.rtt.p99.microseconds",
    "tcp.rtt.samples",
    "tcp.retransmissions",
    "tcp.receive_resets",
    "tcp.send_resets",
)
missing = [name for name in required_positive if maximum.get(name, 0) <= 0]
if missing:
    raise SystemExit("TCP metrics did not become positive: " + ", ".join(missing))
if "kernel.ring_buffer.dropped" not in maximum:
    raise SystemExit("kernel.ring_buffer.dropped metric was not emitted")
required_events = {"tcp_retransmit", "tcp_receive_reset", "tcp_send_reset"}
if not required_events.issubset(event_types):
    raise SystemExit("missing TCP kernel events: " +
                     ", ".join(sorted(required_events - event_types)))
print("TCP eBPF RTT/retransmission/reset test passed", maximum)
PY
