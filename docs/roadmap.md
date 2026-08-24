# Roadmap

## M1：基础遥测闭环

- [x] 跨语言 Protobuf 契约
- [x] Go gRPC 双向流接入
- [x] 内存节点 Store 和 HTTP API
- [x] 基础健康评分及单元测试
- [x] Go 模拟 Agent
- [x] C++ procfs 采集器和解析测试
- [x] C++ gRPC双向流客户端
- [x] Hello ACK续传语义和指数退避重连
- [x] C++ Agent到Go控制面的跨语言端到端测试

## M2：内核性能诊断

- [x] C++ libbpf CO-RE Loader
- [x] 调度等待延迟直方图与P95/P99上报
- [x] 块 I/O 请求关联、读写延迟直方图与fio验证
- [x] TCP RTT、重传和异常关闭
- [x] Ring Buffer 异常事件通道和丢失事件计数
- [ ] Prometheus 指标和 Grafana Dashboard

## M3：自适应调度

- [ ] EWMA 指标平滑
- [ ] 硬门槛和节点摘除
- [ ] unhealthy/degraded/healthy 状态滞回
- [ ] 恢复冷却与渐进增权
- [ ] 带版本号的路由权重下发
- [ ] 假网关/游戏服分配器验证闭环

## M4：实验与简历数据

- [ ] stress-ng CPU/内存注入
- [ ] fio 磁盘延迟注入
- [ ] iperf3 与 tc netem 网络注入
- [ ] Round Robin 与自适应策略对照实验
- [ ] Agent CPU/内存开销和事件丢失率报告
- [ ] P95/P99、错误率、摘除和恢复时间报告
