# 架构设计

## 1. 设计目标

SentinelMesh 不以“采集尽可能多的指标”为目标，而是解决三个连续问题：

1. 哪个节点正在变慢？
2. 变慢发生在 CPU 调度、磁盘 I/O、网络还是应用层？
3. 在定位期间，如何降低异常节点承载的流量并避免权重来回振荡？

## 2. 组件边界

### C++ Node Agent

部署在 Linux 工作节点，职责保持克制：

- 从 procfs 和 cgroup v2 采集低频资源指标。
- 加载 eBPF 程序并读取 BPF Map/Ring Buffer。
- 在本地时间窗口中聚合指标，限制高频事件量。
- 通过 gRPC 双向流批量上报，并在断线时进行有界缓存。

Agent 不负责全局评分和路由决策，避免每个节点持有不一致的集群视图。

块I/O探针使用`block_rq_issue`和`block_rq_complete`携带的同一个`struct request *`作为关联键，在下发时记录时间和读写方向，在完成时计算延迟并写入读/写独立的per-CPU直方图。in-flight Map使用LRU上限，避免异常请求长期未完成时无限增长。

### Go Control Plane

- 维护 Agent 长连接和节点生命周期。
- 校验批次序列号，拒绝重复或倒序批次。
- 保存最新节点快照并输出查询 API。
- 计算健康状态；M3 将生成带版本号的路由权重。
- 通过`/metrics`向Prometheus暴露节点状态和Agent聚合指标，时序数据交由专用TSDB保存。

## 3. 数据协议

Agent 建立流后必须先发送 `AgentHello`。此后的 `MetricBatch.sequence` 在同一 Agent 启动周期内严格递增。控制面为每个批次返回 `CollectorAck`：

- `accepted_sequence`：已确认的最大序列号；
- `accepted_samples`：本批次接收的指标数；
- `dropped_samples`：因校验或限流丢弃的样本数；
- `config_version`：M3 用于下发配置版本。

控制面也会在Hello确认中返回该节点此前接受的最大序列号。Agent重连后从下一序列继续；如果批次已送达但ACK在断线时丢失，Agent可以重发同一序列，控制面返回已接受序列而不会重复应用指标。连续连接失败时Agent采用1秒到30秒的指数退避。

协议刻意使用明确的单位字段，内部约定：百分比为 `0..100`，延迟用纳秒，字节速率用 `bytes_per_second`。

## 4. 健康评分

M1 的基础评分只负责建立可测试接口：

- `cpu.utilization.percent`
- `memory.utilization.percent`
- `system.load.normalized`
- `disk.io.utilization.percent`

先应用硬门槛，再计算 0 到 100 的加权分。M2/M3 将加入调度延迟、TCP 重传、服务错误率、EWMA 和状态滞回。所有阈值必须通过配置注入，不能散落在业务代码中。

## 5. 存储策略

- 内存 Store：保存最新节点状态，为 M1 查询与测试服务。
- Prometheus：M2 保存聚合后的时序指标。
- 关系型数据库：只在需要时保存节点元数据、告警规则和审计记录，不存储每个高频样本。

## 6. 安全边界

M1 仅监听开发地址且未启用 TLS，不用于公网。生产化之前必须完成：

- gRPC mTLS 与 Agent 身份认证；
- 每节点速率限制和最大消息大小；
- eBPF 最小能力集，避免长期使用完整 privileged 容器；
- 控制指令白名单和配置版本校验。
