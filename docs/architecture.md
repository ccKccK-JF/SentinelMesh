# 架构设计

## 1. 目标与非目标

SentinelMesh 解决三个连续问题：哪个节点正在变慢、瓶颈位于哪条系统路径、诊断期间如何减少异常节点的新流量。为此系统把单机采集、全局判断和路由消费拆成独立组件。

当前目标：

- 采集 Linux 资源容量与内核延迟信号；
- 建立 C++ Agent 到 Go 控制面的可靠遥测链路；
- 输出可解释的节点状态和版本化路由权重；
- 用故障注入和对照实验验证功能。

当前不承担长期时序存储、业务请求代理、自动扩缩容、商业级多租户和跨区域一致性。这些边界避免把控制面做成难以验证的“大而全”平台。

## 2. 逻辑架构

```text
┌────────────────────────── Linux node ──────────────────────────┐
│                                                                │
│  /proc/stat ─┐                                                  │
│  /proc/meminfo├──► ProcfsCollector ─┐                           │
│  /proc/loadavg│                      │                           │
│  /proc/net/dev┘                      │                           │
│                                      ├──► Snapshot ─► Protobuf  │
│  sched tracepoints ─► runqlat Map ───┤                           │
│  block tracepoints ─► blocklat Map ──┤       C++ Node Agent     │
│  TCP tracepoints ─► Map/Ring Buffer ─┘                           │
└──────────────────────────────┬─────────────────────────────────┘
                               │ gRPC bidirectional stream
┌──────────────────────────────▼─────────────────────────────────┐
│                        Go Control Plane                        │
│                                                               │
│ Ingest validation → Memory Store → Health State Machine        │
│                               └──► Routing Policy + Version     │
│                                      │                         │
│ HTTP nodes/routing ◄─────────────────┤                         │
│ Prometheus exporter ◄────────────────┘                         │
└───────────────┬──────────────────────────────┬────────────────┘
                │                              │
         operator/Grafana              gateway/allocator
```

## 3. 组件职责

### 3.1 C++ Node Agent

Agent 只处理本节点事实：

- 读取 procfs 的累计计数并计算相邻采样差；
- 加载 eBPF ELF、挂载 tracepoint、读取 Map 和消费 Ring Buffer；
- 聚合固定窗口指标，限制每批事件数量；
- 建立 gRPC 长连接，发送 Hello、MetricBatch 和 Heartbeat；
- 根据 ACK 延续 sequence，连接失败时指数退避。

Agent 不计算集群排名或最终路由权重，因为单节点看不到其他节点状态。将策略留在控制面也便于统一升级与回滚。

### 3.2 eBPF 内核探针

| 探针 | 挂载点 | Map/输出 | 目的 |
|---|---|---|---|
| runqlat | `sched_wakeup`、`sched_wakeup_new`、`sched_switch` | PID 时间表、per-CPU 延迟直方图 | runnable 到真正运行的等待时间 |
| blocklat | `block_rq_issue`、`block_rq_complete` | request 关联表、读写 per-CPU 直方图 | 块请求下发到完成的延迟 |
| tcplat | `tcp_probe`、重传、收发 reset tracepoint | RTT 直方图、计数器、Ring Buffer | TCP 质量与异常关闭 |

探针热路径只做时间戳、查表、分桶和计数。跨 CPU 求和、窗口差值和分位数计算放在用户态，避免在内核上下文中做高成本处理。

### 3.3 Go Control Plane

控制面由五个内部模块组成：

- `ingest`：处理 gRPC 流、消息顺序、输入边界和 ACK；
- `store`：并发安全地保存节点最新快照和路由快照；
- `scoring`：计算 EWMA、分数、健康状态、滞回和冷却；
- `routing`：计算资格、权重、恢复 ramp 和平滑加权轮询；
- `httpapi/prometheus`：对外提供查询、路由和监控接口。

控制面写路径在同一个 Store 写锁内完成“序列检查—合并指标—状态迁移—刷新路由”，确保 API 不会看到健康状态已变但路由尚未更新的中间态。读路径返回深拷贝，避免调用方修改内部 map 或在解锁后发生数据竞争。

### 3.4 网关与观测端

假网关证明路由快照可以被实际消费，但不代理真实业务流量。它使用 ETag 条件轮询和单调版本，缓存最新有效配置，再用平滑加权轮询选择节点。

Prometheus 从 `/metrics` 抓取最新快照，负责长期时序存储；Grafana Dashboard 查询 Prometheus。HTTP 节点 API 面向调试和管理，不代替 TSDB。

## 4. 遥测协议与数据流

### 4.1 握手

同一 gRPC 流的首条消息必须是 `AgentHello`。`node_id` 需匹配限定字符集，Boot ID 标识一次系统启动周期。控制面返回该节点已接受的最大 sequence 和配置版本。

### 4.2 指标批次

`MetricBatch` 包含：

- 严格递增的 `sequence`；
- 纳秒 Unix 采样时间；
- 名称、值、单位和标签组成的指标；
- 内核异常事件。

每批最多接受 2,048 个指标与 1,024 条事件。空名称、NaN、Inf 和超出上限的内容会计入 dropped。单位约定显式写入指标，百分比范围为 0–100，延迟指标使用微秒或纳秒后缀，速率使用 bytes/s。

### 4.3 ACK 与重连

服务端为每批返回 `accepted_sequence`。若 ACK 丢失，Agent 可以在重连后重发；Store 发现 sequence 不大于已接受值时不再次应用，ACK 仍返回当前最大值。连续连接失败时 Agent 从 1 秒开始指数退避，最大 30 秒，避免控制面故障时形成重连风暴。

同 Boot ID 的网络重连保留序列与状态；Boot ID 改变表示节点重启，控制面重置序列、EWMA、滞回候选和恢复冷却。

### 4.4 一致性语义

当前语义是单控制面进程内、至少一次传输下的幂等应用。它不宣称 exactly-once：内存状态未持久化，控制面进程重启后去重历史会丢失。生产化可以持久化每个 `node_id + boot_id` 的 sequence 检查点。

## 5. 健康与路由闭环

评分输入包括 CPU、内存、归一化 Load 和磁盘利用率。普通值先经过 `alpha=0.35` 的 EWMA；原始 CPU、内存、磁盘命中硬门槛时绕过平滑立即进入 unhealthy。

状态迁移遵循快速失败、谨慎恢复：恶化需连续 2 个样本，改善需连续 3 个样本；unhealthy 恢复前等待 30 秒。状态恢复后路由权重从正常值的 10% 开始，在 60 秒内线性升到 100%。

不可用节点权重为 0；healthy 使用健康分；degraded 再乘 0.5。最大余数法把可用节点整数权重归一到总和 10,000。路由内容改变才递增版本，API 用版本作为 ETag。

完整状态转换见 [自适应调度](scheduling.md)。

## 6. 容量与背压

系统在三个位置设置边界：

1. eBPF Map 有固定最大容量，避免内核内存无限增长；
2. TCP Ring Buffer 为 256 KiB，reserve 失败显式计数；
3. Agent 和控制面都限制单批事件/指标数量。

内核事件不能等待慢消费者，否则会影响被观测系统。过载策略是丢弃并计数，而不是反压内核热路径。指标批次通过 gRPC 的 HTTP/2 流量控制获得传输层背压；连接失败时 Agent 有界重试，不在当前实现中无限堆积历史批次。

## 7. 时间与分位数

内核延迟使用单调时钟，避免系统时间校准造成负延迟。TCP Ring Buffer 事件进入用户态时，通过启动时计算的 monotonic-to-Unix 偏移转换为 Unix 纳秒，便于与控制面日志对齐。

延迟使用 32 桶二进制对数直方图。用户态保存上次累计值，以当前值减去上次值获得采集窗口，再返回达到目标分位的桶上界。该方案固定内存、适合热路径，但 P95/P99 是近似值。

## 8. 可观测性

控制面把 Agent 名称规范化为 `sentinelmesh_` 前缀的 Prometheus 指标，并补充 node ID 与 hostname 标签。健康分、连接状态、最后迁移时间、路由资格、权重和配置版本也会导出。

预置 Dashboard 覆盖资源、调度、块 I/O、TCP、事件可靠性与路由。事件丢失需要结合事件量解释：正常窗口丢失率应接近 0；故障注入可以刻意压满通道验证 dropped 计数。

## 9. 故障处理

| 故障 | 当前行为 |
|---|---|
| Agent 断连 | Store 标记 `connected=false`，路由权重变为 0 |
| 重复/倒序批次 | 不应用数据，ACK 返回已接受最大序列 |
| 新 Boot ID | 重建节点生命周期状态，避免前一次启动的异常污染新进程 |
| BPF 加载或挂载失败 | Agent 输出明确错误；可退化为 procfs 模式 |
| Ring Buffer 满 | 丢弃事件并增加 dropped，而不是阻塞内核 |
| 没有可路由节点 | 网关显式失败，不回退到 unhealthy 节点 |
| 路由未变化 | 返回 HTTP 304，网关继续使用缓存快照 |

## 10. 安全与生产化边界

开发环境默认监听本地地址，gRPC 未启用 TLS，特权测试容器使用 `--privileged`。生产部署至少需要：

- gRPC mTLS，证书身份绑定 node ID；
- HTTP 认证、RBAC、审计和网络隔离；
- 收敛为最小 BPF/PERFMON 能力，配合 seccomp 与只读文件系统；
- 限制消息大小、标签基数、每节点速率和连接数；
- 持久化节点元数据与状态检查点；
- 多副本单写者或一致性存储，避免冲突路由版本；
- Agent 包签名、灰度升级与配置回滚。

这些是明确的工程演进项，不属于当前已验证能力。
