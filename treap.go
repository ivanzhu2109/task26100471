package admission

// interval 为一条占用容量的左闭右开时间区间。
type interval struct {
	start int64
	end   int64
	id    string
}

// covers 报告区间是否覆盖时刻 t（左闭右开）。
func (iv interval) covers(t int64) bool { return iv.start <= t && t < iv.end }

// itreap 为按 (start, end, id) 排序的区间 treap，
// 子树内维护 maxEnd 以支持区间相交枚举的剪枝。
// insert/remove 为 O(log n)，intersecting 为 O(log n + k)，k 为命中数。
type itreap struct {
	root *inode
	size int
	seed uint64
}

type inode struct {
	iv          interval
	prio        uint64
	left, right *inode
	maxEnd      int64
}

func lessIV(a, b interval) bool {
	if a.start != b.start {
		return a.start < b.start
	}
	if a.end != b.end {
		return a.end < b.end
	}
	return a.id < b.id
}

func (n *inode) fix() {
	m := n.iv.end
	if n.left != nil && n.left.maxEnd > m {
		m = n.left.maxEnd
	}
	if n.right != nil && n.right.maxEnd > m {
		m = n.right.maxEnd
	}
	n.maxEnd = m
}

// nextPrio 生成确定性的伪随机优先级（splitmix64）。
func (tr *itreap) nextPrio() uint64 {
	tr.seed += 0x9e3779b97f4a7c15
	z := tr.seed
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// insert 插入一个区间（键唯一）。
func (tr *itreap) insert(iv interval) {
	tr.root = tr.insertNode(tr.root, iv, tr.nextPrio())
	tr.size++
}

func (tr *itreap) insertNode(n *inode, iv interval, prio uint64) *inode {
	if n == nil {
		return &inode{iv: iv, prio: prio, maxEnd: iv.end}
	}
	if lessIV(iv, n.iv) {
		n.left = tr.insertNode(n.left, iv, prio)
		if n.left.prio < n.prio {
			n = rotateRight(n)
		}
	} else {
		n.right = tr.insertNode(n.right, iv, prio)
		if n.right.prio < n.prio {
			n = rotateLeft(n)
		}
	}
	n.fix()
	return n
}

func rotateRight(n *inode) *inode {
	l := n.left
	n.left = l.right
	l.right = n
	n.fix()
	l.fix()
	return l
}

func rotateLeft(n *inode) *inode {
	r := n.right
	n.right = r.left
	r.left = n
	n.fix()
	r.fix()
	return r
}

// remove 删除与 iv 完全相同的区间。调用方保证其存在。
func (tr *itreap) remove(iv interval) {
	tr.root = tr.removeNode(tr.root, iv)
	tr.size--
}

func (tr *itreap) removeNode(n *inode, iv interval) *inode {
	if n == nil {
		return nil
	}
	switch {
	case lessIV(iv, n.iv):
		n.left = tr.removeNode(n.left, iv)
	case lessIV(n.iv, iv):
		n.right = tr.removeNode(n.right, iv)
	default:
		return mergeNodes(n.left, n.right)
	}
	n.fix()
	return n
}

func mergeNodes(l, r *inode) *inode {
	if l == nil {
		return r
	}
	if r == nil {
		return l
	}
	if l.prio < r.prio {
		l.right = mergeNodes(l.right, r)
		l.fix()
		return l
	}
	r.left = mergeNodes(l, r.left)
	r.fix()
	return r
}

// intersecting 返回所有与 [a, b) 相交的区间（按键序）。
func (tr *itreap) intersecting(a, b int64) []interval {
	var out []interval
	collectIntersecting(tr.root, a, b, &out)
	return out
}

func collectIntersecting(n *inode, a, b int64, out *[]interval) {
	// maxEnd <= a：子树内没有 end > a 的区间，全部不相交。
	if n == nil || n.maxEnd <= a {
		return
	}
	collectIntersecting(n.left, a, b, out)
	// 右子树所有键 >= 当前键；当前 start >= b 时右子树不可能相交。
	if n.iv.start >= b {
		return
	}
	if n.iv.end > a {
		*out = append(*out, n.iv)
	}
	collectIntersecting(n.right, a, b, out)
}
