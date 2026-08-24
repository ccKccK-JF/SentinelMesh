package prometheus

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/ccKccK-JF/SentinelMesh/internal/store"
)

type Exporter struct {
	store *store.Memory
}

type sample struct {
	labels map[string]string
	value  float64
}

type family struct {
	help    string
	typ     string
	samples []sample
}

func New(memory *store.Memory) *Exporter {
	return &Exporter{store: memory}
}

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

func (e *Exporter) collect() map[string]*family {
	families := map[string]*family{}
	add := func(name, help, typ string, labels map[string]string, value float64) {
		metricFamily, ok := families[name]
		if !ok {
			metricFamily = &family{help: help, typ: typ}
			families[name] = metricFamily
		}
		metricFamily.samples = append(metricFamily.samples, sample{labels: labels, value: value})
	}

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
		add("sentinelmesh_node_kernel_events_total", "Kernel events accepted since the current node boot identity was registered.", "counter", baseLabels, float64(node.KernelEventCount))
		add("sentinelmesh_node_last_seen_timestamp_seconds", "Unix timestamp of the last node activity.", "gauge", baseLabels, float64(node.LastSeen.UnixNano())/1e9)

		keys := make([]string, 0, len(node.Metrics))
		for key := range node.Metrics {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			metric := node.Metrics[key]
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
	return families
}

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

func copyLabels(labels map[string]string) map[string]string {
	result := make(map[string]string, len(labels)+2)
	for key, value := range labels {
		result[key] = value
	}
	return result
}

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
		parts = append(parts, sanitizeName(key)+"=\""+escapeLabel(labels[key])+"\"")
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func escapeLabel(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\n", "\\n")
	return strings.ReplaceAll(value, "\"", "\\\"")
}
