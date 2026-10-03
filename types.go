// Package taskalloc 提供进程内的任务分配准入控制。
//
// 核心性质：每次成功提交后，任何操作员在任意时刻同时占用容量的任务
// 不得超过 5 个。apply 以批为单位原子生效；拒绝的批次不改变任何已
// 提交状态，并返回可核查的超载见证（操作员、首次超载时刻、该时刻
// 同时占用容量的 6 个任务 id）。
package taskalloc

// Priority 任务优先级。仅未结束的 Critical / High 任务占用容量。
type Priority int

const (
	Low Priority = iota
	Medium
	High
	Critical
)

// Task 是一条已提交或待提交的任务记录。
// 时间区间为左闭右开 [Start, End)，Start 严格小于 End。
type Task struct {
	ID       string
	Operator string
	Start    int64
	End      int64
	Priority Priority
	Ended    bool
}

// occupies 报告该任务是否占用操作员容量。
func (t Task) occupies() bool {
	return !t.Ended && (t.Priority == Critical || t.Priority == High)
}

// Change 描述批内对单个任务 id 的修改。
// Upsert 为 nil 表示删除；否则为新增或整条覆盖。
type Change struct {
	ID     string
	Upsert *Task
}

// Rejection 是 apply 拒绝批次时返回的超载见证。
// 见证来自本批修改后的候选状态。
type Rejection struct {
	Operator string   // 发生超载的操作员
	Time     int64    // 该操作员候选日程中首次超载的时刻
	TaskIDs  []string // 该时刻同时占用容量的 6 个不同任务 id
}
