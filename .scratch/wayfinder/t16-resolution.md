# t16 收口稿：REST/HTTP API 契约与任务列表查询面

> 本文件是本地底稿；canonical 结论是 issue #16 的结票评论。
> 取证底稿：`.scratch/wayfinder/t16-facts.md`（528 行，逐条带 `文件:行号`）。

**一句话**：一套 `/api/` 前缀、snake_case 字段、列表统一包裹、写操作一律 POST + JSON body、错误体 `{error:{code,message,detail?}}` 的契约；V1 15 个端点 + 第二批 7 个端点；进度面用轮询（不上 SSE）；CLI 走进程内 service 层、不共用 HTTP 面；细粒度领域错误码与 HTTP 状态码并存。

---

## 1. 契约基线（横切）

| # | 约定 | 理由 / 出处 |
|---|---|---|
| B1 | 前缀 `/api/`，**不做版本号** | 前端内嵌进同一二进制，前后端同版本发布；#1「Go 单二进制 + 内嵌 Vite/React」 |
| B2 | JSON 字段全 **snake_case** | 与 DB 列名（`task_id`/`created_at`）、配置键（`dir_template`）、六档原因码（`egress_unavailable`）一致，跨层不再映射 |
| B3 | **列表包裹** `{items, total, page, per_page}`；**单体响应裸对象**；空列表恒 `[]`（Go nil slice 不得序列化成 `null`） | 加字段/加分页不是破坏性变更；`[]` 照旧仓 `api/wire.go` 的实测约定 |
| B4 | 错误体 `{error:{code, message, detail?}}` | 形状与旧仓 `internal/apierr/apierr.go` 同形（`code` 稳定机器码、`message` 英文散文）；`detail` 为可选结构化对象 |
| B5 | **写操作一律 POST + JSON body**；写端点要求 `Content-Type: application/json`（否则 415）；查询串只作手测次要入口 | 照旧仓 ADR-0011（qB 不转义 `&`/`#`/空格，目录名恰好高频含这些字符）；`415` 顺带挡住表单 CSRF，配合 authelia 的 SameSite cookie |
| B6 | 前端与 API **同源**（内嵌），**不开 CORS**；**鉴权归前置 authelia，进程内不做**；`GET /api/health` 是唯一由 kubelet 直连容器的路径 | 地图已定；PRODUCT.md「应用内不做鉴权」 |
| B7 | 前端把状态/触发/步骤/原因码**机器码**交本地映射表翻中文，载荷不传中文 | 原型 `ST_LABEL`/`TRIGGER_LABEL`/`reasonLabel` 等 10 张映射表（`frontend-views.html:982-1023`） |

### 进度面（B8）

**轮询，不上 SSE**：列表页仅在存在非终态任务时每 **3s** 轮 `GET /api/tasks`；详情页任务非终态时每 **2s** 轮 `GET /api/tasks/{id}`。不加 `GET /api/events`、不做长轮询、不做 ETag 协商。

理由：单副本（#15 不变量）+ authelia 前置（SSE 要处理反代缓冲、心跳、断线重连）+ #7 的验收「只等期望值」不要求实时。轮询也让 V1 的测试面保持「一次请求一次断言」。

---

## 2. 端点清单

### 2.1 V1（15 个）

| 方法 + 路径 | 用途 | 关键载荷 |
|---|---|---|
| `GET /api/health` | k8s 探针 / 运维 | → `{status:"ok"\|"degraded", version, db:{ok, path, migrations}}`；readiness = DB 可打开 + 迁移完成；不就绪 → **503** `db_unavailable` |
| `GET /api/pipelines` | pipeline 列表（与 `mdc config check` 的对照面） | → `{items:[{id, name, steps:[{type, name?}]}]}`；**不给 `{id}` 详情端点**（配置值不经 API；启动时拓扑校验已保证运行中不存在无效 pipeline） |
| `GET /api/fs/dirs?path=` | 容器内目录选择器 | → `{path, root:{path,label}, dirs:[{name, dir_count, file_count}], truncated}`；越界 **400** `root_not_browsable`、不存在 **404**；只列目录、不返回文件内容；上限 200 条 |
| `POST /api/scan/preview` | 扫描预览（跑到 `--until parse`） | `{pipeline, roots[], exclude[]}` → **202** `{preview_id}` |
| `GET /api/scan/previews/{id}` | 预览轮询 | → `{state:"running"\|"done"\|"failed", walked_files, matched, groups:[…], error?}` |
| `POST /api/tasks` | 建批次（manual 触发） | `{pipeline, roots[]}` → **200** `{batch_id, created, skipped:[{path, reason}]}` |
| `GET /api/tasks` | 任务列表 | 查询参数见 §3.1 → `{items, total, page, per_page}` |
| `GET /api/tasks/{id}` | 任务详情（四个 tab 一次给全） | → 任务行 + `metadata` + `task_files[]` + `step_runs[]` + `source_runs[]` + `file_rows[]` |
| `POST /api/tasks/{id}/rerun` | 任务级后缀重跑（从 `parse` 起） | → **202** `{task_run_id}`；在跑任务 → **409** `task_running` |
| `POST /api/tasks/{id}/cancel` | 取消任务（也是待补号的「跳过」） | → **200** `{status:"cancelled"}`；已终态 → **409** `task_terminal` |
| `POST /api/tasks/rerun` | 批量重跑 | `{ids[]}` → **200** `[{task_id, ok, error?}]`（逐条结果，不整批失败） |
| `POST /api/tasks/{id}/numbers` | 待补号提交（两处 UI 共用一个写端点） | `{files:[{path, assigned_number, is_part}]}` → **202** `{task_run_id}`；未填全 → **422** `numbers_incomplete` + `detail.missing_paths[]` |
| `GET /api/tasks/{id}/thumb?role=&idx=&w=` | 列表缩略图（服务端缩放） | `w` 白名单 `{40,80,160,320,640}`，缺省 = 原图；非法 `w`/`role` → 400 |
| `GET /api/tasks/{id}/image?role=&idx=` | 详情原图 / 灯箱 | 同上 |
| `GET /api/batches?limit=` | 只给列表页的批次筛选下拉用（**不做独立视图**） | → `{items:[{id, pipeline_id, trigger, trigger_ref, status, started_at, finished_at, total, tasks, succeeded, partial, failed, needs_number, skipped}]}`；时间标签用 `started_at`（`batches` 表无 `created_at` 列） |

### 2.2 第二批（7 个，**形状现在冻结，V1 不注册路由**）

| 方法 + 路径 | 用途 |
|---|---|
| `GET /api/monitors` | 监控列表（含每根水位线与运行时状态） |
| `POST /api/monitors` | 创建 |
| `POST /api/monitors/{id}` | 替换（body = 完整行） |
| `POST /api/monitors/{id}/delete` | 删除 |
| `POST /api/monitors/{id}/enable` | 启用（保留水位线） |
| `POST /api/monitors/{id}/disable` | 暂停（保留水位线） |
| `POST /api/webhooks/qbittorrent` | qBittorrent webhook（ADR-0011 契约） |

监控行形状（#10 表形状直译）：
```
{id, name, enabled, pipeline_id, interval_seconds, quiet_window_seconds,
 created_at, updated_at,
 dirs:[{path, kind:"root"|"exclude", watermark_at}],
 runtime_state:"ok"|"pipeline_missing"|"root_unavailable"}
```
`runtime_state` 每次启动重算、**不落库**（#10）。创建/更新的 400 校验：`unknown_pipeline` / `exclude_not_under_root` / `monitor_root_nested` / `empty_selection`（0 个路径）。

webhook（照旧仓 ADR-0011 + 旧仓实现实测）：
- payload `{path(%F), name(%N), hash(%I), category(%L)}`
- `X-Webhook-Token` 请求头（**只认头、不认查询串**——查询串会被 qB 写进自己的日志）；token 不匹配 → **401** `unauthorized`
- 功能关闭（`webhook.enabled=false`）→ **404** `not_found`
- 分类白名单未命中 → **202** `{reason:"category filtered"}`（不报错，避免 qB 重试）
- 命中 → **202** `{batch_id, created, skipped[]}`；`trigger_ref` = torrent hash
- 不做就绪判定（信任 torrentFinished），只过滤 `.!qB`/`.part` 与扩展名白名单

---

## 3. 载荷与查询面

### 3.1 `GET /api/tasks` 的查询参数

```
status=     可重复（多选，OR）；值 = 7 状态
batch=      批次 id（单个）
trigger=    manual|monitor|webhook
number=     番号，**子串模糊匹配**（LIKE '%x%'，输入归一化大写）
created_after= / created_before=   RFC3339（前端把「最近 24 小时 / 最近 7 天」算成时间戳）
page=       ≥1，越界回空 items 且 total 不变
per_page=   白名单 {20,50,100}，其他 400
排序        固定 created_at DESC，**不开 sort 参数**
```

- **`trigger=` 的落点**：`tasks` 表没有 trigger 列 → 先查 `batches`（行数少，全扫可忽略）取该 trigger 的 `batch_id` 集合，再用 `ix_tasks_batch` 过滤 tasks。**不加索引、不碰 #9**，#11「不需要新索引」的裁定继续成立。
- **`number=` 用子串模糊**：家庭库量级（几千行）全扫可忽略；搜「123」能出 `ABP-123`，符合「即时搜索」。不做前缀/精确模式切换。
- `total` = 过滤后的总数（与 `items` 同一组条件）。

### 3.2 任务行（列表 item 与详情共用）

落库列原样：`{id, status, number, batch_id, pipeline_id, last_error, created_at, updated_at}`。

派生字段（只读展示、不落库）：

| 字段 | 来源 |
|---|---|
| `stage` | 该任务最新一条 `step_runs.type`（#9：无 stage 列，阶段派生） |
| `title` | `metadata_json` 的 `title` |
| `sources[]` / `missing[]` / `partial` | `metadata_json` 的 `Sources`/`Missing`；`partial = len(missing)>0`（#4） |
| `trigger` / `trigger_ref` | join `batches` |
| `file_count` | `task_files` 计数 |
| `out_dir` | 输出目录（`file_rows` / 目录模板） |

列表 item 的**额外**字段：**仅 `needs_number` 任务**带 `files:[{path, role, part_index, assigned_number, is_part, candidates[]}]` —— 待补号侧栏视图不跳转就能填（详情端点的 `task_files[]` 形状与它一致）。

原型里的 `hue`（配色种子）与 `conf`（置信度）是原型专有展示字段，**不进载荷**（无数据来源）。

### 3.3 详情端点 `GET /api/tasks/{id}`

一个端点给全四个 tab（数据量小：step 8 条、源 4 条、台账几十行；轮询整包也只几 KB）。图片只给 **ImageRef**（`{role, idx, ext, bytes}`），不给字节。

- `metadata`：14 源级字段 + `Sources`/`Missing`/`Partial`（#4 的模型，`Derived` 不进 API）
- `task_files[]`：`{path, kind, role, part_index, follows_part, size, mtime, active, assigned_number, is_part, candidates[]}`
  - `role ∈ {primary, part, companion}` —— **本票把 #9 的 `role` 列语义定死**
  - `candidates[]` 来自 `parse` 步骤的 `step_runs.summary_json`（#9：每步一条摘要）
  - 原型里的 `primary` 布尔**不单列**，前端读 `role == 'primary'`
- `step_runs[]`：`{id, idx, type, name, status, started_at, ms, summary_json, error}`（`batch_id`/`task_run_id` 的外键不必回传，父对象已表达）
- `source_runs[]`：`{id, step_run_id, source_id, status, reason, err, fields_json, ms}`（`reason` = #12 的六档原因码）
- `file_rows[]`：`{task_id, task_run_id, step_run_id, kind, role, idx, src, dst, bytes, sha256, action, created_at}`

### 3.4 扫描预览

`POST /api/scan/preview {pipeline, roots[], exclude[]}` → **202** `{preview_id}`；轮询 `GET /api/scan/previews/{id}`。

**异步的理由**：authelia/反代默认超时在 60s 级，数万文件 NFS 全树 walk 会被砍断（#1 雾区已记「大库扫描成本」）。预览状态**只在内存、不落 DB**，重启即丢；保留 15 分钟或被下一个预览覆盖；**无取消端点**（可丢弃的东西不配取消语义）。

```jsonc
{
  "state": "running" | "done" | "failed",
  "walked_files": 12483,     // 已 walk 文件数（进度）
  "matched": 21,             // 进候选的数量
  "groups": [{
    "primary": "ABP-123.mp4", "parts": ["ABP-123-cd2.mp4"],
    "number": "ABP-123", "candidates": [],
    "marker": "cd2", "dir": "/media/xxx", "size": 1234567, "res": "1920x1080",
    "occupied": false        // 该组任一路径已被 active 任务占用 → 前端置灰
  }],
  "error": {"code": "…", "message": "…"}   // 仅 failed
}
```

### 3.5 图片端点

- 强缓存 + 不可变 URL：响应带 `ETag`（= `file_rows.sha256`）与 `Cache-Control: private, max-age=31536000, immutable`；`If-None-Match` 命中 → **304**
- `w` 白名单 `{40,80,160,320,640}`，缺省 = 原图；非法 → **400**
- 图片不在（输出目录被外部删了）→ **404** `image_missing`
- 缩放结果进程内 LRU 缓存（单副本，重启即丢）
- 不抽视频帧（V1 无 ffmpeg）

---

## 4. 错误码（细粒度领域 code，与 HTTP 状态码并存）

通用客户端看状态码，UI/CLI 按 `code` 分支到具体文案；`message` 给人读；`detail` 可选结构化。

| 状态 | code | 何时 |
|---|---|---|
| 400 | `bad_request` | 通用参数/校验失败（非法 `w`/`role`、`per_page` 非白名单…） |
| 400 | `empty_selection` | 0 路径 / 空选择（#10） |
| 400 | `root_not_browsable` | `fs/dirs` 的 path 越出 `browse_roots` |
| 400 | `exclude_not_under_root` | 排除不是某个根的真子目录（#10） |
| 400 | `monitor_root_nested` | 跨监控根相同/嵌套（#10） |
| 400 | `unknown_pipeline` | 引用的 pipeline id 不存在（monitor 创建/更新） |
| 401 | `unauthorized` | webhook token 不匹配 |
| 404 | `not_found` | 资源不存在；webhook 功能关闭 |
| 404 | `image_missing` | 图片文件已不在 |
| 409 | `task_running` | 重跑一个在跑的任务（#15 明确「#16 面用 409」） |
| 409 | `task_terminal` | 取消一个已终态任务 |
| 415 | `unsupported_media_type` | 写端点非 JSON Content-Type |
| 422 | `numbers_incomplete` | 待补号未填全，`detail.missing_paths[]` |
| 500 | `internal` | 兜底 |
| 503 | `db_unavailable` | health 不就绪（DB 打不开 / 迁移未完成） |

**六档原因码（`egress_unavailable`/`timeout`/`blocked`/`cooldown`/`http_5xx`/`parse`）不进错误体**——它们是 `source_runs.reason` 的领域字段（#12），不是 API 错误码。

`skipped[].reason`（建批次/ webhook 的逐路径跳过原因）：`path_occupied`（被 active 任务占用）/ `filtered`（未过 filter 规则）/ `not_found`（路径不存在）。

---

## 5. 已定语义的落地口径

- **待补号提交**：`path` 为键（靠 #9 的 `ux_task_files_active_path`）；**全填才提交**（缺 → 422 并列出缺哪些 path——`needs_number` 是任务级状态，部分填开不了 run）；成功 → 202 + 新 `task_run_id`，新 run 从 `parse` 起，任务状态复位 `queued`。
- **待补号的「跳过」**：不新增端点，前端直接调 `POST /api/tasks/{id}/cancel`（转 `cancelled`、`active=0` 释放路径占用）。
- **取消的适用范围**：`queued`/`running`/`needs_number` 都可取消；已终态 → 409 `task_terminal`（与重跑对称：重跑只对终态开放、在跑 409）。
- **待补原因**：复用 `tasks.last_error` 存机器码（`ambiguous` / `duplicate_stem`），API 字段名就叫 `last_error`，前端按 `status` 决定翻成「解析出多个候选番号（分歧）」还是失败原因。**不新增列、不碰 #9 表形状。**
- **建批次**：`roots[]` 复用 #10 的「触发注入路径集合覆盖 `scan.with`」语义，元素可为**文件或目录**；勾选以组为单位 = 前端把组内全部文件（含伴随文件）展开成 roots；服务端对这些 roots 走 `filter→group→parse`（**不重扫整树**）。`trigger="manual"`，`trigger_ref` = 预览选定的根（无则空）。
- **池占用/排队可见性不上 API 面**（V1）：并发度只进日志与进程内计数（#15），队列等待可由 `task_runs.started_at − tasks.created_at` 算得出；将来要显示再开 `GET /api/stats`。
- **CLI 与 HTTP 的关系**：CLI 与 `mdc serve` 同一二进制，**直接调进程内 service 层**；HTTP 只是给 UI 的适配层。`mdc config check` / `mdc source test` 不需要 server 在跑。CLI 的子命令清单**不在本票的 HTTP 面内**，随 `/to-spec` 落。
- **历史保留**：每次重跑开新 `task_runs`；`step_runs`/`source_runs`/`file_rows` 都挂这次 run（#9）。详情端点的三个数组只给**最近一次 run**，历史留痕不在 V1 的读面。

---

## 6. 对已结票的修正

1. **#8（配置文件 schema 与校验）增键 2 处**
   - `browse_roots` 从字符串数组改为**对象数组** `[{path, label}]`（`label` 可省、缺省 = path）——#11 裁决要「容器内绝对路径 + 挂载点标签（如「媒体库 · /media」）」，字符串数组里没有标签的位置。
   - 新增 **`webhook` 块** `{enabled: false, token: "", categories: {<分类名>: <pipeline_id>}}`（全键有默认、首次启动不生成文件、`mdc config check` 打印生效值与来源的规则不变）。
2. **#9（任务与存储模型）无表形状改动**，但两处语义由本票定死：
   - `task_files.role ∈ {primary, part, companion}`；
   - `tasks.last_error` 在 `needs_number` 任务上存待补原因机器码（`ambiguous`/`duplicate_stem`）。
3. **#15（并发模型）留给本票的两项已收口**：取消端点/状态码（§2.1、§5）、池占用不上 API 面（§5）。
4. **原型资产的一处旧文本**（不改文件，记录在案）：`prototype/frontend-views.html:1291` 仍写「当前预览截止于分组阶段」，与 #11 裁决的 **`--until parse`** 不一致；**实现以 `--until parse` 为准**。
5. **有意与旧仓不同的三处**（避免后来者以为是抄漏）：成功响应**统一包裹**（旧仓 `api/server.go` 成功响应不包裹）；**页码分页**（旧仓 ADR-0019 选了键集游标 + 白名单排序）；搜索**只搜番号且子串模糊**（旧仓 `q` 搜 number/prefix/路径）。

---

## 7. 明确不做（本票）

- SSE / 长轮询 / WebSocket；`GET /api/events`
- 端点的版本号（`/api/v1/`）
- `sort` 参数、游标分页、`GET /api/pipelines/{id}`、`GET /api/batches/{id}`、批次独立视图
- 任务删除端点（V1 不做删除，任务只增不减——地图 Out of scope / 雾区）
- 进程内鉴权、CORS、CSRF token（SameSite + JSON Content-Type 已挡）
- 运行时日志查看器（#8/#11：可查询留痕就是 `step_runs` + `source_runs`）
- 服务端渲染、GraphQL、gRPC 面
- 复用旧仓 78 条 API 契约（地图 Out of scope）

---

## 8. 交棒给 `/to-spec` 的手交项

- **术语表**（#2 已定落仓时点 = `/to-spec`）：预览（preview）、任务运行（task_run）、批次（batch）、胜出源（Sources）、落盘台账（file_rows）、待补号（needs_number）、水位线（watermark）、原因码（reason）。
- **ADR 候选**（domain-modeling：难逆转 + 需上下文 + 真取舍）：
  1. 「写操作一律 POST + JSON body」（放弃 REST 动词，为 qB 的字符串替换与目录名里的 `&`/`#` 让步）
  2. 「页码分页，不用键集游标」（与旧仓 ADR-0019 相反，为 UI 的页码控件与 `total` 让步）
  3. 「进度面用轮询，不上 SSE」（单副本 + authelia 前置下的取舍）
- **CLI 子命令清单**（`mdc serve` / `scan` / `config check` / `source test` 的参数面）——Q3 已定走进程内 service 层，命令清单随 `/to-spec`。

---

## 9. 取证出处

- 事实底稿：`.scratch/wayfinder/t16-facts.md`
- 旧仓 webhook 契约：`~/clone/open-mdcng/docs/adr/0011-inbound-trigger-unified-ingest.md`、`api/webhook.go:39-101`
- 旧仓错误体：`~/clone/open-mdcng/internal/apierr/apierr.go:15-29`
- 旧仓分页对照：`~/clone/open-mdcng` ADR-0019（键集游标）
- 本仓原型：`prototype/frontend-views.html:693-880, 1070-1600, 1997-2035`
- 已结票：#9（表/索引）、#10（监控与触发）、#11（视图形状与 §9 草案）、#12（原因码）、#15（并发与取消/重跑语义）、#8（配置键）、#4（字段模型）、#3（分组与待补号）
