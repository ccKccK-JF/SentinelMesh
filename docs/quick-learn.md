# SentinelMesh 快速学习与面试指南

> 本文是配合带注释代码使用的「速学路线图」。先理解问题，再理解架构，最后逐个击破算法与难点。完整面试手册见 [interview-guide.md](interview-guide.md)。

## 0. 三十秒理解项目

**问题**：游戏服务器 / 通用后端节点变慢时，监控只能看到"CPU 很高"，却分不清是调度竞争、磁盘尾延迟还是网络问题；更没法自动把流量从坏节点切走。

**方案**：一条「采集 → 诊断 → 决策 → 调度 → 验证」的闭环：

```text
C++ Agent（每台节点）
  ├─ procfs 读 CPU/内存/Load/网络（低频、累计）
  └─ eBPF 测调度等待 / 块IO延迟 / TCP RTT·重传·RST（内核热路径，低开销）
        │
        │ gRPC 双向流 + Protobuf 批次 + ACK
        ▼
Go Control Plane
  ├─ 内存 Store（最新快照 + 幂等去重）
  ├─ 健康状态机（EWMA + 硬门槛 + 滞回 + 冷却）
  └─ 路由策略（异常摘除 / 恢复渐进增权 / 权重归一到 10000）
        │
        ├─ HTTP /v1/nodes、/v1/routing（网关 ETag/304）
        ├─ HTTP /metrics → Prometheus → Grafana
        └─ 平滑加权轮询（SWRR）分配给网关
```

**核心思想**：Agent 只负责「单机事实采集与窗口聚合」，控制面持有全局视图统一决策。避免各节点基于局部数据产生互相矛盾的路由结果。

## 1. 按此顺序读代码（带注释版）

| 步骤 | 文件 | 收获 |
|---|---|---|
| 1 | `api/proto/sentinel/v1/telemetry.proto` | 跨语言契约：双向流、oneof、sequence/ACK 幂等基础 |
| 2 | `agent/ebpf/runqlat.bpf.c` | eBPF 最小范例：HASH 记录入队、PERCPU_ARRAY 分桶、CO-RE |
| 3 | `agent/ebpf/blocklat.bpf.c` | 用 `struct request*` 关联 issue/complete 生命周期 |
| 4 | `agent/ebpf/tcplat.bpf.c` | RINGBUF 事件通道 + dropped 计数（有界可靠） |
| 5 | `agent/src/latency_histogram.cpp` | 对数直方图 → P95/P99 的窗口增量算法 |
| 6 | `agent/src/tcp_probe.cpp` | libbpf 加载、RAII、per-CPU 聚合、Ring Buffer 消费 |
| 7 | `agent/src/procfs.cpp` | 差值法算 CPU/网络速率、MemAvailable 语义 |
| 8 | `agent/src/telemetry_client.cpp` | 批次发送 + ACK 判定（accepted_sequence >= sequence） |
| 9 | `internal/ingest/server.go` | 服务端校验、容量限制、重复批次转 ACK |
| 10 | `internal/store/memory.go` | 全局锁 + 深拷贝、Boot ID 生命周期、sequence 幂等 |
| 11 | `internal/scoring/scorer.go` | 健康状态机（核心难点） |
| 12 | `internal/routing/policy.go` | 权重计算 + 最大余数归一化 |
| 13 | `internal/routing/selector.go` | 平滑加权轮询（SWRR） |
| 14 | `internal/experiment/routing.go` | 可复现的对照实验设计 |

## 2. 一次指标上报的完整旅程（必背）

1. Agent 启动读 `/sys/kernel/random/boot_id`，建立 gRPC 双向流，**第一条消息必须是 Hello**。
2. 控制面校验 `node_id` 格式，以 `node_id + boot_id` 识别节点生命周期；Boot ID 变化 → 重置评分状态机；返回「已接受的最大序列」。
3. C++ 采集 procfs；启用 eBPF 时读 per-CPU Map、消费 Ring Buffer，计算**相邻窗口增量**得到 P95/P99。
4. 组装 `MetricBatch`（sequence、采样时间、指标、最多 1024 条内核事件）发送。
5. 控制面限制单批 2048 指标 / 1024 事件，过滤空名、NaN、Inf。
6. Store 在**写锁**内：校验 sequence > last → 合并指标 → 评分状态机 → 刷新路由快照。
7. 返回 ACK；Agent 只有看到 `accepted_sequence >= 自己序列` 才认为成功。
8. HTTP API / Prometheus / 网关读取**深拷贝**快照，不共享内部 map。

## 3. 三大核心难点（面试主战场）

### 3.1 eBPF 如何做到低开销观测

- **只观测因果路径**：procfs 处理稳定低频指标；eBPF 只处理「延迟和异常事件」这类无法从计数看出的东西。
- **per-CPU 对数直方图**：内核热路径只做「查表 + 自增」，不做原子操作（per-CPU 避免 cache line 竞争）；用户态跨 CPU 求和 + 相邻窗口差值估算 P95/P99。
- **2^n 对数桶**：32 个桶覆盖 1μs ~ 数十秒，固定内存。
- **有界 Ring Buffer**：事件通道 256 KiB，reserve 失败记 per-CPU dropped；用户态超 1024 条截断计数。丢失可观测，而不是假装不丢。
- **CO-RE + BTF**：Clang 保留字段重定位信息，libbpf 加载时按目标内核 BTF 修正偏移 → Compile Once, Run Everywhere。

### 3.2 遥测链路如何保证不丢不重（幂等）

关键歧义：**服务端已处理但 ACK 丢失**（连接断开）。对策：

- 单调 sequence：Store 只接受 `sequence > last_sequence`；
- 同 Boot ID 重连：Hello ACK 带回 accepted_sequence，Agent 从 `last+1` 续传；
- 重复批次：服务端不重算，但 ACK 仍返回 accepted_sequence >= sequence，Agent 视为成功。

语义澄清（面试必被追问）：这是「至少一次传输下的**幂等应用**（单进程生命周期内）」，**不是** exactly-once——控制面重启后去重历史丢失。

### 3.3 状态机如何避免流量抖动

四层防护（快速失败、谨慎恢复）：

1. **EWMA**（α=0.35）：平滑瞬时尖峰；
2. **硬门槛**：CPU≥99.5% / 内存≥98% / 磁盘≥99.5% → 读**原始值**立即 unhealthy（不被平滑稀释）；
3. **滞回**：恶化需连续 **2** 个样本、改善需连续 **3** 个才迁移状态；
4. **冷却 + 渐进增权**：unhealthy 后先等 **30 秒**冷却；恢复后权重从 **10%** 起步，**60 秒**线性爬到 100%。

## 4. 路由算法速览

### 权重计算（policy.go）

```text
healthy   → 权重 = 健康分
degraded  → 权重 = 健康分 × 0.5（惩罚）
断连/unhealthy/unknown → 权重 = 0
恢复中    → 权重 × ramp factor（0.1→1.0 线性）
```

### 最大余数归一化

浮点权重 `exact = raw/total × 10000` → 向下取整得到整数 → 按小数余数从大到小逐个 +1 → **总和严格等于 10000**。比逐项四舍五入更不容易漂移。

### 平滑加权轮询（SWRR）

每轮所有节点 `current += weight`，选 current 最大者，被选者 `current -= total`。避免普通 WRR 的「潮汐效应」，且是确定性算法（节点按 ID 排序），便于测试。

## 5. 边界与诚实回答（面试加分）

| 已实现并验证 | 只是演进方向（勿说成已完成） |
|---|---|
| 内存 Store 单进程一致性 | mTLS + 节点身份认证 |
| 控制面重启丢内存状态 | 元数据持久化 / 多副本一致性 / leader |
| 确定性对照实验（错误率 5.21%→0.20%） | 真实线上流量 SLO 对比 |
| Ring Buffer 有界、丢失可观测 | 事件采样 / 按类型限流 |
| Agent 需 root/特权 | CAP_BPF + CAP_PERFMON 收敛、seccomp |

## 6. 一分钟自我介绍模板

> 我做了个 Linux 服务器性能诊断与自适应路由平台。节点侧用 C++20 + libbpf CO-RE 采集 procfs 指标，并用 eBPF 跟踪调度等待、块 I/O、TCP 的延迟与异常事件，通过 per-CPU 对数直方图和 256 KiB 有界 Ring Buffer 把采集开销压到空载窗口单核 CPU 约 0.1%。控制面用 Go 接收 gRPC 双向流，基于单调序列 + Boot ID 实现断线重连的幂等续传；用 EWMA、硬门槛、滞回和 30 秒冷却构建健康状态机，异常节点自动摘除、恢复节点 10% 起步 60 秒渐进增权，总权重严格归一到 10000 并配合平滑加权轮询。在确定性 12,000 请求模型中，把错误率从 5.21% 降到 0.20%、P99 从 219ms 降到 16ms，并用 stress-ng/fio/netem 做故障注入验证。

## 7. 高频追问速答（详见 interview-guide.md）

- **eBPF 为什么安全？** verifier 校验有界控制流/指针/栈/helper；但挂载点、Map 大小、事件率仍需人工收敛。
- **tracepoint / kprobe / fentry？** tracepoint 是稳定 ABI，本项目优先；fentry 开销最低但要求高。
- **Ring Buffer vs Perf Buffer？** Ring 是共享 MPSC、可保跨 CPU 顺序；Perf 通常按 CPU 分配。二者都可能丢数据。
- **为什么需要 Boot ID？** sequence 只在一个启动周期内有效，重启后从 1 开始，没有 Boot ID 会把新周期数据误判为重复。
- **为什么只存最新快照？** 控制面负责实时决策；时序存储交给 Prometheus（压缩/保留策略/PromQL 现成），职责分离。
- **RWMutex 会成瓶颈吗？** 当前规模下合理；升级方向是分片锁或不可变快照 + atomic pointer，用 benchmark 决定。
- **健康分能直接当结论吗？** 不能，它是策略输入；相同分数可能由不同资源组合导致，须结合 reason/原始指标。
