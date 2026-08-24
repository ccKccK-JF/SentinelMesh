package store

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/ccKccK-JF/SentinelMesh/internal/domain"
	"github.com/ccKccK-JF/SentinelMesh/internal/scoring"
)

var (
	ErrNodeNotFound     = errors.New("node not found")
	ErrSequenceNotNewer = errors.New("batch sequence is not newer than the accepted sequence")
)

type Hello struct {
	NodeID       string
	Hostname     string
	IPAddress    string
	AgentVersion string
	BootID       string
}

type Memory struct {
	mu     sync.RWMutex
	nodes  map[string]*domain.NodeSnapshot
	scorer scoring.Scorer
	now    func() time.Time
}

func NewMemory(scorer scoring.Scorer) *Memory {
	return &Memory{
		nodes:  make(map[string]*domain.NodeSnapshot),
		scorer: scorer,
		now:    time.Now,
	}
}

func (m *Memory) Connect(hello Hello) domain.NodeSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	node, ok := m.nodes[hello.NodeID]
	if !ok || node.BootID != hello.BootID {
		m.scorer.Reset(hello.NodeID)
		node = &domain.NodeSnapshot{
			NodeID:       hello.NodeID,
			Metrics:      make(map[string]domain.Metric),
			HealthStatus: domain.HealthUnknown,
		}
		m.nodes[hello.NodeID] = node
	}
	node.Hostname = hello.Hostname
	node.IPAddress = hello.IPAddress
	node.AgentVersion = hello.AgentVersion
	node.BootID = hello.BootID
	node.Connected = true
	node.LastSeen = m.now().UTC()
	return clone(*node)
}

func (m *Memory) Disconnect(nodeID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if node, ok := m.nodes[nodeID]; ok {
		node.Connected = false
		node.LastSeen = m.now().UTC()
	}
}

func (m *Memory) ApplyBatch(nodeID string, sequence uint64, observedAt time.Time, metrics map[string]domain.Metric, kernelEvents int) (domain.NodeSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	node, ok := m.nodes[nodeID]
	if !ok {
		return domain.NodeSnapshot{}, ErrNodeNotFound
	}
	if sequence <= node.LastSequence {
		return clone(*node), ErrSequenceNotNewer
	}

	for name, metric := range metrics {
		node.Metrics[name] = copyMetric(metric)
	}
	now := m.now().UTC()
	result := m.scorer.Evaluate(nodeID, node.Metrics, now)
	node.LastSequence = sequence
	node.ObservedAt = observedAt.UTC()
	node.LastSeen = now
	node.HealthScore = result.Score
	node.HealthStatus = result.Status
	node.HealthReason = result.Reason
	node.HealthChangedAt = result.ChangedAt
	node.RecoveryNotBefore = result.RecoveryNotBefore
	node.KernelEventCount += uint64(kernelEvents)
	return clone(*node), nil
}

func (m *Memory) Touch(nodeID string) (domain.NodeSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	node, ok := m.nodes[nodeID]
	if !ok {
		return domain.NodeSnapshot{}, ErrNodeNotFound
	}
	node.LastSeen = m.now().UTC()
	return clone(*node), nil
}

func (m *Memory) Get(nodeID string) (domain.NodeSnapshot, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	node, ok := m.nodes[nodeID]
	if !ok {
		return domain.NodeSnapshot{}, false
	}
	return clone(*node), true
}

func (m *Memory) List() []domain.NodeSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	nodes := make([]domain.NodeSnapshot, 0, len(m.nodes))
	for _, node := range m.nodes {
		nodes = append(nodes, clone(*node))
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].NodeID < nodes[j].NodeID })
	return nodes
}

func clone(node domain.NodeSnapshot) domain.NodeSnapshot {
	copyNode := node
	copyNode.Metrics = make(map[string]domain.Metric, len(node.Metrics))
	for name, metric := range node.Metrics {
		copyNode.Metrics[name] = copyMetric(metric)
	}
	return copyNode
}

func copyMetric(metric domain.Metric) domain.Metric {
	copyValue := metric
	if metric.Labels != nil {
		copyValue.Labels = make(map[string]string, len(metric.Labels))
		for key, value := range metric.Labels {
			copyValue.Labels[key] = value
		}
	}
	return copyValue
}
