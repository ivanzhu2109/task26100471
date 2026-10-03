package taskalloc

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"
)

// refScheduler 是独立的朴素参照实现：每次全量扫描，允许低效。
type refScheduler struct {
	tasks map[string]Task
}

func newRefScheduler() *refScheduler { return &refScheduler{tasks: make(map[string]Task)} }

// apply 返回 (是否拒绝, 超载操作员集合)。候选状态用全量拷贝构造。
func (r *refScheduler) apply(changes []Change) (bool, map[string]bool) {
	cand := make(map[string]Task, len(r.tasks))
	for k, v := range r.tasks {
		cand[k] = v
	}
	for _, ch := range changes {
		if ch.Upsert == nil {
			delete(cand, ch.ID)
		} else {
			nt := *ch.Upsert
			nt.ID = ch.ID
			cand[ch.ID] = nt
		}
	}
	overloaded := overloadOperators(cand)
	if len(overloaded) > 0 {
		return true, overloaded
	}
	r.tasks = cand
	return false, nil
}

// overloadOperators 全量扫描候选状态，返回有超载的操作员集合。
func overloadOperators(tasks map[string]Task) map[string]bool {
	type ev struct {
		t     int64
		delta int
	}
	byOp := make(map[string][]ev)
	for _, task := range tasks {
		if task.occupies() {
			byOp[task.Operator] = append(byOp[task.Operator], ev{task.Start, 1}, ev{task.End, -1})
		}
	}
	out := make(map[string]bool)
	for op, evs := range byOp {
		sort.Slice(evs, func(i, j int) bool {
			if evs[i].t != evs[j].t {
				return evs[i].t < evs[j].t
			}
			return evs[i].delta < evs[j].delta
		})
		cur := 0
		for _, e := range evs {
			cur += e.delta
			if cur > maxCapacity {
				out[op] = true
				break
			}
		}
	}
	return out
}

// firstOverloadRef 朴素计算操作员候选日程中首次超载时刻及该时刻占用任务。
func firstOverloadRef(tasks map[string]Task, op string) (int64, []string) {
	var occ []Task
	for _, task := range tasks {
		if task.occupies() && task.Operator == op {
			occ = append(occ, task)
		}
	}
	times := make(map[int64]bool)
	for _, task := range occ {
		times[task.Start] = true
	}
	var ts []int64
	for t := range times {
		ts = append(ts, t)
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i] < ts[j] })
	for _, t := range ts {
		var ids []string
		for _, task := range occ {
			if task.Start <= t && t < task.End {
				ids = append(ids, task.ID)
			}
		}
		if len(ids) > maxCapacity {
			sort.Strings(ids)
			return t, ids
		}
	}
	return 0, nil
}

// activeRef 朴素计算操作员某时刻的占用任务。
func (r *refScheduler) activeRef(op string, t int64) []string {
	var ids []string
	for _, task := range r.tasks {
		if task.occupies() && task.Operator == op && task.Start <= t && t < task.End {
			ids = append(ids, task.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

// TestRandomizedAgainstReference 用固定种子的随机小批操作序列对照两个实现。
func TestRandomizedAgainstReference(t *testing.T) {
	rng := rand.New(rand.NewSource(20261003))
	lib := NewScheduler()
	ref := newRefScheduler()

	const idSpace = 40
	ops := []string{"op0", "op1", "op2", "op3"}
	prios := []Priority{Low, Medium, High, Critical}

	randTask := func(id string) *Task {
		start := int64(rng.Intn(40))
		return &Task{
			ID:       id,
			Operator: ops[rng.Intn(len(ops))],
			Start:    start,
			End:      start + 1 + int64(rng.Intn(12)),
			Priority: prios[rng.Intn(len(prios))],
			Ended:    rng.Intn(5) == 0,
		}
	}

	for step := 0; step < 4000; step++ {
		n := 1 + rng.Intn(5)
		var changes []Change
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("task-%d", rng.Intn(idSpace))
			if rng.Intn(4) == 0 {
				changes = append(changes, del(id))
			} else {
				changes = append(changes, Change{ID: id, Upsert: randTask(id)})
			}
		}

		libRej := lib.Apply(changes)
		refRejected, refOverloaded := ref.apply(changes)

		if (libRej != nil) != refRejected {
			t.Fatalf("step %d: 判定不一致 lib=%v ref=%v, changes=%v", step, libRej != nil, refRejected, changes)
		}
		if libRej != nil {
			// 见证的操作员确实超载。
			if !refOverloaded[libRej.Operator] {
				t.Fatalf("step %d: 见证操作员 %s 在参照中未超载", step, libRej.Operator)
			}
			// 用参照的候选状态核查见证：时刻是首次超载、6 个 id 当时都占用。
			cand := make(map[string]Task, len(ref.tasks))
			for k, v := range ref.tasks {
				cand[k] = v
			}
			for _, ch := range changes {
				if ch.Upsert == nil {
					delete(cand, ch.ID)
				} else {
					nt := *ch.Upsert
					nt.ID = ch.ID
					cand[ch.ID] = nt
				}
			}
			wantTime, wantIDs := firstOverloadRef(cand, libRej.Operator)
			if libRej.Time != wantTime {
				t.Fatalf("step %d: 见证时刻=%d, 参照首次超载=%d", step, libRej.Time, wantTime)
			}
			idSet := make(map[string]bool)
			for _, id := range wantIDs {
				idSet[id] = true
			}
			if len(libRej.TaskIDs) != 6 {
				t.Fatalf("step %d: 见证数量=%d", step, len(libRej.TaskIDs))
			}
			seen := make(map[string]bool)
			for _, id := range libRej.TaskIDs {
				if !idSet[id] {
					t.Fatalf("step %d: 见证 id %s 在 t=%d 并未占用", step, id, wantTime)
				}
				if seen[id] {
					t.Fatalf("step %d: 见证 id %s 重复", step, id)
				}
				seen[id] = true
			}
		}

		// 每个 step 后全量比对已提交状态与查询结果。
		if step%7 == 0 {
			for i := 0; i < idSpace; i++ {
				id := fmt.Sprintf("task-%d", i)
				lt, lok := lib.Get(id)
				rt, rok := ref.tasks[id]
				if lok != rok || (lok && lt != rt) {
					t.Fatalf("step %d: Get(%s) 不一致 lib=(%v,%v) ref=(%v,%v)", step, id, lt, lok, rt, rok)
				}
			}
			for _, op := range ops {
				for _, tm := range []int64{0, 7, 20, 39, 51} {
					got := lib.Active(op, tm)
					want := ref.activeRef(op, tm)
					if !sortedEqual(got, want) {
						t.Fatalf("step %d: Active(%s,%d)=%v, want %v", step, op, tm, got, want)
					}
				}
			}
		}
	}
}
