package taskalloc

// interval_treap.go：按 (start, id) 排序的 treap，子树聚合 maxEnd，
// 支持对某一时刻的 stabbing 查询（收集该时刻占用容量的任务 id）。

type ivlNode struct {
	start int64
	end   int64
	id    string
	prio  uint32
	left  *ivlNode
	right *ivlNode

	maxEnd int64 // 子树最大 end
}

func (n *ivlNode) update() {
	m := n.end
	if n.left != nil && n.left.maxEnd > m {
		m = n.left.maxEnd
	}
	if n.right != nil && n.right.maxEnd > m {
		m = n.right.maxEnd
	}
	n.maxEnd = m
}

func ivlLess(aStart int64, aID string, bStart int64, bID string) bool {
	if aStart != bStart {
		return aStart < bStart
	}
	return aID < bID
}

// splitIvl 按键 (start, id) 把树分成左小右大；inclusive 为真时
// 等于该键的节点归入左树。
func splitIvl(n *ivlNode, start int64, id string, inclusive bool) (*ivlNode, *ivlNode) {
	if n == nil {
		return nil, nil
	}
	less := ivlLess(n.start, n.id, start, id)
	eq := n.start == start && n.id == id
	if less || (inclusive && eq) {
		l, r := splitIvl(n.right, start, id, inclusive)
		n.right = l
		n.update()
		return n, r
	}
	l, r := splitIvl(n.left, start, id, inclusive)
	n.left = r
	n.update()
	return l, n
}

func mergeIvl(l, r *ivlNode) *ivlNode {
	if l == nil {
		return r
	}
	if r == nil {
		return l
	}
	if l.prio < r.prio {
		l.right = mergeIvl(l.right, r)
		l.update()
		return l
	}
	r.left = mergeIvl(l, r.left)
	r.update()
	return r
}

// addInterval 插入区间 [start, end)，id 在操作员内唯一。
func addInterval(root *ivlNode, start, end int64, id string) *ivlNode {
	l, r := splitIvl(root, start, id, false)
	node := &ivlNode{start: start, end: end, id: id, prio: nextRand()}
	node.update()
	return mergeIvl(mergeIvl(l, node), r)
}

// removeInterval 删除键 (start, id) 对应的区间。
func removeInterval(root *ivlNode, start int64, id string) *ivlNode {
	l, r := splitIvl(root, start, id, false)
	_, r = splitIvl(r, start, id, true) // 丢弃等于该键的节点
	return mergeIvl(l, r)
}

// collectAt 收集在时刻 t 占用容量（start <= t < end）的任务 id，
// 最多 limit 个。利用 maxEnd 与键序剪枝，代价 O(log n + k)。
func collectAt(root *ivlNode, t int64, limit int, out []string) []string {
	if root == nil || len(out) >= limit {
		return out
	}
	if root.left != nil && root.left.maxEnd > t {
		out = collectAt(root.left, t, limit, out)
	}
	if len(out) < limit && root.start <= t && root.end > t {
		out = append(out, root.id)
	}
	// 右子树键均大于本节点键，若本节点 start 已大于 t 则右子树全部未开始。
	if len(out) < limit && root.start <= t && root.right != nil && root.right.maxEnd > t {
		out = collectAt(root.right, t, limit, out)
	}
	return out
}
