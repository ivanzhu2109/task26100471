package taskalloc

import (
	"fmt"
	"testing"
	"time"
)

// TestScale 面向 20 万存量任务（其中 10 万属于同一操作员）的规模用例：
// 在大日程的局部反复执行成功修改、失败修改和查询，并记录耗时。
func TestScale(t *testing.T) {
	s := NewScheduler()

	const totalTasks = 200_000
	const hotTasks = 100_000
	const coldOps = 2_000
	const batchSize = 50

	// 构建：hot 操作员 10 万条互不相邻重叠的任务，其余 10 万散布在
	// 2000 个操作员上。全部通过 50 条一批的 apply 写入。
	start := time.Now()
	var batch []Change
	flush := func() {
		if len(batch) > 0 {
			if r := s.Apply(batch); r != nil {
				t.Fatalf("构建阶段被意外拒绝: %+v", r)
			}
			batch = batch[:0]
		}
	}
	for i := 0; i < totalTasks; i++ {
		var task Task
		if i < hotTasks {
			task = Task{ID: fmt.Sprintf("hot-%d", i), Operator: "hot",
				Start: int64(i) * 10, End: int64(i)*10 + 5, Priority: Critical}
		} else {
			j := i - hotTasks
			task = Task{ID: fmt.Sprintf("cold-%d", j), Operator: fmt.Sprintf("op-%d", j%coldOps),
				Start: int64(j/coldOps) * 10, End: int64(j/coldOps)*10 + 5, Priority: High}
		}
		t := task
		batch = append(batch, Change{ID: task.ID, Upsert: &t})
		if len(batch) == batchSize {
			flush()
		}
	}
	flush()
	buildDur := time.Since(start)

	// 混合负载：10 万条小批操作（成功修改 / 失败修改 / 查询）。
	// 局部性：围绕 hot 操作员一个滑动窗口反复修改。
	const rounds = 10_000
	const window = 2_000
	var okDur, failDur, queryDur time.Duration
	var okN, failN, queryN int

	for k := 0; k < rounds; k++ {
		base := (k * 37) % (hotTasks - window)
		switch k % 3 {
		case 0: // 成功修改：窗口内 10 条任务改期到互不冲突的空档并改优先级。
			t0 := time.Now()
			var changes []Change
			for i := 0; i < 10; i++ {
				id := fmt.Sprintf("hot-%d", base+i)
				slot := int64(2_000_000 + (k*10+i)*10)
				t := Task{ID: id, Operator: "hot", Start: slot, End: slot + 5,
					Priority: Priority(k % 4)}
				changes = append(changes, Change{ID: id, Upsert: &t})
			}
			if r := s.Apply(changes); r != nil {
				t.Fatalf("第 %d 轮成功修改被拒绝: %+v", k, r)
			}
			okDur += time.Since(t0)
			okN++
		case 1: // 失败修改：把窗口内 6 条任务改到同一重叠区间，必然超载。
			t0 := time.Now()
			var changes []Change
			for i := 0; i < 6; i++ {
				id := fmt.Sprintf("hot-%d", base+100+i)
				t := Task{ID: id, Operator: "hot", Start: 5_000_000, End: 5_000_100,
					Priority: Critical}
				changes = append(changes, Change{ID: id, Upsert: &t})
			}
			r := s.Apply(changes)
			if r == nil {
				t.Fatalf("第 %d 轮失败修改被意外接受", k)
			}
			if r.Operator != "hot" || r.Time != 5_000_000 || len(r.TaskIDs) != 6 {
				t.Fatalf("第 %d 轮见证异常: %+v", k, r)
			}
			failDur += time.Since(t0)
			failN++
		case 2: // 查询：get + active。
			t0 := time.Now()
			for i := 0; i < 10; i++ {
				if _, ok := s.Get(fmt.Sprintf("hot-%d", base+i)); !ok {
					t.Fatalf("第 %d 轮 Get 未命中", k)
				}
			}
			if got := s.Active("hot", int64(base)*10+1); len(got) > maxCapacity {
				t.Fatalf("第 %d 轮 Active 超载: %v", k, got)
			}
			queryDur += time.Since(t0)
			queryN++
		}
	}

	// 抽查：失败修改不得污染状态，5_000_000 处不应有占用。
	if got := s.Active("hot", 5_000_000); len(got) != 0 {
		t.Fatalf("失败批次污染了状态: %v", got)
	}
	if _, ok := s.Get("hot-0"); !ok {
		t.Fatal("存量任务丢失")
	}

	t.Logf("构建 %d 任务（%d 批）: %v", totalTasks, totalTasks/batchSize, buildDur)
	t.Logf("成功修改 %d 批: 总 %v, 均值 %v", okN, okDur, okDur/time.Duration(okN))
	t.Logf("失败修改 %d 批: 总 %v, 均值 %v", failN, failDur, failDur/time.Duration(failN))
	t.Logf("查询 %d 批: 总 %v, 均值 %v", queryN, queryDur, queryDur/time.Duration(queryN))
}
