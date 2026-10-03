package taskalloc

// event_treap.go：按时间键组织的 treap，维护每个操作员占用数
// 随时间变化的差分序列。子树聚合 sum（差分和）与 maxPrefix
// （中序差分序列的最大前缀和），从而 O(1) 得到全局峰值占用，
// O(log n) 定位首次超载时刻。

// 占用区间左闭右开：开始时刻 +1，截止时刻 -1。
// 时刻 t 的占用数 = 所有 key <= t 的差分之和。

type eventNode struct {
	key       int64 // 时刻
	delta     int   // 该时刻的净占用变化
	prio      uint32
	left      *eventNode
	right     *eventNode
	sum       int // 子树差分总和
	maxPrefix int // 子树中序差分序列的最大前缀和（含空前缀 0）
}

var rngState uint64 = 0x9E3779B97F4A7C15

// nextRand 是确定性的 xorshift64*，调用串行，无需加锁。
func nextRand() uint32 {
	rngState ^= rngState << 13
	rngState ^= rngState >> 7
	rngState ^= rngState << 17
	return uint32(rngState * 2685821657736338717 >> 32)
}

func (n *eventNode) update() {
	var ls, lm, rs, rm int
	if n.left != nil {
		ls, lm = n.left.sum, n.left.maxPrefix
	}
	if n.right != nil {
		rs, rm = n.right.sum, n.right.maxPrefix
	}
	n.sum = ls + n.delta + rs
	// 中序为 [左序列, 本节点, 右序列]；rm、lm 均含空前缀故非负。
	n.maxPrefix = lm
	if v := ls + n.delta + rm; v > n.maxPrefix {
		n.maxPrefix = v
	}
}

// splitEvents 按键把树分成 (< key) 与 (>= key)；inclusive 为真时
// 等于 key 的节点归入左树。
func splitEvents(n *eventNode, key int64, inclusive bool) (*eventNode, *eventNode) {
	if n == nil {
		return nil, nil
	}
	if n.key < key || (inclusive && n.key == key) {
		l, r := splitEvents(n.right, key, inclusive)
		n.right = l
		n.update()
		return n, r
	}
	l, r := splitEvents(n.left, key, inclusive)
	n.left = r
	n.update()
	return l, n
}

func mergeEvents(l, r *eventNode) *eventNode {
	if l == nil {
		return r
	}
	if r == nil {
		return l
	}
	if l.prio < r.prio {
		l.right = mergeEvents(l.right, r)
		l.update()
		return l
	}
	r.left = mergeEvents(l, r.left)
	r.update()
	return r
}

// addEvent 在 key 处累加差分 d；差分归零时删除该节点。
func addEvent(root *eventNode, key int64, d int) *eventNode {
	l, r := splitEvents(root, key, false)
	eq, r := splitEvents(r, key, true) // eq 至多含 key 一个节点
	if eq != nil {
		eq.delta += d
		if eq.delta == 0 {
			eq = nil
		} else {
			eq.update()
		}
	} else if d != 0 {
		eq = &eventNode{key: key, delta: d, prio: nextRand()}
		eq.update()
	}
	return mergeEvents(mergeEvents(l, eq), r)
}

// firstOverload 返回首个使累计占用达到 threshold 的时刻。
// 调用前需保证根 maxPrefix >= threshold。
func firstOverload(root *eventNode, threshold int) (int64, bool) {
	base := 0 // 当前子树之前所有键的差分和
	n := root
	for n != nil {
		var ls, lm int
		if n.left != nil {
			ls, lm = n.left.sum, n.left.maxPrefix
		}
		if base+lm >= threshold {
			n = n.left
			continue
		}
		if base+ls+n.delta >= threshold {
			return n.key, true
		}
		base += ls + n.delta
		n = n.right
	}
	return 0, false
}
