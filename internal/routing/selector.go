// ============================================================================
// internal/routing/selector.go
// ----------------------------------------------------------------------------
// 平滑加权轮询（Smooth Weighted Round Robin, SWRR）：网关侧的流量分配器。
//
// 经典加权轮询（WRR）的问题是：权重高的节点容易在短时间内被连续打满，
// 产生“潮汐效应”。SWRR 通过引入 current（动态 current weight）做平滑：
//
//   - 每次调用 Next()：所有节点 current += weight，选择 current 最大的节点；
//   - 被选中节点 current -= total（总权重）；
//   - 于是每一轮下来，各节点实际分配比例 ≈ 权重比例，且分布均匀。
//
// 该实现是确定性算法：节点按 ID 排序，相同快照 + 相同调用次数必然产出
// 相同分配序列，便于测试与回归。
// ============================================================================

package routing

import "sort"

// selectorNode 单个节点的轮询状态。
type selectorNode struct {
	id      string
	weight  int64 // effective weight（静态，来自路由快照）
	current int64 // current weight（动态，算法核心）
}

// Selector 平滑加权轮询选择器。
type Selector struct {
	nodes []selectorNode
	total int64 // 全部节点权重之和
}

// NewSelector 由路由快照构造选择器。
// 只纳入 eligible 且 weight>0 的节点；节点按 ID 排序保证确定性。
func NewSelector(snapshot Snapshot) *Selector {
	selector := &Selector{}
	for _, assignment := range snapshot.Nodes {
		if !assignment.Eligible || assignment.Weight == 0 {
			continue
		}
		selector.nodes = append(selector.nodes, selectorNode{
			id: assignment.NodeID, weight: int64(assignment.Weight),
		})
		selector.total += int64(assignment.Weight)
	}
	sort.Slice(selector.nodes, func(i, j int) bool { return selector.nodes[i].id < selector.nodes[j].id })
	return selector
}

// Next 返回下一个要分配请求的节点。
// 返回 (nodeID, true) 表示成功；无可用节点时返回 ("", false)。
func (s *Selector) Next() (string, bool) {
	if len(s.nodes) == 0 || s.total == 0 {
		return "", false
	}
	// 第一遍：所有节点 current += weight，同时找出 current 最大者
	winner := 0
	for index := range s.nodes {
		s.nodes[index].current += s.nodes[index].weight
		if s.nodes[index].current > s.nodes[winner].current {
			winner = index
		}
	}
	// 选中节点扣减总权重，让它的“势能”回落到均值附近，实现平滑
	s.nodes[winner].current -= s.total
	return s.nodes[winner].id, true
}
