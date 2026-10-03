# taskalloc

进程内的任务分配准入库（Go）。处理批量改派、改期和结束任务，保证：
**每次成功提交后，任何操作员在任何时刻同时占用容量的任务都不超过 5 个。**
拒绝的批次不改变任何已提交状态，并返回可核查的超载见证。

## 数据模型

```go
type Task struct {
    ID       string   // 唯一任务 id
    Operator string   // 操作员 id
    Start    int64    // 开始时间（左闭）
    End      int64    // 截止时间（右开），Start < End
    Priority Priority // Low / Medium / High / Critical
    Ended    bool     // 是否已结束
}
```

仅未结束的 `Critical` / `High` 任务占用容量；`Low` / `Medium` 与已结束
任务照常保存，后续可重新变为占用容量的任务。

## 接口

```go
s := taskalloc.NewScheduler()

// 原子应用一批修改（最多 50 个不同任务 id）。
// Change.Upsert 为 nil 表示删除，否则为新增或整条覆盖
// （可同时改变操作员、时间、优先级、结束标记）。
// 整批只按全部修改后的候选状态判断：成功全部生效返回 nil；
// 失败全部不生效，返回 *Rejection。
rej := s.Apply([]taskalloc.Change{
    {ID: "t1", Upsert: &taskalloc.Task{Operator: "op1", Start: 0, End: 10, Priority: taskalloc.Critical}},
    {ID: "t2"}, // 删除
})

// 拒绝见证：超载操作员、候选日程中首次超载时刻、
// 该时刻同时占用容量的 6 个不同任务 id。
if rej != nil {
    fmt.Println(rej.Operator, rej.Time, rej.TaskIDs)
}

task, ok := s.Get("t1")          // 读取已提交任务
ids := s.Active("op1", 5)        // 某时刻实际占用容量的任务 id
```

## 实现要点

- 每个操作员两棵 treap：占用数差分序列（子树聚合和与最大前缀和，
  根即峰值占用，O(log n) 定位首次超载时刻）与区间索引
  （按 `(start, id)` 排序、聚合 `maxEnd`，支持 stabbing 查询）。
- `Apply` 先把候选修改落到索引并记录逆操作，检查峰值后再决定
  提交或逆序回滚；回滚不依赖全量备份。
- 小批提交、拒绝与单次查询的代价都是 O(B log n)，不扫描、排序或
  复制全部任务或某个操作员的完整日程；稀疏大跨度日程精确处理，
  不做逐点展开或时间桶近似。

## 测试

```sh
go test ./...
```

覆盖：长任务与互不相交短任务、相邻端点不重复计数、超载发生在已有
任务开始时刻、满容量操作员批内交换、失败批次状态零污染、朴素参照
实现的随机对照，以及 20 万任务 / 1 万批混合负载的规模用例（记录耗时）。
