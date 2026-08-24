#include <cassert>
#include <cmath>
#include <iostream>

#include "sentinel/procfs.hpp"

int main() {
  const auto cpu = sentinel::ParseCpuStat("cpu  100 2 30 400 5 6 7 8 0 0\n");
  assert(cpu.user == 100);
  assert(cpu.IdleAll() == 405);
  assert(cpu.Total() == 558);

  const auto memory = sentinel::ParseMemInfo(
      "MemTotal:       1000 kB\nMemFree: 100 kB\nMemAvailable: 600 kB\n");
  assert(memory.total_kib == 1000);
  assert(memory.available_kib == 600);

  const auto load = sentinel::ParseLoadAverage("1.25 0.80 0.50 1/100 123\n");
  assert(std::abs(load - 1.25) < 0.0001);

  const auto network = sentinel::ParseNetDev(
      "Inter-| Receive | Transmit\n"
      "  eth0: 1000 10 0 2 0 0 0 0 2000 20 0 3 0 0 0 0\n");
  assert(network.size() == 1);
  assert(network[0].interface_name == "eth0");
  assert(network[0].receive_bytes == 1000);
  assert(network[0].transmit_bytes == 2000);
  assert(network[0].receive_drops == 2);
  assert(network[0].transmit_drops == 3);

  sentinel::Snapshot tcp_snapshot;
  tcp_snapshot.tcp_rtt_p95_microseconds = 32768.0;
  tcp_snapshot.tcp_rtt_p99_microseconds = 65536.0;
  tcp_snapshot.tcp_rtt_samples = 84;
  tcp_snapshot.tcp_retransmissions = 26;
  tcp_snapshot.tcp_receive_resets = 1;
  tcp_snapshot.tcp_send_resets = 2;
  const auto json = sentinel::ToJson(tcp_snapshot);
  assert(json.find("\"tcp.rtt.p95.microseconds\":32768.00") !=
         std::string::npos);
  assert(json.find("\"tcp.rtt.samples\":84") != std::string::npos);
  assert(json.find("\"tcp.retransmissions\":26") != std::string::npos);
  assert(json.find("\"tcp.receive_resets\":1") != std::string::npos);
  assert(json.find("\"tcp.send_resets\":2") != std::string::npos);

  std::cout << "procfs parser tests passed\n";
  return 0;
}
