package admission

import "sort"

// Store 为进程内任务准入库。调用方保证串行访问。
//
// 不变式：已提交状态下，任何操作员在任意时刻占用容量的任务数 <= Capacity。
// ops 中仅索引占用容量（未结束且 Critical/High）的任务区间；
// 普通优先级与已结束任务只保存在 tasks 中。
type Store struct {
	tasks map[string]Task
	ops   map[string]*itreap
}

// NewStore 返回一个空的准入库。
func NewStore() *Store {
	return &Store{tasks: make(map[string]Task), ops: make(map[string]*itreap)}
}

// opDelta 为单个操作员在一批中的候选变更。
type opDelta struct {
	added      []interval      // 候选状态下新增占用容量的区间
	removedIDs map[string]bool // 候选状态下不再占用容量的任务 id
}

// Apply 原子地应用一批修改：全部生效或全部不生效。
// 失败时返回超载见证（来自批修改后的候选状态），成功时返回 nil。
// 准入判断只依赖整批修改后的候选状态，与批内排列顺序无关。
func (s *Store) Apply(changes []Change) *Rejection {
	deltas := make(map[string]*opDelta)
	for _, ch := range changes {
		s.collectChange(ch, deltas)
	}

	// 按操作员名排序，保证多个操作员冲突时结果可复现。
	ops := make([]string, 0, len(deltas))
	for op := range deltas {
		ops = append(ops, op)
	}
	sort.Strings(ops)
	for _, op := range ops {
		d := deltas[op]
		if len(d.added) == 0 {
			continue // 纯减少占用不可能造成超载
		}
		if rej := s.checkOperator(op, d); rej != nil {
			return rej
		}
	}

	for _, ch := range changes {
		s.commit(ch)
	}
	return nil
}

// collectChange 把单条修改折算为相关操作员的区间增删。
// 只有候选状态相对已提交状态真正改变的占用区间才计入。
func (s *Store) collectChange(ch Change, deltas map[string]*opDelta) {
	old, existed := s.tasks[ch.Task.ID]
	var oldIV, newIV interval
	oldOcc, newOcc := false, false
	if existed && old.Occupies() {
		oldIV = interval{start: old.Start, end: old.End, id: old.ID}
		oldOcc = true
	}
	if ch.Kind != KindDelete && ch.Task.Occupies() {
		newIV = interval{start: ch.Task.Start, end: ch.Task.End, id: ch.Task.ID}
		newOcc = true
	}
	if oldOcc && newOcc && old.Operator == ch.Task.Operator && oldIV == newIV {
		return // 占用区间未变
	}
	if oldOcc {
		d := deltas[old.Operator]
		if d == nil {
			d = &opDelta{removedIDs: make(map[string]bool)}
			deltas[old.Operator] = d
		}
		d.removedIDs[old.ID] = true
	}
	if newOcc {
		d := deltas[ch.Task.Operator]
		if d == nil {
			d = &opDelta{removedIDs: make(map[string]bool)}
			deltas[ch.Task.Operator] = d
		}
		d.added = append(d.added, newIV)
	}
}

// checkOperator 在候选状态下检查单个操作员是否超载。
// 超载只能出现在新增占用区间的并集内（其余位置占用数不增），
// 因此只在这些局部区间内做精确扫描。
func (s *Store) checkOperator(op string, d *opDelta) *Rejection {
	for _, seg := range mergeIntervals(d.added) {
		if rej := s.checkSegment(op, d, seg.start, seg.end); rej != nil {
			return rej
		}
	}
	return nil
}

// mergeIntervals 合并相交或相接的区间，按起点升序返回连通分量。
func mergeIntervals(ivs []interval) []interval {
	sorted := make([]interval, len(ivs))
	copy(sorted, ivs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].start < sorted[j].start })
	var out []interval
	for _, iv := range sorted {
		if n := len(out); n > 0 && iv.start <= out[n-1].end {
			if iv.end > out[n-1].end {
				out[n-1].end = iv.end
			}
		} else {
			out = append(out, iv)
		}
	}
	return out
}

// checkSegment 在候选状态的 [a, b) 内做扫描线检查，
// 返回该段内首次超载的见证，未超载返回 nil。
func (s *Store) checkSegment(op string, d *opDelta, a, b int64) *Rejection {
	// delta[t] = 候选占用数在时刻 t 的净变化（开始 +1，结束 -1）。
	delta := make(map[int64]int)
	add := func(iv interval) {
		start, end := iv.start, iv.end
		if start < a {
			start = a
		}
		if end > b {
			end = b
		}
		if start < end {
			delta[start]++
			delta[end]--
		}
	}
	for _, iv := range d.added {
		add(iv)
	}
	if tr := s.ops[op]; tr != nil {
		for _, iv := range tr.intersecting(a, b) {
			if !d.removedIDs[iv.id] {
				add(iv)
			}
		}
	}

	times := make([]int64, 0, len(delta))
	for t := range delta {
		times = append(times, t)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })

	count := 0
	for _, t := range times {
		count += delta[t]
		if count > Capacity {
			return &Rejection{Operator: op, Time: t, TaskIDs: s.witnessIDs(op, d, t)}
		}
	}
	return nil
}

// witnessIDs 收集候选状态下时刻 t 覆盖 operator 的 6 个不同任务 id（升序）。
func (s *Store) witnessIDs(op string, d *opDelta, t int64) []string {
	seen := make(map[string]bool)
	var ids []string
	push := func(id string) bool {
		if seen[id] {
			return false
		}
		seen[id] = true
		ids = append(ids, id)
		return len(ids) == Capacity+1
	}
	if tr := s.ops[op]; tr != nil {
		for _, iv := range tr.intersecting(t, t+1) {
			if d.removedIDs[iv.id] {
				continue
			}
			if push(iv.id) {
				break
			}
		}
	}
	if len(ids) < Capacity+1 {
		for _, iv := range d.added {
			if iv.covers(t) && push(iv.id) {
				break
			}
		}
	}
	sort.Strings(ids)
	return ids
}

// commit 应用单条修改到已提交状态。仅在整批通过准入检查后调用。
func (s *Store) commit(ch Change) {
	if old, ok := s.tasks[ch.Task.ID]; ok {
		if old.Occupies() {
			tr := s.ops[old.Operator]
			tr.remove(interval{start: old.Start, end: old.End, id: old.ID})
			if tr.size == 0 {
				delete(s.ops, old.Operator)
			}
		}
		delete(s.tasks, ch.Task.ID)
	}
	if ch.Kind == KindDelete {
		return
	}
	s.tasks[ch.Task.ID] = ch.Task
	if ch.Task.Occupies() {
		tr := s.ops[ch.Task.Operator]
		if tr == nil {
			tr = &itreap{seed: 1}
			s.ops[ch.Task.Operator] = tr
		}
		tr.insert(interval{start: ch.Task.Start, end: ch.Task.End, id: ch.Task.ID})
	}
}

// Get 读取已提交任务。
func (s *Store) Get(id string) (Task, bool) {
	t, ok := s.tasks[id]
	return t, ok
}

// Active 返回 operator 在时刻 t 实际占用容量的任务 id（升序）。
func (s *Store) Active(operator string, t int64) []string {
	tr := s.ops[operator]
	if tr == nil {
		return nil
	}
	ivs := tr.intersecting(t, t+1)
	var ids []string
	for _, iv := range ivs {
		ids = append(ids, iv.id)
	}
	sort.Strings(ids)
	return ids
}
