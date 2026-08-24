#include <algorithm>
#include <chrono>
#include <filesystem>
#include <fstream>
#include <iostream>
#include <stdexcept>
#include <string>
#include <thread>

#include <unistd.h>

#include "sentinel/procfs.hpp"
#include "sentinel/telemetry_client.hpp"

namespace {

struct Options {
  std::filesystem::path proc_root{"/proc"};
  std::string manager_address{"127.0.0.1:50051"};
  std::string node_id;
  std::chrono::seconds interval{5};
  bool once{false};
  bool stdout_only{false};
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
    } else if (argument == "--proc-root" && i + 1 < argc) {
      options.proc_root = argv[++i];
    } else if (argument == "--manager-address" && i + 1 < argc) {
      options.manager_address = argv[++i];
    } else if (argument == "--node-id" && i + 1 < argc) {
      options.node_id = argv[++i];
    } else if (argument == "--interval" && i + 1 < argc) {
      const auto seconds = std::stoul(argv[++i]);
      if (seconds == 0) throw std::invalid_argument("interval must be positive");
      options.interval = std::chrono::seconds(seconds);
    } else {
      throw std::invalid_argument("unknown or incomplete argument: " + argument);
    }
  }
  if (options.node_id.empty()) options.node_id = Hostname();
  return options;
}

int RunStdout(const Options& options, sentinel::ProcfsCollector* collector) {
  do {
    std::cout << sentinel::ToJson(collector->Collect()) << std::endl;
    if (options.once) return 0;
    std::this_thread::sleep_for(options.interval);
  } while (true);
}

int RunGrpc(const Options& options, sentinel::ProcfsCollector* collector) {
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
      const auto snapshot = collector->Collect();
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

    // CPU and rate metrics require a delta between two samples.
    collector.Collect();
    std::this_thread::sleep_for(std::chrono::milliseconds(250));

    if (options.stdout_only) {
      return RunStdout(options, &collector);
    }
    return RunGrpc(options, &collector);
  } catch (const std::exception& error) {
    std::cerr << "sentinel-agent: " << error.what() << std::endl;
    return 1;
  }
}
