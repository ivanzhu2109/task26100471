package admission

// Priority 为任务优先级。仅未结束的 Critical 与 High 任务占用容量。
type Priority string

const (
	PriorityCritical Priority = "Critical"
	PriorityHigh     Priority = "High"
	PriorityMedium   Priority = "Medium"
	PriorityLow      Priority = "Low"
)

// Capacity 是任意操作员在任意时刻可同时占用容量的任务数上限。
const Capacity = 5

// Task 为一条已提交任务的完整记录。占用区间为左闭右开 [Start, End)。
type Task struct {
	ID       string
	Operator string
	Start    int64
	End      int64
	Priority Priority
	Ended    bool
}

// Occupies 报告该任务是否占用容量。
func (t Task) Occupies() bool {
	return !t.Ended && (t.Priority == PriorityCritical || t.Priority == PriorityHigh)
}

// ChangeKind 为批内单条修改的类型。
type ChangeKind int

const (
	// KindAdd 新增一条任务（id 此前不存在）。
	KindAdd ChangeKind = iota
	// KindSet 整条覆盖一条已有任务。
	KindSet
	// KindDelete 删除一条已有任务，仅使用 Task.ID。
	KindDelete
)

// Change 为批内单条修改。
type Change struct {
	Kind ChangeKind
	Task Task
}

// Rejection 为 apply 拒绝时返回的超载见证。
// Operator 在候选日程中于 Time 时刻（首次）同时占用容量的任务数超过 Capacity，
// TaskIDs 为该时刻同时占用容量的 6 个不同任务 id。
type Rejection struct {
	Operator string
	Time     int64
	TaskIDs  []string
}
