# Prometheus和Grafana

## 数据流

控制面在`/metrics`把内存Store中的最新节点快照转换为Prometheus文本格式。Prometheus每5秒抓取一次并保存时序，Grafana使用预置数据源和Dashboard查询Prometheus。控制面不在内存中复制长期时序数据。

```text
C++/Go Agent -> gRPC -> Go Store -> /metrics -> Prometheus -> Grafana
```

## 启动

```bash
docker compose -f deploy/observability.compose.yml up -d --build
```

服务地址：

| 服务 | 地址 | 用途 |
|---|---|---|
| Control Plane | `http://127.0.0.1:8080` | HTTP API和`/metrics` |
| Prometheus | `http://127.0.0.1:9090` | Target状态、PromQL和TSDB |
| Grafana | `http://127.0.0.1:3000` | `SentinelMesh/节点性能总览` |

快速产生数据：

```bash
go run ./cmd/sim-agent \
  --address 127.0.0.1:50051 \
  --node-id observability-demo \
  --count 30
```

## 指标映射

Agent指标名称统一添加`sentinelmesh_`前缀，并将点号等不兼容字符转换为下划线。原有标签会保留，同时所有样本增加`node_id`和`hostname`。

| Agent/Store字段 | Prometheus指标 | 类型 |
|---|---|---|
| `cpu.utilization.percent` | `sentinelmesh_cpu_utilization_percent` | Gauge |
| `scheduler.run_queue.latency.p99.microseconds` | `sentinelmesh_scheduler_run_queue_latency_p99_microseconds` | Gauge |
| `block.io.read.latency.p99.microseconds` | `sentinelmesh_block_io_read_latency_p99_microseconds` | Gauge |
| `tcp.rtt.p99.microseconds` | `sentinelmesh_tcp_rtt_p99_microseconds` | Gauge |
| `kernel.ring_buffer.dropped` | `sentinelmesh_kernel_ring_buffer_dropped` | Gauge（窗口值） |
| 节点事件累计 | `sentinelmesh_node_kernel_events_total` | Counter |
| 节点连接状态 | `sentinelmesh_node_connected` | Gauge（0/1） |
| 节点健康分 | `sentinelmesh_node_health_score` | Gauge（0..100） |
| 最近健康迁移时间 | `sentinelmesh_node_health_changed_timestamp_seconds` | Gauge |
| 最早允许恢复时间 | `sentinelmesh_node_recovery_not_before_timestamp_seconds` | Gauge |
| 路由配置版本 | `sentinelmesh_routing_config_version` | Gauge |
| 节点是否可路由 | `sentinelmesh_node_routing_eligible` | Gauge（0/1） |
| 节点路由权重 | `sentinelmesh_node_routing_weight` | Gauge（总和10,000） |

名称以`_total`结尾的Agent累计计数按Counter导出，其余采集窗口值按Gauge导出。

## Dashboard

预置Dashboard包含9个面板：健康分、Agent连接状态、CPU/内存、调度等待P99、块I/O读写P99、TCP RTT P99、TCP重传/RST、内核事件可靠性和自适应路由权重。`node`变量支持单节点、多节点或全部节点对比。

Dashboard和数据源均通过文件自动配置，无需在Grafana界面手工导入：

- `deploy/grafana/dashboards/sentinelmesh-overview.json`
- `deploy/grafana/provisioning/datasources/sentinelmesh.yml`
- `deploy/grafana/provisioning/dashboards/sentinelmesh.yml`

## 排查

检查抓取目标：

```bash
curl http://127.0.0.1:9090/api/v1/targets
```

直接查询CPU指标：

```bash
curl --get http://127.0.0.1:9090/api/v1/query \
  --data-urlencode 'query=sentinelmesh_cpu_utilization_percent'
```

若Target为`down`，先确认控制面容器存活，再从Prometheus容器访问`http://control-plane:8080/metrics`。若Dashboard没有数据，确认Agent已上报且时间范围包含最近5分钟。

## 安全边界

开发Compose启用了Grafana匿名Viewer，且Prometheus、Grafana和控制面端口都映射到宿主机。它仅用于本地验证；部署到共享网络前应关闭匿名访问、配置认证/TLS，并通过防火墙或反向代理限制管理端口。
