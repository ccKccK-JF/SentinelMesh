// ============================================================================
// internal/store/memory.go
// ----------------------------------------------------------------------------
// 内存 Store：控制面的“数据中心”，用一个 sync.RWMutex 保护全局状态。
//
// 存储内容：
//   - nodes:  node_id -> 最新节点快照（只存最新，不存历史时序）
//   - routes: 当前路由权重快照（带单调版本号，供网关 ETag/304 消费）
//   - scorer: 健康评分状态机（EWMA/滞回/冷却状态）
//   - router: 路由权重策略
//
// 并发设计：所有写路径（Connect/Disconnect/ApplyBatch/Touch）拿写锁，
// 读路径（Get/List/Routing）拿读锁；对外返回的永远是深拷贝（clone），
// 避免调用方持有内部 map 引用，从而保证并发安全与数据不变性。
//
// 面试要点：为什么重启会丢状态？因为这是内存 Store —— 高频遥测样本交给
// Prometheus TSDB，控制面只负责“最新状态 + 实时决策”，职责分离。
// ============================================================================

package store

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/ccKccK-JF/SentinelMesh/internal/domain"
	"github.com/ccKccK-JF/SentinelMesh/internal/routing"
	"github.com/ccKccK-JF/SentinelMesh/internal/scoring"
)

var (
	ErrNodeNotFound     = errors.New("node not found")
	ErrSequenceNotNewer = errors.New("batch sequence is not newer than the accepted sequence")
)

// Hello 是 ingest 层翻译后的注册信息，解耦 gRPC 消息与存储层。
type Hello struct {
	NodeID       string
	Hostname     string
	IPAddress    string
	AgentVersion string
	BootID       string
}

// Memory 是内存存储实现。
// now 字段是可注入的时钟函数，便于测试里固定时间（依赖倒置）。
type Memory struct {
	mu     sync.RWMutex
	nodes  map[string]*domain.NodeSnapshot
	scorer scoring.Scorer    // 健康状态机
	router routing.Policy    // 路由权重策略
	routes routing.Snapshot  // 最新路由快照
	now    func() time.Time  // 时钟函数（默认真实时钟，测试可替换）
}

func NewMemory(scorer scoring.Scorer) *Memory {
	return &Memory{
		nodes:  make(map[string]*domain.NodeSnapshot),
		scorer: scorer,
		router: routing.New(routing.DefaultConfig()),
		routes: routing.Snapshot{Nodes: []routing.Assignment{}},
		now:    time.Now,
	}
}

// Connect 处理 Agent 注册。
// 关键逻辑：Boot ID 决定“是否同一个启动生命周期”。
//   - 节点不存在，或 Boot ID 变化（系统重启了）-> 重置评分器、新建快照；
//   - 相同 Boot ID 的重连 -> 保留已有快照与评分状态，只更新元信息。
//
// 该区分是幂等语义的基础：Boot 变化后 sequence 重新从 1 开始，
// 不会把新周期的 sequence=1 误判为旧周期的重复数据。
func (m *Memory) Connect(hello Hello) domain.NodeSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	node, ok := m.nodes[hello.NodeID]
	if !ok || node.BootID != hello.BootID {
		m.scorer.Reset(hello.NodeID) // 清空该节点的 EWMA/候选/冷却状态
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
	now := m.now().UTC()
	node.LastSeen = now
	m.refreshRoutingLocked(now) // 连接状态变了，路由需要重算（权重可能恢复）
	return clone(*node)
}

// Disconnect 标记节点离线。路由策略会把离线节点权重降为 0。
func (m *Memory) Disconnect(nodeID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if node, ok := m.nodes[nodeID]; ok {
		node.Connected = false
		now := m.now().UTC()
		node.LastSeen = now
		m.refreshRoutingLocked(now)
	}
}

// ApplyBatch 应用一批指标：合并指标 -> 评分 -> 刷新路由。
// 幂等保证：只接受 sequence > last_sequence 的批次，重复/乱序批次
// 返回 ErrSequenceNotNewer（但快照保持最新，ACK 仍可正常回）。
func (m *Memory) ApplyBatch(nodeID string, sequence uint64, observedAt time.Time, metrics map[string]domain.Metric, kernelEvents int) (domain.NodeSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	node, ok := m.nodes[nodeID]
	if !ok {
		return domain.NodeSnapshot{}, ErrNodeNotFound
	}
	// 核心幂等判断：序列必须严格递增
	if sequence <= node.LastSequence {
		return clone(*node), ErrSequenceNotNewer
	}

	// 合并最新指标（同名同标签覆盖）
	for name, metric := range metrics {
		node.Metrics[name] = copyMetric(metric)
	}
	now := m.now().UTC()
	// 评分状态机：EWMA 平滑 + 硬门槛 + 滞回 + 冷却，返回评分与状态
	result := m.scorer.Evaluate(nodeID, node.Metrics, now)
	node.LastSequence = sequence
	node.ObservedAt = observedAt.UTC()
	node.LastSeen = now
	node.HealthScore = result.Score
	node.HealthStatus = result.Status
	node.HealthReason = result.Reason
	node.HealthChangedAt = result.ChangedAt
	node.RecoveryNotBefore = result.RecoveryNotBefore
	node.RecoveryStartedAt = result.RecoveryStartedAt
	node.KernelEventCount += uint64(kernelEvents) // 内核事件只累加计数，不存原始事件
	m.refreshRoutingLocked(now)
	return clone(*node), nil
}

// Touch 处理心跳：只刷新 LastSeen，不评分、不重算路由。
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

// Get 读取单个节点（深拷贝）。
func (m *Memory) Get(nodeID string) (domain.NodeSnapshot, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	node, ok := m.nodes[nodeID]
	if !ok {
		return domain.NodeSnapshot{}, false
	}
	return clone(*node), true
}

// List 返回所有节点快照，按 NodeID 排序保证输出确定性（利于测试与 diff）。
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

// Routing 返回当前路由快照的深拷贝（网关消费入口）。
func (m *Memory) Routing() routing.Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return routing.Clone(m.routes)
}

// refreshRoutingLocked 在写锁内重算路由权重。
// 优化：只有当权重分配真的发生变化时才递增版本号（版本号用于 ETag/304 与乱序保护），
// 避免“假变化”导致网关每次都重新拉取。
func (m *Memory) refreshRoutingLocked(now time.Time) {
	nodes := make([]domain.NodeSnapshot, 0, len(m.nodes))
	for _, node := range m.nodes {
		nodes = append(nodes, *node)
	}
	assignments := m.router.Compute(nodes, now)
	if routing.EqualAssignments(m.routes.Nodes, assignments) {
		return // 权重无变化：版本号不动，网关缓存继续有效
	}
	m.routes.Version++       // 单调递增版本号
	m.routes.GeneratedAt = now
	m.routes.Nodes = assignments
}

// clone 深拷贝 NodeSnapshot，切断与内部 map 的引用关系。
// 这是并发安全的关键：外部拿到的快照可以随便改，不影响 Store 内部状态。
func clone(node domain.NodeSnapshot) domain.NodeSnapshot {
	copyNode := node
	copyNode.Metrics = make(map[string]domain.Metric, len(node.Metrics))
	for name, metric := range node.Metrics {
		copyNode.Metrics[name] = copyMetric(metric)
	}
	return copyNode
}

// copyMetric 深拷贝 Metric，尤其要拷贝 Labels map。
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
