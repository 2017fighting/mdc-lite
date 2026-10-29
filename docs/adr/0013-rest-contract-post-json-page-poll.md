# 0013. REST 契约：写操作 POST + JSON body、页码分页、轮询进度面

- 状态：Accepted（形状由 ADR-0018 做增量补充：三个响应字段 + 一个请求字段）
- 日期：2026-10-27
- 来源：wayfinder 票「REST/HTTP API 契约与任务列表查询面」（#16）

## 背景

前端内嵌在同一个二进制里、与后端同版本发布，所以不需要 API 版本号；家庭库量级（几万任务）不需要游标分页；单副本进程内维护 SSE 连接的成本大于收益（authelia 反代 + 轮询足够）。旧仓的成功响应不包裹、用键集游标、搜索覆盖 number/prefix/路径。

## 决定

- 前缀 `/api/`、**不版本化**；字段全 **snake_case**；**列表包裹** `{items, total, page, per_page}`，单体响应裸对象，空列表恒 `[]`；错误体 `{error:{code, message, detail?}}`。
- **写操作一律 POST + JSON body**，强制 `Content-Type: application/json`（否则 415，顺带挡表单 CSRF）；查询串只作手测次要入口。
- **页码分页**（`page` + `per_page` 白名单 `{20,50,100}`），排序**固定 `created_at DESC`**，不开 `sort` 参数；搜索**只搜番号且子串模糊**。
- **进度面 = 轮询**（列表 3s / 详情 2s，仅在存在非终态任务时）；不上 SSE、不做长轮询、不做 ETag 协商。
- **CLI 与 `mdc serve` 同一二进制，CLI 直接调进程内 service 层**；HTTP 只是 UI 的适配层。
- 六档原因码**不进错误体**（它是 `source_runs.reason`）；载荷只传机器码，中文映射在前端。

## 替代方案

- 键集游标 + 排序白名单（旧仓 ADR-0019）：被否 —— 家庭库量级页码分页足够，且页码对「翻到第 N 页」的直觉更友好。
- SSE / WebSocket：被否 —— 单副本 + 反代下不值得。
- `/api/v1/`：被否 —— 前后端同版本发布。

## 后果

- 15 个 V1 端点 + 7 个第二批端点（形状现在冻结，V1 不注册路由）。
- 待补号「跳过」不新增端点，直接调 `cancel`；`trigger=` 筛选靠 join `batches` 落地，不加索引。
- 图片端点带 `ETag`（= `sha256`）+ `immutable`；`w` 白名单 `{40,80,160,320,640}`。
