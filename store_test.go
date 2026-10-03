package admission

import (
	"reflect"
	"testing"
)

func add(s *Store, tasks ...Task) *Rejection {
	ch := make([]Change, len(tasks))
	for i, tk := range tasks {
		ch[i] = Change{Kind: KindAdd, Task: tk}
	}
	return s.Apply(ch)
}

func occ(id, op string, start, end int64) Task {
	return Task{ID: id, Operator: op, Start: start, End: end, Priority: PriorityHigh}
}

// 一条长任务与多条互不相交的短任务相交，但任一时刻最多 2 个并发，必须接受。
// 两两相交或“与新增任务起点相交数”之类的近似判断会错误拒绝。
func TestLongTaskWithDisjointShorts(t *testing.T) {
	s := NewStore()
	if rej := add(s, occ("long", "op", 0, 100)); rej != nil {
		t.Fatalf("unexpected rejection: %+v", rej)
	}
	var shorts []Task
	for i := int64(0); i < 10; i++ {
		shorts = append(shorts, occ(shortID(i), "op", i*10, i*10+10))
	}
	if rej := add(s, shorts...); rej != nil {
		t.Fatalf("disjoint shorts must be accepted, got %+v", rej)
	}
	for i := int64(0); i < 10; i++ {
		got := s.Active("op", i*10+5)
		want := []string{"long", shortID(i)} // Active 返回升序
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("active at %d = %v, want %v", i*10+5, got, want)
		}
	}
}

func shortID(i int64) string {
	return "short-" + string(rune('a'+i))
}

// 相邻区间端点不重复计数：5 个任务在 t=10 结束、5 个在 t=10 开始，
// t=10 时刻只有 5 个占用容量，必须接受；再叠加第 6 个 [10,20) 则在 t=10 超载。
func TestAdjacentEndpointsNotDoubleCounted(t *testing.T) {
	s := NewStore()
	var batch []Task
	for i := 0; i < 5; i++ {
		batch = append(batch, occ(occID("a", i), "op", 0, 10))
		batch = append(batch, occ(occID("b", i), "op", 10, 20))
	}
	if rej := add(s, batch...); rej != nil {
		t.Fatalf("adjacent intervals must not double count, got %+v", rej)
	}
	wantAt10 := []string{"b-0", "b-1", "b-2", "b-3", "b-4"}
	if got := s.Active("op", 10); !reflect.DeepEqual(got, wantAt10) {
		t.Fatalf("active at 10 = %v, want %v", got, wantAt10)
	}
	rej := add(s, occ("extra", "op", 10, 20))
	if rej == nil {
		t.Fatal("expected rejection")
	}
	if rej.Time != 10 {
		t.Fatalf("rejection time = %d, want 10", rej.Time)
	}
	if !reflect.DeepEqual(rej.TaskIDs, append(wantAt10, "extra")) {
		t.Fatalf("witness = %v", rej.TaskIDs)
	}
}

func occID(prefix string, i int) string {
	return prefix + "-" + string(rune('0'+i))
}

// 超载发生在已有任务的开始时刻，而非新任务的开始时刻：
// 已有 5 个 [10,20)，新增 [0,15)，超载区间为 [10,15)，首次超载时刻为 10。
func TestOverloadAtExistingTaskStart(t *testing.T) {
	s := NewStore()
	var existing []Task
	for i := 0; i < 5; i++ {
		existing = append(existing, occ(occID("e", i), "op", 10, 20))
	}
	if rej := add(s, existing...); rej != nil {
		t.Fatalf("setup failed: %+v", rej)
	}
	rej := add(s, occ("new", "op", 0, 15))
	if rej == nil {
		t.Fatal("expected rejection")
	}
	if rej.Operator != "op" {
		t.Fatalf("operator = %q, want op", rej.Operator)
	}
	if rej.Time != 10 {
		t.Fatalf("first overload time = %d, want 10 (existing tasks' start)", rej.Time)
	}
	want := []string{"e-0", "e-1", "e-2", "e-3", "e-4", "new"}
	if !reflect.DeepEqual(rej.TaskIDs, want) {
		t.Fatalf("witness = %v, want %v", rej.TaskIDs, want)
	}
	// 拒绝不得污染状态。
	if _, ok := s.Get("new"); ok {
		t.Fatal("rejected task must not be committed")
	}
	if got := s.Active("op", 12); !reflect.DeepEqual(got, want[:5]) {
		t.Fatalf("active at 12 = %v, want %v", got, want[:5])
	}
}

// 两个满容量操作员在一批内交换任务：逐项处理会超载，但最终状态合法，必须接受。
func TestSwapBetweenFullOperators(t *testing.T) {
	s := NewStore()
	var batch []Task
	for i := 0; i < 5; i++ {
		batch = append(batch, occ(occID("a", i), "opA", 0, 10))
		batch = append(batch, occ(occID("b", i), "opB", 0, 10))
	}
	if rej := add(s, batch...); rej != nil {
		t.Fatalf("setup failed: %+v", rej)
	}
	// 交换 a-0 与 b-0 的操作员。
	a0, _ := s.Get("a-0")
	b0, _ := s.Get("b-0")
	a0.Operator, b0.Operator = "opB", "opA"
	rej := s.Apply([]Change{
		{Kind: KindSet, Task: a0},
		{Kind: KindSet, Task: b0},
	})
	if rej != nil {
		t.Fatalf("swap between full operators must be accepted, got %+v", rej)
	}
	if got := s.Active("opA", 5); len(got) != 5 || got[0] != "a-1" || got[4] != "b-0" {
		t.Fatalf("opA active = %v", got)
	}
	if got := s.Active("opB", 5); len(got) != 5 || got[0] != "a-0" || got[1] != "b-1" {
		t.Fatalf("opB active = %v", got)
	}
}

// 混合删除、改派和提升优先级的批次失败：候选状态超载，
// 且拒绝后所有原任务与查询结果完全保留。
func TestMixedBatchFailurePreservesState(t *testing.T) {
	s := NewStore()
	var batch []Task
	for i := 0; i < 5; i++ {
		batch = append(batch, occ(occID("x", i), "opX", 0, 10))
	}
	batch = append(batch,
		Task{ID: "m-0", Operator: "opX", Start: 0, End: 10, Priority: PriorityMedium},
		Task{ID: "m-1", Operator: "opX", Start: 0, End: 10, Priority: PriorityLow},
		occ("y-0", "opY", 0, 10),
	)
	if rej := add(s, batch...); rej != nil {
		t.Fatalf("setup failed: %+v", rej)
	}
	snapshot := make(map[string]Task)
	for _, id := range []string{"x-0", "x-1", "x-2", "x-3", "x-4", "m-0", "m-1", "y-0"} {
		tk, ok := s.Get(id)
		if !ok {
			t.Fatalf("setup: %s missing", id)
		}
		snapshot[id] = tk
	}
	activeX := s.Active("opX", 5)
	activeY := s.Active("opY", 5)

	// 删除 x-0（腾出 1 个）、把 x-1 改派给 opY、把 m-0/m-1 提升为 Critical：
	// 候选状态下 opX 有 x-2..x-4 + m-0 + m-1 + 新增 n-0 = 6 个，超载。
	m0, m1 := snapshot["m-0"], snapshot["m-1"]
	m0.Priority, m1.Priority = PriorityCritical, PriorityCritical
	x1 := snapshot["x-1"]
	x1.Operator = "opY"
	rej := s.Apply([]Change{
		{Kind: KindDelete, Task: Task{ID: "x-0"}},
		{Kind: KindSet, Task: x1},
		{Kind: KindSet, Task: m0},
		{Kind: KindSet, Task: m1},
		{Kind: KindAdd, Task: occ("n-0", "opX", 0, 10)},
	})
	if rej == nil {
		t.Fatal("expected rejection")
	}
	if rej.Operator != "opX" || rej.Time != 0 {
		t.Fatalf("rejection = %+v, want operator opX time 0", rej)
	}
	wantWitness := []string{"m-0", "m-1", "n-0", "x-2", "x-3", "x-4"}
	if !reflect.DeepEqual(rej.TaskIDs, wantWitness) {
		t.Fatalf("witness = %v, want %v", rej.TaskIDs, wantWitness)
	}

	// 所有原任务完全保留。
	for id, want := range snapshot {
		got, ok := s.Get(id)
		if !ok || got != want {
			t.Fatalf("Get(%s) = %+v,%v, want %+v", id, got, ok, want)
		}
	}
	if _, ok := s.Get("n-0"); ok {
		t.Fatal("rejected add must not be committed")
	}
	if got := s.Active("opX", 5); !reflect.DeepEqual(got, activeX) {
		t.Fatalf("opX active changed: %v -> %v", activeX, got)
	}
	if got := s.Active("opY", 5); !reflect.DeepEqual(got, activeY) {
		t.Fatalf("opY active changed: %v -> %v", activeY, got)
	}
}

// 已结束与普通优先级任务必须保存，且可重新变为占用容量的任务。
func TestNonOccupyingTasksPersistAndReactivate(t *testing.T) {
	s := NewStore()
	ended := Task{ID: "e", Operator: "op", Start: 0, End: 10, Priority: PriorityCritical, Ended: true}
	low := Task{ID: "l", Operator: "op", Start: 0, End: 10, Priority: PriorityLow}
	if rej := add(s, ended, low); rej != nil {
		t.Fatalf("unexpected rejection: %+v", rej)
	}
	if got := s.Active("op", 5); len(got) != 0 {
		t.Fatalf("active = %v, want empty", got)
	}
	e, _ := s.Get("e")
	e.Ended = false
	l, _ := s.Get("l")
	l.Priority = PriorityHigh
	if rej := s.Apply([]Change{{Kind: KindSet, Task: e}, {Kind: KindSet, Task: l}}); rej != nil {
		t.Fatalf("reactivation failed: %+v", rej)
	}
	if got := s.Active("op", 5); !reflect.DeepEqual(got, []string{"e", "l"}) {
		t.Fatalf("active = %v, want [e l]", got)
	}
}
