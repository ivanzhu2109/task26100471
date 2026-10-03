package admission

import (
	"fmt"
	"testing"
	"time"
)

// TestLargeSchedule 规模用例：20 万存量任务（其中 10 万属于同一操作员），
// 在大日程的局部反复执行 1 万次小批成功修改、失败修改与查询，记录耗时。
func TestLargeSchedule(t *testing.T) {
	s := NewStore()

	setupStart := time.Now()
	const heavyTasks = 100_000
	const otherOps = 100
	const perOp = 1_000
	var batch []Change
	flush := func() {
		if len(batch) > 0 {
			if rej := s.Apply(batch); rej != nil {
				t.Fatalf("setup rejected: %+v", rej)
			}
			batch = batch[:0]
		}
	}
	// heavy：10 万个互不重叠的占用区间 [i*10, i*10+5)。
	for i := 0; i < heavyTasks; i++ {
		batch = append(batch, Change{Kind: KindAdd, Task: Task{
			ID: fmt.Sprintf("heavy-%d", i), Operator: "heavy",
			Start: int64(i * 10), End: int64(i*10 + 5), Priority: PriorityHigh,
		}})
		if len(batch) == 50 {
			flush()
		}
	}
	// 其余 10 万个任务分散在 100 个操作员上。
	for o := 0; o < otherOps; o++ {
		for i := 0; i < perOp; i++ {
			batch = append(batch, Change{Kind: KindAdd, Task: Task{
				ID: fmt.Sprintf("op%d-%d", o, i), Operator: fmt.Sprintf("op%d", o),
				Start: int64(i * 10), End: int64(i*10 + 5), Priority: PriorityMedium,
			}})
			if len(batch) == 50 {
				flush()
			}
		}
	}
	flush()
	t.Logf("setup: 200k tasks committed in %v", time.Since(setupStart))

	loopStart := time.Now()
	var added []string // 成功新增、可循环删除的临时任务
	const iterations = 10_000
	var okCount, failCount, queryCount int
	for i := 0; i < iterations; i++ {
		a := int64((i % 1000) * 10) // 局部锚点，在大日程前段往复
		switch i % 4 {
		case 0: // 成功小批：新增 2 个短任务 + 改期 1 个存量任务
			id1, id2 := fmt.Sprintf("tmp-a-%d", i), fmt.Sprintf("tmp-b-%d", i)
			shifted, _ := s.Get(fmt.Sprintf("heavy-%d", i%1000))
			shifted.Start++
			shifted.End++
			rej := s.Apply([]Change{
				{Kind: KindAdd, Task: Task{ID: id1, Operator: "heavy", Start: a + 5, End: a + 9, Priority: PriorityCritical}},
				{Kind: KindAdd, Task: Task{ID: id2, Operator: "heavy", Start: a + 5, End: a + 9, Priority: PriorityHigh}},
				{Kind: KindSet, Task: shifted},
			})
			if rej != nil {
				t.Fatalf("iter %d: unexpected rejection %+v", i, rej)
			}
			added = append(added, id1, id2)
			okCount++
		case 1: // 失败小批：6 个 Critical 同时叠加在局部热点上
			var changes []Change
			for k := 0; k < 6; k++ {
				changes = append(changes, Change{Kind: KindAdd, Task: Task{
					ID: fmt.Sprintf("boom-%d-%d", i, k), Operator: "heavy",
					Start: a, End: a + 3, Priority: PriorityCritical,
				}})
			}
			rej := s.Apply(changes)
			if rej == nil {
				t.Fatalf("iter %d: expected rejection", i)
			}
			if rej.Operator != "heavy" || len(rej.TaskIDs) != 6 {
				t.Fatalf("iter %d: bad witness %+v", i, rej)
			}
			if _, ok := s.Get("boom-" + fmt.Sprint(i) + "-0"); ok {
				t.Fatalf("iter %d: rejected batch polluted state", i)
			}
			failCount++
		case 2: // 成功小批：删除 2 个早前新增的临时任务 + 降低 1 个优先级
			if len(added) >= 6 {
				victim1, victim2 := added[0], added[1]
				added = added[2:]
				demote, _ := s.Get(added[0])
				demote.Priority = PriorityLow
				rej := s.Apply([]Change{
					{Kind: KindDelete, Task: Task{ID: victim1}},
					{Kind: KindDelete, Task: Task{ID: victim2}},
					{Kind: KindSet, Task: demote},
				})
				if rej != nil {
					t.Fatalf("iter %d: unexpected rejection %+v", i, rej)
				}
				okCount++
			}
		case 3: // 查询：热点操作员与分散操作员
			if got := s.Active("heavy", a+1); len(got) > Capacity {
				t.Fatalf("iter %d: invariant broken, active=%v", i, got)
			}
			s.Active(fmt.Sprintf("op%d", i%otherOps), int64((i%perOp)*10))
			s.Get(fmt.Sprintf("heavy-%d", (i*7)%heavyTasks))
			queryCount++
		}
	}
	elapsed := time.Since(loopStart)
	t.Logf("loop: %d iterations (%d ok batches, %d rejected batches, %d query rounds) in %v (avg %v/iter)",
		iterations, okCount, failCount, queryCount, elapsed, elapsed/iterations)
}
