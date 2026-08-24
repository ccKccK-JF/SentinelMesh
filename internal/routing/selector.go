package routing

import "sort"

type selectorNode struct {
	id      string
	weight  int64
	current int64
}

type Selector struct {
	nodes []selectorNode
	total int64
}

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

// Next implements smooth weighted round robin with deterministic node-ID ties.
func (s *Selector) Next() (string, bool) {
	if len(s.nodes) == 0 || s.total == 0 {
		return "", false
	}
	winner := 0
	for index := range s.nodes {
		s.nodes[index].current += s.nodes[index].weight
		if s.nodes[index].current > s.nodes[winner].current {
			winner = index
		}
	}
	s.nodes[winner].current -= s.total
	return s.nodes[winner].id, true
}
