# 交付状态与演进方向

## 已完成并验证

### 遥测闭环

- [x] 跨语言 Protobuf 契约
- [x] Go gRPC 双向流接入
- [x] Hello/sequence/ACK 幂等语义
- [x] 同 Boot ID 续传与新 Boot 状态重置
- [x] 1–30 秒指数退避重连
- [x] 并发安全节点 Store 与 HTTP API
- [x] C++ procfs Agent 和 Go 可控模拟 Agent
- [x] C++ Agent 到 Go 控制面的跨语言 E2E

### 内核性能诊断

- [x] C++ libbpf CO-RE Loader
- [x] 调度等待延迟直方图与 P95/P99
- [x] 块 I/O 请求关联、读写延迟与事件数
- [x] TCP RTT、重传、收发 RST
- [x] Ring Buffer 异常事件与丢失计数
- [x] Prometheus 指标和 9 面板 Grafana Dashboard

### 自适应路由

- [x] EWMA 指标平滑
- [x] CPU/内存/磁盘硬门槛立即 unhealthy
- [x] 恶化/改善滞回与恢复冷却
- [x] 断连、unknown、unhealthy 节点摘除
- [x] degraded 权重惩罚
- [x] 恢复节点渐进增权
- [x] 总和 10,000 的版本化权重快照
- [x] ETag/304 与平滑加权轮询假网关

### 实验与工程质量

- [x] stress-ng CPU/内存故障注入
- [x] fio 磁盘延迟故障注入
- [x] iperf3 与 tc netem 网络故障注入
- [x] Round Robin 与自适应策略确定性对照
- [x] Agent CPU/内存开销报告
- [x] 正常与过载窗口事件丢失率报告
- [x] P95/P99、错误率、摘除和恢复模型报告
- [x] Go race/vet、C++ CTest、跨语言与路由 E2E 的 CI 门禁

## 后续生产化方向

以下内容未在当前仓库中实现，面试和简历中应作为演进方案描述：

- [ ] gRPC mTLS、Agent 身份和证书轮换
- [ ] 控制面持久化、多副本和 leader/单写者机制
- [ ] HTTP 管理面认证、RBAC 与审计
- [ ] eBPF 最小权限容器、seccomp 与包签名
- [ ] 告警规则、通知渠道和告警抑制
- [ ] 网关真实流量灰度、影子决策和一键回退
- [ ] 物理机长稳压测与不同内核版本兼容矩阵
- [ ] Agent 包发布、自动升级和配置签名
