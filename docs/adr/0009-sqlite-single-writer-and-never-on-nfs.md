# 0009. SQLite 单文件、单写连接，且不得落在 NFS 上

- 状态：Accepted
- 日期：2026-10-27
- 来源：wayfinder 票「任务与存储模型」（#9）、「并发模型」（#15）

## 背景

用户环境是 NFS 媒体库 + k8s 单容器。并发写入 SQLite 会 BUSY：实测 `MaxOpenConns >= 2` 时 250 次并发写出现 **109~153 次** `SQLITE_BUSY`，`=1` 时 **0 次**；驱动默认 `busy_timeout = 0`，且官方明说 busy handler 不保证被调用。SQLite 官方也明说 **WAL 不能跨网络文件系统**。

## 决定

- 存储栈：`modernc.org/sqlite`（纯 Go，`CGO_ENABLED=0`）+ `pressly/goose v3` 内嵌迁移（`go:embed`，启动 `Up`，只前进）。
- **写句柄 `SetMaxOpenConns(1)`** + `_txlock=immediate`；**读走独立只读池**（`SetMaxOpenConns(4)`）；**应用层不加 mutex**。
- pragma 一律走 DSN（`_pragma=journal_mode(WAL)`、`_pragma=busy_timeout(5000)`、`_pragma=synchronous(NORMAL)`、`_pragma=foreign_keys(1)`），不用连接级 `Exec("pragma ...")`。
- `synchronous=NORMAL`：进程崩溃零丢失；断电可能丢最近几个事务 —— 而状态库是**派生数据**（真相在输出目录），丢了就是任务回 `queued` 重跑。
- **`db_path` 所在的 `/config` 必须是非 NFS 卷**（部署约束；可考虑启动时检测文件系统类型并拒绝启动）。
- 事务边界三条：状态迁移 + 派生列同事务；每步结束一条独立事务写留痕；run 结束一条事务批量写 `file_rows` + 清理。

## 替代方案

- Postgres：已划出范围（grilling 裁决）。
- 读写同池（旧仓做法）：被否 —— 前端列表查询会被 run 结束的批量写挡住，且写事务会分散到多条连接。
- 应用层 mutex + 多写连接：被否 —— 两层排队是「事务里再取连接」那类静默挂死的温床。

## 后果

- 同进程内不该出现 `SQLITE_BUSY`；真出现就等满 `busy_timeout` 后如实报错，不做重试式 busy 处理。
- 造 BUSY 的测试需要第二个进程或第二个 `*sql.DB`。
- 单写连接 + 单副本写进不变量；多副本是将来新的一次努力。
