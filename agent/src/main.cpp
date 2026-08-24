#include <algorithm>
#include <chrono>
#include <filesystem>
#include <fstream>
#include <iostream>
#include <memory>
#include <stdexcept>
#include <string>
#include <thread>

#include <unistd.h>

#include "sentinel/procfs.hpp"
#include "sentinel/runqlat_probe.hpp"
#include "sentinel/telemetry_client.hpp"

namespace {

struct Options {
  std::filesystem::path proc_root{"/proc"};
  std::string manager_address{"127.0.0.1:50051"};
  std::string node_id;
  std::filesystem::path runqlat_object;
  std::chrono::seconds interval{5};
  bool once{false};
  bool stdout_only{false};
  bool enable_ebpf{false};
};

std::string Hostname() {
  char buffer[256]{};
  if (gethostname(buffer, sizeof(buffer) - 1) != 0) {
    return "unknown-host";
  }
  return buffer;
}

std::string ReadFirstLine(const std::filesystem::path& path) {
  std::ifstream input(path);
  std::string value;
  if (input) std::getline(input, value);
  return value;
}

std::int64_t UnixNanos() {
  return std::chrono::duration_cast<std::chrono::nanoseconds>(
             std::chrono::system_clock::now().time_since_epoch())
      .count();
}

Options ParseOptions(int argc, char** argv) {
  Options options;
  for (int i = 1; i < argc; ++i) {
    const std::string argument = argv[i];
    if (argument == "--once") {
      options.once = true;
    } else if (argument == "--stdout") {
      options.stdout_only = true;
    } else if (argument == "--enable-ebpf") {
      options.enable_ebpf = true;
    } else if (argument == "--proc-root" && i + 1 < argc) {
      options.proc_root = argv[++i];
    } else if (argument == "--manager-address" && i + 1 < argc) {
      options.manager_address = argv[++i];
    } else if (argument == "--node-id" && i + 1 < argc) {
      options.node_id = argv[++i];
    } else if (argument == "--runqlat-object" && i + 1 < argc) {
      options.runqlat_object = argv[++i];
    } else if (argument == "--interval" && i + 1 < argc) {
      const auto seconds = std::stoul(argv[++i]);
      if (seconds == 0) throw std::invalid_argument("interval must be positive");
      options.interval = std::chrono::seconds(seconds);
    } else {
      throw std::invalid_argument("unknown or incomplete argument: " + argument);
    }
  }
  if (options.node_id.empty()) options.node_id = Hostname();
  if (options.runqlat_object.empty()) {
    options.runqlat_object =
        std::filesystem::absolute(argv[0]).parent_path() / "runqlat.bpf.o";
  }
  return options;
}

sentinel::Snapshot CollectSnapshot(
    sentinel::ProcfsCollector* collector,
    sentinel::RunQueueLatencyProbe* runqlat_probe) {
  auto snapshot = collector->Collect();
  if (runqlat_probe != nullptr) {
    const auto latency = runqlat_probe->Collect();
    if (latency) {
      snapshot.scheduler_run_queue_p95_microseconds =
          latency->p95_microseconds;
      snapshot.scheduler_run_queue_p99_microseconds =
          latency->p99_microseconds;
      snapshot.scheduler_run_queue_events = latency->events;
    }
  }
  return snapshot;
}

int RunStdout(const Options& options, sentinel::ProcfsCollector* collector,
              sentinel::RunQueueLatencyProbe* runqlat_probe) {
  do {
    std::cout << sentinel::ToJson(CollectSnapshot(collector, runqlat_probe))
              << std::endl;
    if (options.once) return 0;
    std::this_thread::sleep_for(options.interval);
  } while (true);
}

int RunGrpc(const Options& options, sentinel::ProcfsCollector* collector,
            sentinel::RunQueueLatencyProbe* runqlat_probe) {
  const sentinel::AgentIdentity identity{
      .node_id = options.node_id,
      .hostname = Hostname(),
      .ip_address = "",
      .agent_version = "sentinel-agent/0.2.0",
      .boot_id = ReadFirstLine(options.proc_root / "sys/kernel/random/boot_id"),
  };
  sentinel::TelemetryClient client(options.manager_address);
  std::uint64_t next_sequence = 1;
  auto retry_delay = std::chrono::seconds(1);

  while (true) {
    if (!client.Connect(identity)) {
      std::cerr << "connect failed: " << client.last_error() << std::endl;
      if (options.once) return 2;
      std::this_thread::sleep_for(retry_delay);
      retry_delay = std::min(retry_delay * 2, std::chrono::seconds(30));
      continue;
    }

    next_sequence = std::max(next_sequence, client.accepted_sequence() + 1);
    retry_delay = std::chrono::seconds(1);
    std::cerr << "connected to " << options.manager_address
              << ", next sequence=" << next_sequence
              << ", config version=" << client.config_version() << std::endl;

    while (true) {
      const auto snapshot = CollectSnapshot(collector, runqlat_probe);
      if (!client.SendSnapshot(next_sequence, UnixNanos(), snapshot)) {
        std::cerr << "send failed: " << client.last_error() << std::endl;
        break;
      }
      std::cerr << "accepted metric sequence " << next_sequence << std::endl;
      next_sequence = client.accepted_sequence() + 1;
      if (options.once) {
        client.Close();
        return 0;
      }
      std::this_thread::sleep_for(options.interval);
    }
  }
}

}  // namespace

int main(int argc, char** argv) {
  try {
    const auto options = ParseOptions(argc, argv);
    sentinel::ProcfsCollector collector(options.proc_root);
    std::unique_ptr<sentinel::RunQueueLatencyProbe> runqlat_probe;
    if (options.enable_ebpf) {
      runqlat_probe = std::make_unique<sentinel::RunQueueLatencyProbe>();
      if (!runqlat_probe->Open(options.runqlat_object)) {
        throw std::runtime_error("enable runqlat eBPF probe: " +
                                 runqlat_probe->last_error());
      }
      std::cerr << "runqlat eBPF probe attached from "
                << options.runqlat_object << std::endl;
    }

    // CPU and rate metrics require a delta between two samples.
    collector.Collect();
    std::this_thread::sleep_for(std::chrono::milliseconds(250));

    if (options.stdout_only) {
      return RunStdout(options, &collector, runqlat_probe.get());
    }
    return RunGrpc(options, &collector, runqlat_probe.get());
  } catch (const std::exception& error) {
    std::cerr << "sentinel-agent: " << error.what() << std::endl;
    return 1;
  }
}
