// ============================================================================
// agent/src/main.cpp
// ----------------------------------------------------------------------------
// C++ Node Agent 的进程入口。
//
// 职责：
//   1. 解析命令行参数（开关、地址、间隔、BPF 对象路径等）；
//   2. 创建 ProcfsCollector（低频指标）与三个 eBPF 探针（调度/块IO/TCP）；
//   3. 两种运行模式：
//      - --stdout：把采集快照转 JSON 打印到 stdout（调试/无控制面场景）；
//      - 默认：通过 gRPC 双向流连接控制面，周期上报 Snapshot 并等待 ACK；
//   4. 断线重连：指数退避（1s -> 30s），重连后从服务端已接受序列续传。
//
// 面试要点：
//   - RAII：探针用 unique_ptr 管理，析构自动释放 bpf_object/bpf_link；
//   - CPU 利用率必须用两次采样的差值（累计计数器无法直接读百分比），
//     因此 main 启动时先 Collect() 一次作为基线；
//   - Boot ID 来自 /sys/kernel/random/boot_id，标识一次启动生命周期。
// ============================================================================

#include <algorithm>
#include <chrono>
#include <filesystem>
#include <fstream>
#include <iostream>
#include <memory>
#include <stdexcept>
#include <string>
#include <thread>
#include <utility>

#include <unistd.h>

#include "sentinel/blockio_probe.hpp"
#include "sentinel/procfs.hpp"
#include "sentinel/runqlat_probe.hpp"
#include "sentinel/tcp_probe.hpp"
#include "sentinel/telemetry_client.hpp"

namespace {

// 命令行选项集合（对应 main 末尾的解析逻辑）
struct Options {
  std::filesystem::path proc_root{"/proc"};   // procfs 挂载根（测试可指向夹具目录）
  std::string manager_address{"127.0.0.1:50051"}; // 控制面 gRPC 地址
  std::string node_id;                        // 节点 ID（默认取 hostname）
  std::filesystem::path runqlat_object;       // 已编译的 BPF 对象路径
  std::filesystem::path blocklat_object;
  std::filesystem::path tcplat_object;
  std::chrono::seconds interval{5};           // 上报/采集间隔（秒）
  bool once{false};                           // 只采集一次就退出
  bool stdout_only{false};                    // 只输出 JSON，不连控制面
  bool enable_runqlat{false};                 // 开启调度等待探针
  bool enable_blockio{false};                 // 开启块 I/O 探针
  bool enable_tcp{false};                     // 开启 TCP 探针
};

// 读取主机名（失败回退到占位符）
std::string Hostname() {
  char buffer[256]{};
  if (gethostname(buffer, sizeof(buffer) - 1) != 0) {
    return "unknown-host";
  }
  return buffer;
}

// 读取文件第一行（用于读 boot_id）
std::string ReadFirstLine(const std::filesystem::path& path) {
  std::ifstream input(path);
  std::string value;
  if (input) std::getline(input, value);
  return value;
}

// 当前 Unix 时间（纳秒）
std::int64_t UnixNanos() {
  return std::chrono::duration_cast<std::chrono::nanoseconds>(
             std::chrono::system_clock::now().time_since_epoch())
      .count();
}

// 手动解析命令行参数（不引入 getopt 依赖，参数简单）
Options ParseOptions(int argc, char** argv) {
  Options options;
  for (int i = 1; i < argc; ++i) {
    const std::string argument = argv[i];
    if (argument == "--once") {
      options.once = true;
    } else if (argument == "--stdout") {
      options.stdout_only = true;
    } else if (argument == "--enable-ebpf") {
      // 一键开启全部 eBPF 探针
      options.enable_runqlat = true;
      options.enable_blockio = true;
      options.enable_tcp = true;
    } else if (argument == "--enable-runqlat") {
      options.enable_runqlat = true;
    } else if (argument == "--enable-blockio") {
      options.enable_blockio = true;
    } else if (argument == "--enable-tcp") {
      options.enable_tcp = true;
    } else if (argument == "--proc-root" && i + 1 < argc) {
      options.proc_root = argv[++i];
    } else if (argument == "--manager-address" && i + 1 < argc) {
      options.manager_address = argv[++i];
    } else if (argument == "--node-id" && i + 1 < argc) {
      options.node_id = argv[++i];
    } else if (argument == "--runqlat-object" && i + 1 < argc) {
      options.runqlat_object = argv[++i];
    } else if (argument == "--blocklat-object" && i + 1 < argc) {
      options.blocklat_object = argv[++i];
    } else if (argument == "--tcplat-object" && i + 1 < argc) {
      options.tcplat_object = argv[++i];
    } else if (argument == "--interval" && i + 1 < argc) {
      const auto seconds = std::stoul(argv[++i]);
      if (seconds == 0) throw std::invalid_argument("interval must be positive");
      options.interval = std::chrono::seconds(seconds);
    } else {
      throw std::invalid_argument("unknown or incomplete argument: " + argument);
    }
  }
  // 默认值：node_id 取 hostname；BPF 对象默认与可执行文件同目录
  if (options.node_id.empty()) options.node_id = Hostname();
  if (options.runqlat_object.empty()) {
    options.runqlat_object =
        std::filesystem::absolute(argv[0]).parent_path() / "runqlat.bpf.o";
  }
  if (options.blocklat_object.empty()) {
    options.blocklat_object =
        std::filesystem::absolute(argv[0]).parent_path() / "blocklat.bpf.o";
  }
  if (options.tcplat_object.empty()) {
    options.tcplat_object =
        std::filesystem::absolute(argv[0]).parent_path() / "tcplat.bpf.o";
  }
  return options;
}

// CollectSnapshot 汇总 procfs + 各 eBPF 探针的结果到一份 Snapshot。
// 探针可能未启用（nullptr），所以每个探针都判空后再合并。
sentinel::Snapshot CollectSnapshot(
    sentinel::ProcfsCollector* collector,
    sentinel::RunQueueLatencyProbe* runqlat_probe,
    sentinel::BlockIoLatencyProbe* blockio_probe,
    sentinel::TcpMetricsProbe* tcp_probe) {
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
  if (tcp_probe != nullptr) {
    auto tcp = tcp_probe->Collect();
    if (tcp) {
      if (tcp->rtt) {
        snapshot.tcp_rtt_p95_microseconds = tcp->rtt->p95_microseconds;
        snapshot.tcp_rtt_p99_microseconds = tcp->rtt->p99_microseconds;
        snapshot.tcp_rtt_samples = tcp->rtt->events;
      }
      snapshot.tcp_retransmissions = tcp->retransmissions;
      snapshot.tcp_receive_resets = tcp->receive_resets;
      snapshot.tcp_send_resets = tcp->send_resets;
      snapshot.kernel_ring_buffer_dropped = tcp->ring_buffer_dropped;
      snapshot.kernel_events = std::move(tcp->events);
    }
  }
  if (blockio_probe != nullptr) {
    const auto block_latency = blockio_probe->Collect();
    if (block_latency) {
      if (block_latency->read) {
        snapshot.block_io_read_p95_microseconds =
            block_latency->read->p95_microseconds;
        snapshot.block_io_read_p99_microseconds =
            block_latency->read->p99_microseconds;
        snapshot.block_io_read_events = block_latency->read->events;
      }
      if (block_latency->write) {
        snapshot.block_io_write_p95_microseconds =
            block_latency->write->p95_microseconds;
        snapshot.block_io_write_p99_microseconds =
            block_latency->write->p99_microseconds;
        snapshot.block_io_write_events = block_latency->write->events;
      }
    }
  }
  return snapshot;
}

// RunStdout 模式：周期性打印 JSON，--once 时打印一次即退出。
// 无控制面、无 gRPC，适合本地验证采集是否正确。
int RunStdout(const Options& options, sentinel::ProcfsCollector* collector,
              sentinel::RunQueueLatencyProbe* runqlat_probe,
              sentinel::BlockIoLatencyProbe* blockio_probe,
              sentinel::TcpMetricsProbe* tcp_probe) {
  do {
    std::cout << sentinel::ToJson(
                     CollectSnapshot(collector, runqlat_probe, blockio_probe,
                                     tcp_probe))
              << std::endl;
    if (options.once) return 0;
    std::this_thread::sleep_for(options.interval);
  } while (true);
}

// RunGrpc 模式：连接控制面并持续上报。
// 断线重连逻辑：
//   - Connect 失败时指数退避（1s -> 2s -> ... -> 30s）；
//   - 重连成功后，next_sequence = max(next, server已接受序列 + 1)，
//     从而跳过已被服务端处理的批次（幂等续传）；
//   - 每次 SendSnapshot 成功收到 ACK 后更新本地序列号。
int RunGrpc(const Options& options, sentinel::ProcfsCollector* collector,
            sentinel::RunQueueLatencyProbe* runqlat_probe,
            sentinel::BlockIoLatencyProbe* blockio_probe,
            sentinel::TcpMetricsProbe* tcp_probe) {
  // Agent 身份：node_id + boot_id 是控制面幂等语义的 key
  const sentinel::AgentIdentity identity{
      .node_id = options.node_id,
      .hostname = Hostname(),
      .ip_address = "",
      .agent_version = "sentinel-agent/0.5.0",
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

    // 续传：从服务端已接受序列的下一条开始
    next_sequence = std::max(next_sequence, client.accepted_sequence() + 1);
    retry_delay = std::chrono::seconds(1); // 连接成功，重置退避
    std::cerr << "connected to " << options.manager_address
              << ", next sequence=" << next_sequence
              << ", config version=" << client.config_version() << std::endl;

    while (true) {
      const auto snapshot =
          CollectSnapshot(collector, runqlat_probe, blockio_probe, tcp_probe);
      if (!client.SendSnapshot(next_sequence, UnixNanos(), snapshot)) {
        std::cerr << "send failed: " << client.last_error() << std::endl;
        break; // 发送失败：跳出内层循环，走重连
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
    // 三个 eBPF 探针均为可选，unique_ptr 为空表示未启用
    std::unique_ptr<sentinel::RunQueueLatencyProbe> runqlat_probe;
    std::unique_ptr<sentinel::BlockIoLatencyProbe> blockio_probe;
    std::unique_ptr<sentinel::TcpMetricsProbe> tcp_probe;
    if (options.enable_runqlat) {
      runqlat_probe = std::make_unique<sentinel::RunQueueLatencyProbe>();
      if (!runqlat_probe->Open(options.runqlat_object)) {
        throw std::runtime_error("enable runqlat eBPF probe: " +
                                 runqlat_probe->last_error());
      }
      std::cerr << "runqlat eBPF probe attached from "
                << options.runqlat_object << std::endl;
    }

    if (options.enable_blockio) {
      blockio_probe = std::make_unique<sentinel::BlockIoLatencyProbe>();
      if (!blockio_probe->Open(options.blocklat_object)) {
        throw std::runtime_error("enable block I/O eBPF probe: " +
                                 blockio_probe->last_error());
      }
      std::cerr << "block I/O eBPF probe attached from "
                << options.blocklat_object << std::endl;
    }

    if (options.enable_tcp) {
      tcp_probe = std::make_unique<sentinel::TcpMetricsProbe>();
      if (!tcp_probe->Open(options.tcplat_object)) {
        throw std::runtime_error("enable TCP eBPF probe: " +
                                 tcp_probe->last_error());
      }
      std::cerr << "TCP eBPF probe attached from " << options.tcplat_object
                << std::endl;
    }

    // CPU 和网络速率都是累计计数器，必须用相邻两次采样的差值计算。
    // 这里先采一次作为基线，再等 250ms 让内核 tracepoint 状态稳定。
    collector.Collect();
    std::this_thread::sleep_for(std::chrono::milliseconds(250));

    if (options.stdout_only) {
      return RunStdout(options, &collector, runqlat_probe.get(),
                       blockio_probe.get(), tcp_probe.get());
    }
    return RunGrpc(options, &collector, runqlat_probe.get(),
                   blockio_probe.get(), tcp_probe.get());
  } catch (const std::exception& error) {
    std::cerr << "sentinel-agent: " << error.what() << std::endl;
    return 1;
  }
}
