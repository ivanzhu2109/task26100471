package admission

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
)

// naive 为测试专用的朴素参照实现：每批都克隆全量状态、
// 对每个操作员扫描全部区间端点。正确性优先，性能无关。
type naive struct {
	tasks map[string]Task
}

func newNaive() *naive { return &naive{tasks: make(map[string]Task)} }

// apply 返回 (是否拒绝, 候选状态)。候选状态供见证核查使用。
func (n *naive) apply(changes []Change) (bool, map[string]Task) {
	cand := make(map[string]Task, len(n.tasks))
	for id, tk := range n.tasks {
		cand[id] = tk
	}
	for _, ch := range changes {
		if ch.Kind == KindDelete {
			delete(cand, ch.Task.ID)
		} else {
			cand[ch.Task.ID] = ch.Task
		}
	}
	// 按操作员聚合占用区间，扫描线求峰值。
	byOp := make(map[string][]interval)
	for _, tk := range cand {
		if tk.Occupies() {
			op := tk.Operator
			byOp[op] = append(byOp[op], interval{start: tk.Start, end: tk.End, id: tk.ID})
		}
	}
	for _, ivs := range byOp {
		events := make(map[int64]int)
		for _, iv := range ivs {
			events[iv.start]++
			events[iv.end]--
		}
		times := make([]int64, 0, len(events))
		for tm := range events {
			times = append(times, tm)
		}
		sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
		count := 0
		for _, tm := range times {
			count += events[tm]
			if count > Capacity {
				return true, cand
			}
		}
	}
	n.tasks = cand
	return false, cand
}

// firstOverloadAt 返回候选状态下 operator 的首次超载时刻，未超载返回 false。
func firstOverloadAt(cand map[string]Task, op string) (int64, bool) {
	events := make(map[int64]int)
	for _, tk := range cand {
		if tk.Operator == op && tk.Occupies() {
			events[tk.Start]++
			events[tk.End]--
		}
	}
	times := make([]int64, 0, len(events))
	for tm := range events {
		times = append(times, tm)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	count := 0
	for _, tm := range times {
		count += events[tm]
		if count > Capacity {
			return tm, true
		}
	}
	return 0, false
}

// checkWitness 依据候选状态独立核查见证。
func checkWitness(t *testing.T, cand map[string]Task, rej *Rejection) {
	t.Helper()
	if rej == nil {
		t.Fatal("expected rejection, got nil")
	}
	if len(rej.TaskIDs) != Capacity+1 {
		t.Fatalf("witness has %d ids, want %d", len(rej.TaskIDs), Capacity+1)
	}
	seen := make(map[string]bool)
	for _, id := range rej.TaskIDs {
		if seen[id] {
			t.Fatalf("duplicate id %q in witness", id)
		}
		seen[id] = true
		tk, ok := cand[id]
		if !ok {
			t.Fatalf("witness id %q not in candidate state", id)
		}
		if tk.Operator != rej.Operator {
			t.Fatalf("witness id %q belongs to %q, not %q", id, tk.Operator, rej.Operator)
		}
		if !tk.Occupies() {
			t.Fatalf("witness id %q does not occupy capacity", id)
		}
		if !(tk.Start <= rej.Time && rej.Time < tk.End) {
			t.Fatalf("witness id %q [%d,%d) does not cover time %d", id, tk.Start, tk.End, rej.Time)
		}
	}
	// 见证时刻必须是该操作员候选日程中的首次超载时刻。
	first, overloaded := firstOverloadAt(cand, rej.Operator)
	if !overloaded {
		t.Fatalf("operator %q is not overloaded in candidate state", rej.Operator)
	}
	if first != rej.Time {
		t.Fatalf("first overload at %d, witness reports %d", first, rej.Time)
	}
}

// TestRandomAgainstNaive 用固定种子的随机小批操作序列对比朴素参照实现。
func TestRandomAgainstNaive(t *testing.T) {
	prios := []Priority{PriorityCritical, PriorityHigh, PriorityMedium, PriorityLow}
	for _, seed := range []int64{1, 7, 42} {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			s := NewStore()
			n := newNaive()
			operators := []string{"alice", "bob", "carol", "dave"}
			var liveIDs []string
			nextID := 0

			for step := 0; step < 3000; step++ {
				batchSize := 1 + rng.Intn(8)
				var changes []Change
				used := make(map[string]bool) // 一批内任务 id 互不相同
				for i := 0; i < batchSize; i++ {
					op := operators[rng.Intn(len(operators))]
					start := int64(rng.Intn(40))
					tk := Task{
						Operator: op,
						Start:    start,
						End:      start + 1 + int64(rng.Intn(10)),
						Priority: prios[rng.Intn(len(prios))],
						Ended:    rng.Intn(5) == 0,
					}
					pickLive := func() string {
						for tries := 0; tries < 10; tries++ {
							id := liveIDs[rng.Intn(len(liveIDs))]
							if !used[id] {
								return id
							}
						}
						return ""
					}
					switch {
					case len(liveIDs) == 0 || rng.Intn(2) == 0:
						tk.ID = fmt.Sprintf("t%d", nextID)
						nextID++
						changes = append(changes, Change{Kind: KindAdd, Task: tk})
					case rng.Intn(3) == 0:
						if id := pickLive(); id != "" {
							used[id] = true
							changes = append(changes, Change{Kind: KindDelete, Task: Task{ID: id}})
						}
					default:
						if id := pickLive(); id != "" {
							used[id] = true
							tk.ID = id
							changes = append(changes, Change{Kind: KindSet, Task: tk})
						}
					}
				}

				rej := s.Apply(changes)
				naiveRejected, cand := n.apply(changes)
				if (rej != nil) != naiveRejected {
					t.Fatalf("step %d: store rejected=%v, naive rejected=%v, changes=%+v",
						step, rej != nil, naiveRejected, changes)
				}
				if rej != nil {
					checkWitness(t, cand, rej)
				}

				// 状态一致性：抽样 Get 与全量 Active 对比。
				liveIDs = liveIDs[:0]
				for id, want := range n.tasks {
					liveIDs = append(liveIDs, id)
					got, ok := s.Get(id)
					if !ok || got != want {
						t.Fatalf("step %d: Get(%s) = %+v,%v, want %+v", step, id, got, ok, want)
					}
				}
				sort.Strings(liveIDs)
				for _, op := range operators {
					for tm := int64(0); tm < 50; tm += 3 {
						got := s.Active(op, tm)
						var want []string
						for id, tk := range n.tasks {
							if tk.Operator == op && tk.Occupies() && tk.Start <= tm && tm < tk.End {
								want = append(want, id)
							}
						}
						sort.Strings(want)
						if !reflect.DeepEqual(got, want) {
							t.Fatalf("step %d: Active(%s,%d) = %v, want %v", step, op, tm, got, want)
						}
					}
				}
			}
		})
	}
}
