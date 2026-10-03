# admission

进程内任务分配准入库（Go）。处理批量改派、改期和结束任务，保证：
**每次成功提交后，任何操作员在任何时刻同时占用容量的任务都不超过 5 个。**
拒绝的批次不改变任何已提交状态，并返回可核查的超载见证。

## 接口

```go
s := admission.NewStore()

// 原子应用一批修改（一批最多 50 个不同任务 id）。
// 每项为 KindAdd（新增）/ KindSet（整条覆盖）/ KindDelete（删除）。
// 成功返回 nil；失败返回 *Rejection，已提交状态完全不变。
rej := s.Apply([]admission.Change{
    {Kind: admission.KindAdd, Task: task},
    {Kind: admission.KindSet, Task: task},     // 可同时改操作员/时间/优先级/结束标记
    {Kind: admission.KindDelete, Task: admission.Task{ID: id}},
})

task, ok := s.Get(id)              // 读取已提交任务
ids := s.Active(operator, t)       // t 时刻实际占用容量的任务 id（升序）
```

- `Task`：`ID`、`Operator`、`Start`/`End`（int64，左闭右开，`Start < End`）、
  `Priority`（`PriorityCritical/High/Medium/Low`）、`Ended`。
  仅**未结束**的 `Critical`/`High` 任务占用容量；其余任务照常保存，可重新变为占用。
- `Rejection`：`Operator`（超载操作员）、`Time`（候选日程中该操作员首次超载时刻）、
  `TaskIDs`（该时刻同时占用容量的 6 个不同任务 id）。
  见证完全来自本批修改后的候选状态。多个操作员冲突时任报一个（按操作员名序，可复现）。

## 实现要点

- 已提交状态：`map[id]Task` 全量保存；每个操作员一棵**区间 treap**
  （按 `(start,end,id)` 排序，子树维护 `maxEnd`），只索引占用容量的区间。
- 准入判断只依赖整批修改后的候选状态，与批内顺序无关。
  超载只可能出现在**新增占用区间的并集**内，因此只对这些局部区间做精确扫描线检查：
  候选占用数 = 已提交覆盖数 − 批内移除 + 批内新增，峰值 > 5 即拒绝。
  成本为 O(log n + k)，k 为局部相交区间数，不扫描全量任务或操作员完整日程。
- 先验证、后提交：拒绝时不触碰任何已提交状态，无需回滚或全量备份。
- 相邻端点不重复计数（左闭右开：结束事件与开始事件在同一时刻抵消）。

## 测试

```sh
go test ./...
```

- `store_test.go`：长任务与互不相交短任务、相邻端点、超载发生在已有任务开始时刻
  （核对报告时刻与 6 项见证）、两满员操作员批内交换、混合批次失败后的状态保留。
- `random_test.go`：固定种子随机小批序列，与朴素参照实现（全量克隆 + 扫描线）逐步对比，
  并依据候选状态独立核查每个见证。
- `perf_test.go`：20 万存量（10 万同属一个操作员）+ 1 万次局部小批
  （成功/失败/查询混合），记录耗时。参考：提交 20 万条约 0.1s，小批平均约 1.4µs。
