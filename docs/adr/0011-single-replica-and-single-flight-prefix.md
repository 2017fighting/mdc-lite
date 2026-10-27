# 0011. 单副本，批次级前缀全局单飞（有意推翻旧仓 ADR-0012）

- 状态：Accepted
- 日期：2026-10-27
- 来源：wayfinder 票「并发模型」（#15）、「监控与触发模型」（#10）

## 背景

旧仓 ADR-0012 明写「多根与手动扫描并发跑同一条接入路径不加锁、不串行」，理由是去重闸吸收重复、并发无害。这个理由成立，但它只算了正确性、没算 NFS 上全树 walk 的成本：并发 walk 会重复走嵌套根（`/media` 与 `/media/sub`），本仓的前缀还包含 `filter` + `group`（Levenshtein/BK-tree 的 CPU）。

## 决定

- **三层、三个数、一把锁**：`concurrency.tasks`（默认 4，槽内 `parse→scrape→images→nfo→output` 串行）、`concurrency.requests`（默认 8，Fetcher 全局在飞）、`concurrency.images`（默认 4，单任务取图并发）；**只配在 `config.yaml` 一处**，步骤级不开 `with.concurrency`。
- **批次级前缀（`scan`/`filter`/`group`）全局单飞**（一把 mutex）；**tick 拿不到 → `TryLock` 失败 → 跳过本轮 + 记日志，水位线不推进**；手动预览与建任务 `Lock` 排队（所以预览最多只等一次在飞的 walk）。
- 前缀槽与任务槽互相独立；批次的任务建完即释放前缀槽。
- **SIGTERM 优雅中断**：停取新任务 → cancel → 最多 25s → 落 `interrupted`（中断状态落库走 `context.WithoutCancel`）。
- 队列 = `tasks.status='queued'` 的全局 FIFO（不建队列表）；失败完全隔离、不熔断。
- **单副本写进不变量**。

## 替代方案

- 旧仓 ADR-0012（不加锁、不串行）：**有意推翻** —— 正确性不是唯一成本。
- 步骤级数据流（每步一个池）：被否 —— 「当前阶段」含糊、取消与重跑跨池协调、写库点分散，换来的吞吐在按域限速（1 req/s）面前基本吃不到。
- 批次熔断 / 全局熔断：不做 —— 冷却已经快速失败。

## 后果

- 「任务」是调度单位 = 取消单位 = 留痕单位 = 重跑单位（与 #2/#9 同一单位）。
- 手动预览可能排在一次监控 walk 之后（家用库秒级）。
- `step_runs` 无增列；池占用不上 API 面（队列等待可由 `task_runs.started_at − tasks.created_at` 算）。
