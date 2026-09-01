// ============================================================================
// internal/prometheus/exporter.go
// ----------------------------------------------------------------------------
// Prometheus 文本格式导出器：把内存 Store 的快照转成 Prometheus 抓取格式。
//
// 为什么不用官方 client_golang 的 prometheus.Collector？
// 因为我们的指标集合是动态的（节点数量、指标名都来自 Agent），
// 手写文本格式让“指标名/标签/类型”完全由 Store 数据推导，更灵活；
// 代价是要自己处理排序、标签转义和类型推断。
//
// 指标命名约定：sentinelmesh_ 前缀 + 指标名 sanitize（非法字符转下划线）。
// 类型推断：以 _total 结尾视为 Counter，其余一律 Gauge
// （Prometheus 的 rate/increase 能正确消费重启归零的 Counter）。
// ============================================================================

package prometheus

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/ccKccK-JF/SentinelMesh/internal/store"
)

// Exporter 实现 http.Handler，被 /metrics 路由挂载。
type Exporter struct {
	store *store.Memory
}

// sample 一条指标样本（标签 + 值）。
type sample struct {
	labels map[string]string
	value  float64
}

// family 一个指标族（同名指标的不同标签样本集合）。
type family struct {
	help    string
	typ     string
	samples []sample
}

func New(memory *store.Memory) *Exporter {
	return &Exporter{store: memory}
}

// ServeHTTP 输出 Prometheus 文本格式（text/plain; version=0.0.4）。
// 输出顺序确定：指标族按名排序，同族样本按标签串排序，便于 diff 与测试。
func (e *Exporter) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	families := e.collect()
	names := make([]string, 0, len(families))
	for name := range families {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		metricFamily := families[name]
		if metricFamily.help != "" {
			_, _ = fmt.Fprintf(w, "# HELP %s %s\n", name, metricFamily.help)
		}
		_, _ = fmt.Fprintf(w, "# TYPE %s %s\n", name, metricFamily.typ)
		sort.Slice(metricFamily.samples, func(i, j int) bool {
			return formatLabels(metricFamily.samples[i].labels) <
				formatLabels(metricFamily.samples[j].labels)
		})
		for _, metric := range metricFamily.samples {
			_, _ = fmt.Fprintf(w, "%s%s %s\n", name, formatLabels(metric.labels),
				strconv.FormatFloat(metric.value, 'g', -1, 64))
		}
	}
}

// collect 从 Store 收集全部指标并归类为 metric family。
func (e *Exporter) collect() map[string]*family {
	families := map[string]*family{}
	// add 辅助：把样本塞进（必要时新建）对应指标族
	add := func(name, help, typ string, labels map[string]string, value float64) {
		metricFamily, ok := families[name]
		if !ok {
			metricFamily = &family{help: help, typ: typ}
			families[name] = metricFamily
		}
		metricFamily.samples = append(metricFamily.samples, sample{labels: labels, value: value})
	}

	// ---- 节点维度指标 ----
	for _, node := range e.store.List() {
		baseLabels := map[string]string{"node_id": node.NodeID, "hostname": node.Hostname}
		connected := 0.0
		if node.Connected {
			connected = 1
		}
		add("sentinelmesh_node_connected", "Whether the node Agent has an active stream.", "gauge", baseLabels, connected)
		add("sentinelmesh_node_health_score", "Current node health score from 0 to 100.", "gauge", baseLabels, node.HealthScore)
		statusLabels := copyLabels(baseLabels)
		statusLabels["status"] = string(node.HealthStatus)
		add("sentinelmesh_node_health_status", "Current node health status as a labeled value.", "gauge", statusLabels, 1)
		if !node.HealthChangedAt.IsZero() {
			add("sentinelmesh_node_health_changed_timestamp_seconds", "Unix timestamp of the latest health state transition.", "gauge", baseLabels, float64(node.HealthChangedAt.UnixNano())/1e9)
		}
		if !node.RecoveryNotBefore.IsZero() {
			add("sentinelmesh_node_recovery_not_before_timestamp_seconds", "Earliest Unix timestamp at which an unhealthy node may recover.", "gauge", baseLabels, float64(node.RecoveryNotBefore.UnixNano())/1e9)
		}
		add("sentinelmesh_node_kernel_events_total", "Kernel events accepted since the current node boot identity was registered.", "counter", baseLabels, float64(node.KernelEventCount))
		add("sentinelmesh_node_last_seen_timestamp_seconds", "Unix timestamp of the last node activity.", "gauge", baseLabels, float64(node.LastSeen.UnixNano())/1e9)

		// ---- Agent 上报的原始指标（动态名，需 sanitize）----
		keys := make([]string, 0, len(node.Metrics))
		for key := range node.Metrics {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			metric := node.Metrics[key]
			// key 形如 "name{label=value,...}"，导出时需要拆出原始指标名
			originalName := key
			if index := strings.IndexByte(originalName, '{'); index >= 0 {
				originalName = originalName[:index]
			}
			name := "sentinelmesh_" + sanitizeName(originalName)
			typ := "gauge"
			if strings.HasSuffix(name, "_total") {
				typ = "counter"
			}
			labels := copyLabels(metric.Labels)
			labels["node_id"] = node.NodeID
			labels["hostname"] = node.Hostname
			add(name, "", typ, labels, metric.Value)
		}
	}

	// ---- 路由维度指标 ----
	routes := e.store.Routing()
	add("sentinelmesh_routing_config_version", "Monotonic routing assignment version.", "gauge", nil, float64(routes.Version))
	for _, assignment := range routes.Nodes {
		labels := map[string]string{
			"node_id": assignment.NodeID,
			"status":  string(assignment.HealthStatus),
			"reason":  assignment.Reason,
		}
		eligible := 0.0
		if assignment.Eligible {
			eligible = 1
		}
		add("sentinelmesh_node_routing_eligible", "Whether a node may receive new routed work.", "gauge", labels, eligible)
		add("sentinelmesh_node_routing_weight", "Normalized node routing weight out of 10000.", "gauge", labels, float64(assignment.Weight))
	}
	return families
}

// sanitizeName 把任意指标名清洗为合法的 Prometheus 指标名
// （字母/数字/下划线，且不能以数字开头）。
func sanitizeName(name string) string {
	var result strings.Builder
	for _, character := range []byte(name) {
		if character == '_' || character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' {
			result.WriteByte(character)
		} else {
			result.WriteByte('_')
		}
	}
	if result.Len() == 0 {
		return "unknown_metric"
	}
	sanitized := result.String()
	if sanitized[0] >= '0' && sanitized[0] <= '9' {
		return "_" + sanitized
	}
	return sanitized
}

// copyLabels 拷贝标签 map（保留额外容量，性能微优化）。
func copyLabels(labels map[string]string) map[string]string {
	result := make(map[string]string, len(labels)+2)
	for key, value := range labels {
		result[key] = value
	}
	return result
}

// formatLabels 把标签 map 渲染成 Prometheus 的 {k="v",...} 形式。
// 按键排序保证输出确定性；键名同样要 sanitize。
func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, sanitizeName(key)+`="`+escapeLabel(labels[key])+`"`)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// escapeLabel 转义标签值中的反斜杠、换行与双引号（Prometheus 文本格式要求）。
func escapeLabel(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\n", "\\n")
	return strings.ReplaceAll(value, `"`, `\"`)
}
