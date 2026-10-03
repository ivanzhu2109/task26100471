package taskalloc

import "sort"

// maxCapacity 是任何操作员任意时刻允许同时占用容量的任务数上限。
const maxCapacity = 5

// Scheduler 是进程内的任务分配准入门。调用串行执行，无需并发控制。
type Scheduler struct {
	tasks map[string]Task
	ops   map[string]*opSchedule
}

// opSchedule 维护单个操作员的占用日程索引。
// 仅未结束的 Critical / High 任务进入索引。
type opSchedule struct {
	events *eventNode // 占用数差分序列，根 maxPrefix 即峰值占用
	ivls   *ivlNode   // 占用区间，用于 stabbing 查询
}

// NewScheduler 返回初始为空的 Scheduler。
func NewScheduler() *Scheduler {
	return &Scheduler{
		tasks: make(map[string]Task),
		ops:   make(map[string]*opSchedule),
	}
}

func (s *Scheduler) schedule(operator string) *opSchedule {
	o := s.ops[operator]
	if o == nil {
		o = &opSchedule{}
		s.ops[operator] = o
	}
	return o
}

func (o *opSchedule) add(t Task) {
	o.events = addEvent(o.events, t.Start, 1)
	o.events = addEvent(o.events, t.End, -1)
	o.ivls = addInterval(o.ivls, t.Start, t.End, t.ID)
}

func (o *opSchedule) remove(t Task) {
	o.events = addEvent(o.events, t.Start, -1)
	o.events = addEvent(o.events, t.End, 1)
	o.ivls = removeInterval(o.ivls, t.Start, t.ID)
}

// Apply 原子地应用一批修改（最多 50 个不同任务 id）。
// 整批只按全部修改后的候选状态判断：成功则全部生效并返回 nil；
// 失败则全部不生效，返回超载见证。
func (s *Scheduler) Apply(changes []Change) *Rejection {
	// 先把候选修改落到各操作员索引上，并记录逆操作。
	// 索引只含占用区间，每步 O(log n)，与存量规模无关。
	type undoOp struct {
		op    *opSchedule
		task  Task
		added bool // true：本步新增，撤销即删除
	}
	var undos []undoOp
	affected := make(map[string]*opSchedule)

	// 候选覆盖层：同一 id 在批内多次出现时以最后一次为准，
	// 旧值必须先看覆盖层，避免索引中残留已失效的区间。
	pending := make(map[string]*Task, len(changes))

	for _, ch := range changes {
		old, existed := s.tasks[ch.ID]
		if p, ok := pending[ch.ID]; ok {
			existed = p != nil
			if p != nil {
				old = *p
			}
		}
		if existed && old.occupies() {
			o := s.ops[old.Operator]
			o.remove(old)
			undos = append(undos, undoOp{o, old, false})
			affected[old.Operator] = o
		}
		if ch.Upsert != nil {
			nt := *ch.Upsert
			nt.ID = ch.ID
			if nt.occupies() {
				o := s.schedule(nt.Operator)
				o.add(nt)
				undos = append(undos, undoOp{o, nt, true})
				affected[nt.Operator] = o
			}
			pending[ch.ID] = &nt
		} else {
			pending[ch.ID] = nil
		}
	}

	// 固定顺序检查，保证结果与批内排列及 map 迭代顺序无关。
	names := make([]string, 0, len(affected))
	for name := range affected {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		o := affected[name]
		if o.events == nil || o.events.maxPrefix <= maxCapacity {
			continue
		}
		// 候选日程中该操作员首次超载的时刻，以及该时刻的 6 项见证。
		t, _ := firstOverload(o.events, maxCapacity+1)
		ids := collectAt(o.ivls, t, maxCapacity+1, nil)
		// 逆序回滚全部候选修改，已提交状态不受污染。
		for i := len(undos) - 1; i >= 0; i-- {
			u := undos[i]
			if u.added {
				u.op.remove(u.task)
			} else {
				u.op.add(u.task)
			}
		}
		return &Rejection{Operator: name, Time: t, TaskIDs: ids}
	}

	// 全部合法，提交任务记录。
	for id, p := range pending {
		if p == nil {
			delete(s.tasks, id)
		} else {
			s.tasks[id] = *p
		}
	}
	return nil
}

// Get 读取已提交任务，便于检查失败批次是否污染状态。
func (s *Scheduler) Get(id string) (Task, bool) {
	t, ok := s.tasks[id]
	return t, ok
}

// Active 返回操作员在时刻 t 实际占用容量的任务 id（升序）。
// 已提交状态下至多 5 个，查询代价 O(log n + k)。
func (s *Scheduler) Active(operator string, t int64) []string {
	o := s.ops[operator]
	if o == nil {
		return nil
	}
	ids := collectAt(o.ivls, t, 1<<30, nil)
	sort.Strings(ids)
	return ids
}
