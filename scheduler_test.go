package taskalloc

import (
	"sort"
	"testing"
)

func upsert(id, op string, start, end int64, p Priority) Change {
	return Change{ID: id, Upsert: &Task{ID: id, Operator: op, Start: start, End: end, Priority: p}}
}

func del(id string) Change { return Change{ID: id} }

func mustApply(t *testing.T, s *Scheduler, changes ...Change) {
	t.Helper()
	if r := s.Apply(changes); r != nil {
		t.Fatalf("Apply 被意外拒绝: %+v", r)
	}
}

func mustReject(t *testing.T, s *Scheduler, changes ...Change) *Rejection {
	t.Helper()
	r := s.Apply(changes)
	if r == nil {
		t.Fatal("Apply 被意外接受")
	}
	if len(r.TaskIDs) != 6 {
		t.Fatalf("见证应为 6 个任务 id, 实际 %d 个: %v", len(r.TaskIDs), r.TaskIDs)
	}
	seen := make(map[string]bool)
	for _, id := range r.TaskIDs {
		if seen[id] {
			t.Fatalf("见证含重复 id: %v", r.TaskIDs)
		}
		seen[id] = true
	}
	return r
}

func sortedEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

// 一条长任务与多条互不相交的短任务相交，峰值只有 2，不得误判超载。
func TestLongTaskWithDisjointShorts(t *testing.T) {
	s := NewScheduler()
	mustApply(t, s, upsert("long", "op", 0, 1000, Critical))
	// 短任务彼此相邻但互不相交，且都与长任务相交。
	var batch []Change
	for i := 0; i < 10; i++ {
		batch = append(batch, upsert(shortID(i), "op", int64(i*100), int64(i*100+100), High))
	}
	mustApply(t, s, batch...)
	for i := 0; i < 10; i++ {
		got := s.Active("op", int64(i*100+50))
		want := []string{"long", shortID(i)}
		if !sortedEqual(got, want) {
			t.Fatalf("t=%d Active=%v, want %v", i*100+50, got, want)
		}
	}
}

func shortID(i int) string {
	return string(rune('a'+i)) + "-short"
}

// 相邻区间端点不重复计数：左闭右开，[0,10) 与 [10,20) 不重叠。
func TestAdjacentEndpointsNotDoubleCounted(t *testing.T) {
	s := NewScheduler()
	// 5 段首尾相接的任务，任何时刻占用都是 1。
	var batch []Change
	for i := 0; i < 5; i++ {
		batch = append(batch, upsert(shortID(i), "op", int64(i*10), int64(i*10+10), Critical))
	}
	mustApply(t, s, batch...)
	// 再来 4 组同样的 5 段链，同一时刻占用为 5，仍合法。
	for g := 1; g < 5; g++ {
		batch = batch[:0]
		for i := 0; i < 5; i++ {
			batch = append(batch, upsert(shortID(g*5+i), "op", int64(i*10), int64(i*10+10), High))
		}
		mustApply(t, s, batch...)
	}
	// 第 6 组链中任意一段都会使端点处达到 6，必须拒绝。
	r := mustReject(t, s, upsert("extra", "op", 0, 10, Critical))
	if r.Operator != "op" {
		t.Fatalf("Operator=%q, want op", r.Operator)
	}
	if r.Time != 0 {
		t.Fatalf("Time=%d, want 0（左闭区间在 0 处已超载）", r.Time)
	}
	// 恰好在端点 10 处：前一段已结束，后一段未开始之外的任务计数。
	got := len(s.Active("op", 10))
	if got != 5 {
		t.Fatalf("t=10 处占用=%d, want 5（端点不重复计数）", got)
	}
}

// 超载发生在已有任务的开始时刻，而非新任务的开始时刻。
func TestOverloadAtExistingTaskStart(t *testing.T) {
	s := NewScheduler()
	mustApply(t, s,
		upsert("t1", "op", 0, 50, Critical),
		upsert("t2", "op", 0, 50, Critical),
		upsert("t3", "op", 0, 50, Critical),
		upsert("t4", "op", 0, 50, Critical),
		upsert("t5", "op", 20, 60, High),
	)
	// 新任务从 10 开始，但第 6 个重叠直到已有任务 t5 的起点 20 才出现。
	r := mustReject(t, s, upsert("new", "op", 10, 30, High))
	if r.Time != 20 {
		t.Fatalf("Time=%d, want 20（已有任务 t5 的开始时刻）", r.Time)
	}
	want := []string{"new", "t1", "t2", "t3", "t4", "t5"}
	if !sortedEqual(r.TaskIDs, want) {
		t.Fatalf("TaskIDs=%v, want %v", r.TaskIDs, want)
	}
	// 拒绝不得污染状态。
	if _, ok := s.Get("new"); ok {
		t.Fatal("拒绝后 new 不应存在")
	}
	if got := len(s.Active("op", 25)); got != 5 {
		t.Fatalf("拒绝后 t=25 占用=%d, want 5", got)
	}
}

// 两个满容量操作员在一批内交换任务：逐项处理会超载，整批最终合法。
func TestFullOperatorsSwapInOneBatch(t *testing.T) {
	s := NewScheduler()
	for i := 0; i < 5; i++ {
		mustApply(t, s, upsert("a"+string(rune('0'+i)), "opA", 0, 10, Critical))
		mustApply(t, s, upsert("b"+string(rune('0'+i)), "opB", 0, 10, Critical))
	}
	// a0 改派给 opB、b0 改派给 opA：任何中间态都有一个操作员达到 6。
	mustApply(t, s,
		upsert("a0", "opB", 0, 10, Critical),
		upsert("b0", "opA", 0, 10, Critical),
	)
	if got := len(s.Active("opA", 5)); got != 5 {
		t.Fatalf("opA 占用=%d, want 5", got)
	}
	if got := len(s.Active("opB", 5)); got != 5 {
		t.Fatalf("opB 占用=%d, want 5", got)
	}
	if task, _ := s.Get("a0"); task.Operator != "opB" {
		t.Fatalf("a0.Operator=%q, want opB", task.Operator)
	}
	if task, _ := s.Get("b0"); task.Operator != "opA" {
		t.Fatalf("b0.Operator=%q, want opA", task.Operator)
	}
}

// 混合删除、改派和提升优先级的批次失败后，所有原任务与查询结果完全保留。
func TestFailedMixedBatchLeavesStateUntouched(t *testing.T) {
	s := NewScheduler()
	// opA 满容量 5 个；opB 有 2 个，其中一个 Low 不占容量，一个已结束。
	mustApply(t, s,
		upsert("a0", "opA", 0, 100, Critical),
		upsert("a1", "opA", 0, 100, High),
		upsert("a2", "opA", 0, 100, Critical),
		upsert("a3", "opA", 0, 100, High),
		upsert("a4", "opA", 0, 100, Critical),
		upsert("b0", "opB", 0, 100, Low),
		upsert("b1", "opB", 0, 100, Critical),
	)
	mustApply(t, s, Change{ID: "b2", Upsert: &Task{
		ID: "b2", Operator: "opB", Start: 0, End: 100, Priority: Critical, Ended: true,
	}})
	snapshot := make(map[string]Task)
	for _, id := range []string{"a0", "a1", "a2", "a3", "a4", "b0", "b1", "b2"} {
		task, ok := s.Get(id)
		if !ok {
			t.Fatalf("前置任务 %s 不存在", id)
		}
		snapshot[id] = task
	}
	activeABefore := s.Active("opA", 50)
	activeBBefore := s.Active("opB", 50)

	// 失败批次：删除 a0、把 b1 改派去 opC、把 b0 提升为 Critical、
	// 把已结束的 b2 改到 opA 并重新激活、新增 c0 到 opA ——
	// 候选状态下 opA 为 a1..a4 + b2 + c0 共 6 个，超载。
	r := mustReject(t, s,
		del("a0"),
		upsert("b1", "opC", 0, 100, Critical),
		upsert("b0", "opB", 0, 100, Critical),
		upsert("b2", "opA", 0, 100, High),
		upsert("c0", "opA", 0, 100, Critical),
	)
	if r.Operator != "opA" {
		t.Fatalf("Operator=%q, want opA", r.Operator)
	}
	// 见证不得引用已删除的 a0、已改派走的 b1、留在 opB 的 b0。
	for _, id := range r.TaskIDs {
		if id == "a0" || id == "b1" || id == "b0" {
			t.Fatalf("见证引用了候选状态下不属于 opA 的任务 %s", id)
		}
	}
	// 状态完全保留。
	for id, want := range snapshot {
		got, ok := s.Get(id)
		if !ok {
			t.Fatalf("拒绝后任务 %s 丢失", id)
		}
		if got != want {
			t.Fatalf("拒绝后任务 %s 被污染: got %+v, want %+v", id, got, want)
		}
	}
	if !sortedEqual(s.Active("opA", 50), activeABefore) {
		t.Fatal("拒绝后 opA 的 Active 结果改变")
	}
	if !sortedEqual(s.Active("opB", 50), activeBBefore) {
		t.Fatal("拒绝后 opB 的 Active 结果改变")
	}
	if got := s.Active("opC", 50); len(got) != 0 {
		t.Fatalf("拒绝后 opC 不应有占用, got %v", got)
	}
	if _, ok := s.Get("c0"); ok {
		t.Fatal("拒绝后新增任务 c0 不应存在")
	}
}

// 普通优先级与已结束任务必须保存，且可重新变为占用容量的任务。
func TestNonOccupyingTasksPersistAndReactivate(t *testing.T) {
	s := NewScheduler()
	mustApply(t, s,
		upsert("low", "op", 0, 10, Low),
		Change{ID: "done", Upsert: &Task{ID: "done", Operator: "op", Start: 0, End: 10, Priority: High, Ended: true}},
	)
	if got := s.Active("op", 5); len(got) != 0 {
		t.Fatalf("Low/已结束任务不应占用容量, got %v", got)
	}
	if _, ok := s.Get("low"); !ok {
		t.Fatal("Low 任务必须保存")
	}
	if _, ok := s.Get("done"); !ok {
		t.Fatal("已结束任务必须保存")
	}
	mustApply(t, s,
		upsert("low", "op", 0, 10, Critical),
		Change{ID: "done", Upsert: &Task{ID: "done", Operator: "op", Start: 0, End: 10, Priority: High}},
	)
	if got := s.Active("op", 5); !sortedEqual(got, []string{"done", "low"}) {
		t.Fatalf("重新激活后 Active=%v", got)
	}
}
