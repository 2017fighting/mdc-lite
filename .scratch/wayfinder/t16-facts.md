# t16 备料：REST/HTTP API 契约与任务列表查询面 —— 事实收集

> 只读取证，不含决策。所有出处为 `文件:行号` 或「执行的命令」。
> 仓库：`/home/zhao/clone/mdc-lite`（GitHub `2017fighting/mdc-lite`）；参考仓：`/home/zhao/clone/open-mdcng`。
> 取证时间：HEAD = `f6d021c`。

---

## 1. `README.md`（仓库根，159 行）

### 1.1 逐条摘录：进程形态 / HTTP / 端口 / CLI / 前端 / 监控 / webhook / 手动模式

| 行号 | 原文 | 主题 |
|---|---|---|
| 1 | `> 整体采用pipeline设计，可以自由组合，增删步骤` | 形态（pipeline） |
| 5 | `0. 设置一个监控：轮询模式，监控目录[/mnt/path/a, /mnt/path/b]，排除目录[/mnt/path/a/ignore]，使用配置文件1` | 监控（轮询） |
| 6 | `1. 监控扫描/手动扫描 得到一个文件列表` | 监控 + 手动 |
| 16 | `## 监控模式` | 监控 |
| 18 | `> 可以配置多个监控，多个监控的监控目录不能重叠，可以指定这个监控扫描到的新文件用哪个配置文件进行处理` | 监控 |
| 20 | `监控模式：inotify/轮询` | 监控（两种模式） |
| 21 | `监控目录: 支持同时监控多个目录` | 监控 |
| 22 | `排除目录：在监控目录里不想监控某些目录` | 监控 |
| 23 | `使用的配置文件：` | 监控（引用配置文件） |
| 25 | `### 监控模式` | 监控 |
| 27 | `#### 轮询` | 监控 |
| 29 | `> 明确提醒用户，不要将下载器的下载中目录加入监控目录` | 监控 |
| 31 | `1. 程序启动时，扫描监控目录，记录所有文件到内存里` | 监控（进程内内存） |
| 32 | `2. 每隔30s，扫描监控目录，和上次扫描做diff，检查新增文件` | 监控（30s 间隔） |
| 33 | `3. 防抖：再隔3s，检查一下，直到文件不再新增，~~还要确保文件已经写入完成…~~` | 监控（防抖；含删除线） |
| 34 | `4. 记录下这次扫描新增的文件` | 监控 |
| 36–38 | `#### inotify` / `这个就简单了，注意防抖就得了` | 监控（inotify） |
| 40 | `## 手动模式` | 手动模式 |
| 42 | `选择一个配置文件，指定某个目录，进行一次全量扫描，展示所有能进入候选处理列表的文件` | 手动模式 |
| 43 | `选中某些文件，可以用这些选中的文件创建任务` | 手动模式（勾选建任务） |
| 88 | `输出：各个字段 图片二进制(…)(网页上展示原始链接以及图片内容)` | 前端（仅此处提「网页上展示」） |
| 144–149 | `### 输出` / `poster => {config.OUTPUT_DIR}/{number}/poster.png` … `{} => {config.OUTPUT_DIR}/{number}/movie.nfo` | 输出路径 |
| 155 / 157 | `#### 移动图片` / `#### 硬链接视频`（均无正文） | 输出（空小节） |

### 1.2 明确回答

- **README 提没提 HTTP API？——没有。**
  证据（执行的命令）：`grep -n -i -E "api|rest|endpoint|http|json" README.md` → **零命中**。
- **有没有 CLI 命令清单？——没有。**
  证据：`grep -n -i -E "cli|命令行|mdc " README.md` → **零命中**（README 全文没有任何 `mdc <cmd>` 形式的命令）。
- **端口 / 监听地址 / 进程形态**：README 全文**零命中**。`grep -n -i -E "端口|listen|server|9208|docker|容器" README.md` → 零命中。
  （端口 9208 的事实来源是 `PRODUCT.md:32` 与 issue #1 正文，不是 README。）
- **前端**：README 只在 88 行说「网页上展示原始链接以及图片内容」，没有 Vite/React/SPA/静态资源的任何描述。
- **webhook**：README 全文**零命中**（`grep -n -i webhook README.md` 无结果）。qBittorrent 也未出现。
- **监控**：README 明确写「inotify/轮询」（20 行），而 #10 已裁定 inotify 不实现、README 待改（见 §2.3）。
- **手动模式**：README 40–43 行，语义是「选配置文件 + 指定目录 + 全量扫描 + 展示候选 + 勾选建任务」。
- **进程内状态**：31 行明说轮询基线「记录所有文件到**内存**里」——与 #9「不预留 `runtime_state`」一致。

---

## 2. 三张票的结票评论

命令：`gh issue view 9 --comments` / `gh issue view 15 --comments` / `gh issue view 10 --comments`。

### 2.1 #9「任务与存储模型」——结票评论

**一句话（原句）**：`V1 的库是 7 张业务表 + goose 版本台账——batches / tasks / task_runs / task_files / file_rows / step_runs / source_runs`；`去重靠存储层的部分唯一索引，崩溃靠启动复位重跑，幂等基准仍是输出目录`。

#### 列名（原样）

```sql
batches(
  id INTEGER PK,
  trigger TEXT,        -- manual | monitor | webhook（枚举归 #10）
  trigger_ref TEXT,    -- 触发来源标识（目录路径 / webhook 载荷摘要）
  pipeline_id TEXT,
  status TEXT,         -- running | succeeded | partial | failed | cancelled
  total INT, tasks INT, succeeded INT, partial INT, failed INT,
  needs_number INT, skipped INT, error INT,
  started_at, finished_at
)

tasks(
  id INTEGER PK,
  status TEXT,         -- queued | running | succeeded | partial | failed | needs_number | cancelled
  number TEXT,         -- 归一化番号（primary 定，#3）
  batch_id INTEGER REFERENCES batches(id),
  pipeline_id TEXT,
  metadata_json TEXT,  -- 14 源级字段 + Sources + Missing + Partial（#4）
  last_error TEXT,
  created_at, updated_at
)

task_runs(
  id INTEGER PK,
  task_id INTEGER REFERENCES tasks(id),
  run_no INT,          -- 任务内自增，1 起；attempt = 它
  status TEXT,         -- running | succeeded | partial | failed | needs_number | interrupted | cancelled
  started_at, finished_at, error TEXT
)

task_files(
  id INTEGER PK,
  task_id INTEGER REFERENCES tasks(id),
  path TEXT, kind TEXT, role TEXT, part_index INT,
  follows_part INT, size INT, mtime,
  active BOOL, assigned_number TEXT, is_part BOOL,
  UNIQUE(task_id, path)
)

file_rows(
  task_id, task_run_id, step_run_id,
  kind TEXT, role TEXT, idx INT, src TEXT, dst TEXT,
  bytes INT, sha256 TEXT,
  action TEXT,   -- created | overwritten | skipped | deleted
  created_at
)

step_runs(
  id, idx INT, type TEXT, name TEXT, status TEXT,
  started_at, ms INT, summary_json TEXT, error TEXT,
  batch_id INTEGER REFERENCES batches(id),
  task_run_id INTEGER REFERENCES task_runs(id),
  CHECK ((batch_id IS NULL) <> (task_run_id IS NULL))
)

source_runs(
  id, step_run_id INTEGER REFERENCES step_runs(id),
  source_id TEXT, status TEXT, err TEXT, fields_json TEXT, ms INT
)
```

#### 索引名（原样，§10「索引与查询面」）

```sql
ux_task_files_active_path   UNIQUE(path) WHERE active=1
ux_task_files_task_path     UNIQUE(task_id, path)
ix_tasks_status             (status)
ix_tasks_batch              (batch_id)
ix_tasks_created            (created_at)
ix_task_runs_task           (task_id, run_no)
ix_step_runs_batch          (batch_id)
ix_step_runs_task_run       (task_run_id)
ix_source_runs_step_run     (step_run_id)
ix_source_runs_source       (source_id)
ix_file_rows_task_run       (task_run_id)
```

#### 与查询面 / 状态迁移 / 取消 / 重跑 / 水位线相关的关键句（原样摘）

- `**没有 part 列**：分片是文件级属性（task_files.part_index）`
- `**不存 stage 列**：阶段由 step_runs 派生（Q2），前端列表页一次 join 拿到「正在刮削」`
- `具名列只放查询/筛选要用的，字段值全在 metadata_json（加字段不迁移）`
- `**active 同步义务**（写进 /to-spec 的状态迁移表）：任务转终态（succeeded/partial/failed/cancelled）⇒ 该任务所有 task_files.active=0；queued/running/needs_number 期间保持 1`
- `run 结束时 tasks.status ← run.status，**例外**：run 记 interrupted 时任务被复位为 queued`
- `每张表只拥有自己真会出现的值（Q19）：任务 7 值、run 7 值（含 interrupted）、批次 5 值。queued 只属任务…interrupted 只属 run`
- `**列表页**：按 status / batch / 时间筛，按番号搜；详情页：任务行 + step_runs + source_runs + file_rows + 输出目录里的图；待补号页：needs_number 任务的 task_files 行。**分页形状归 #11**`
- `重跑 = 任务级后缀（parse 起，不重扫目录，#2），文件集合从 task_files 读`
- `每次重跑开一行 task_runs；step_runs/source_runs/file_rows 都挂这次 run；**历史保留**`
- `清理：按**最近一次 run** 的 file_rows 做 diff…内容相同（sha256 相同且文件仍在）则跳过写入`
- `重跑读 tasks.pipeline_id 对应的**当前**配置文件；文件不存在 ⇒ 任务 failed`
- `## 6. 崩溃恢复`：`UPDATE task_runs SET status='interrupted' … WHERE status='running';` + `非终态任务复位 queued`（上限 5，键名建议 `retry_limit`）
- `CREATE UNIQUE INDEX ux_task_files_active_path ON task_files(path) WHERE active = 1;`
- `**→ fog（新）「任务列表查询面 / REST API 契约」**：三视图与 CLI 共用的 HTTP 端点面未开票（旧仓 78 条 API 已划出范围）。`
- `**→ fog（新）「任务删除（第二批）」**：V1 明确不做删除，任务只增不减`

### 2.2 #15「并发模型」——结票评论

**一句话（原句）**：`V1 的并发模型是**三层、三个数、一把锁**…取消是一条 ctx 一路到底；**单副本**写进不变量。默认 **4 / 8 / 4**，只配在 config.yaml 一处。`

#### 取消 / 重跑 / 409（原样摘）

- `**用户取消任务**：引擎支持（与 SIGTERM 同一条 ctx 路径），任务落 cancelled（终态 ⇒ task_files.active=0 ⇒ 释放路径占用，照 #9）。入口（端点/按钮）归 #16/#11，本票只保证引擎侧语义存在。`
- `**重跑一个正在跑的任务**：**拒绝**（#16 面用 409）。重跑只对终态任务开放。`
- `**SIGTERM = 优雅中断**：停止从队列取新任务 → cancel 根 ctx…→ 等最多 **25s** → 仍在跑的 run 记 interrupted → 退出。`
- `**中断时的状态写必须在被取消的 ctx 之外**（context.WithoutCancel 或独立短超时 ctx）`
- `**取消/中断时已写盘、未入 file_rows 的完整产物**：**不专门清理**`
- `**槽位释放顺序**：run 结束事务（终态 + file_rows + 清理）**提交后**才释放任务槽`
- `**大文件与中断**（Q15）：output.Apply 在**文件之间**检查 ctx，**不在单个文件写入中间**中断…**取消时删掉正在写的 .tmp**`

#### 队列可见性 / 水位线 / 可观测（原样摘）

- `**队列 = tasks.status='queued'**（DB 是唯一真相，不额外建表）…进程内一条 FIFO 队列（按 tasks.created_at），**全局、跨批次**`
- `**可观测**：step_runs **不加列**…并发度只进日志与进程内计数；队列等待由 task_runs.started_at − tasks.created_at 算得出。`
- `「为什么一直 queued」：状态列 + 日志够用；**池占用是否上 API 面归 #16**。`
- `**批次级前缀（scan/filter/group）全局单飞**…tick 拿不到前缀槽 → TryLock 失败 → 跳过本轮 + 记日志，**水位线不推进**`
- `**入队两处**：任务创建时 + 启动时扫 queued（在复位事务之后）。同一任务 id 不重复入队。`
- `needs_number 任务不入队（它在等人）；用户填号后从 parse 起开新 run → 入队`
- `**SQLite 写入串行化**`：`WAL + busy_timeout(5000) + synchronous(NORMAL) + 单写连接 + 独立只读池`；读句柄 `SetMaxOpenConns(4)`（`前端任务列表查询不会被 run 结束的批量写挡住`）
- 配置键（原样）：`concurrency: {tasks: 4, requests: 8, images: 4}`

#### 状态码 / 端点相关（原样摘）

- `**→ #16「REST/HTTP API 契约」**：取消端点、重跑冲突 409、池占用是否暴露、任务列表的 queued 呈现。`
- `**→ #11「前端三视图原型」**：列表/详情显示 queued/running 与排队原因（日志），不新数据源。`
- 实现约束 §10 提到：`CLI mdc source test 走同一 Fetcher`（**本仓唯一被写下的 CLI 命令名**，出处 `#15 结票评论 §12.10`；另见 #7 结票评论「真站手动路径 = CLI `mdc source test`」）。

### 2.3 #10「监控与触发模型」——结票评论

**一句话（原句）**：`三个触发源（manual | monitor | webhook，webhook 值预留、V1 不开端点）**汇入一条接入路径**…**inotify 不实现**`。

#### 查询面 / 列名 / 水位线 / 监控行形状（原样摘）

- 表形状（原样）：
  `monitors(id, name, enabled, pipeline_id, interval_seconds, quiet_window_seconds, created_at, updated_at)`
  `+ monitor_dirs(monitor_id, path, kind ∈ {root, exclude}, watermark_at)`（`kind=exclude` 的行 `watermark_at` 为空）
  → `表形状现在定，**迁移随第二批实现落地**（V1 只建 #9 已定的 7 张 + goose 台账）`
- `**每根一条持久水位线（ctime）**：文件 ctime 晚于水位线才进视野；存量不刮（要刮走手动扫描）`
- `**推进**：一轮里 pending 集合清空且 walk 无错时，水位线 = 本轮 walk 开始时刻 − quiet_window…有未就绪文件或 walk 有错就不推进`
- `首次见到该根时水位线 = 当时（不是 0，否则第一次启用就扫全库）`
- `**监控是运行时状态**，落 DB、不落 config.yaml；CRUD 走 API/前端。`
- `**创建/更新时（400）**：pipeline id 必须存在且通过校验；exclude 必须是该监控某个根的真子目录…`
- `**0 个路径不进接入路径、不建批次**（400）；有输入但全 skip 也留痕。`
- `**空选择 → 400 不建批次**；创建必建批次（全 skip 也留痕，照 #9）。`
- `**触发只注入路径集合** {roots, exclude}，覆盖 pipeline 里 scan.with 的默认值`
- `roots 的元素可以是目录（递归）或文件（就取该文件）`
- `**预览与创建共用 scan→filter 前缀**（--until filter / dry-run）` —— **注意**：#11 结票评论 §8 已把它修正为 **`--until parse`**。
- `**批次**：一次触发一行（#9）。trigger_ref 取值：monitor = 被触发的根绝对路径；manual = 预览选定的根（无则空）；webhook = torrent hash。`
- `启动时对存量行再算一遍`：`悬空引用 → 该监控状态为「pipeline 缺失」，**停止轮询该监控、其余照跑**`；`该状态不落库，每次启动重算`
- `根**不要求此刻存在**（NFS 挂载会晚到）：轮询时逐 tick 记 warning，前端标「根不可用」。`
- `**inotify 不实现**…config 里没有 mode 键`
- §4 webhook（原样）：`起点照旧仓 ADR-0011：%F/%N/%I/%L → POST JSON + 可选 X-Webhook-Token + 分类白名单 + 不做就绪判定（信任 torrentFinished）+ .part/.!qB 过滤；trigger_ref = torrent hash。`
- §6 连带修正：`新开「REST/HTTP API 契约与任务列表查询面」子票（被 #11 阻塞：端点要服务的视图形状由它定）`。

### 2.4 附：被本票直接依赖的两处（不在题目三张票内，但事实相关）

**#11「前端三视图原型」结票评论 §9「交给 #16 的草案」**（命令：`gh issue view 11 --comments`）——原文照抄：

```
GET  /api/pipelines                          → [{id, name}]
POST /api/scan/preview  {pipeline, roots[], exclude[]}
                                             → {groups:[{primary, parts[], number, candidates[], marker, dir, size, res, occupied}]}
POST /api/tasks         {pipeline, paths[]}  → {batch_id, created, skipped[]}
GET  /api/tasks?status=&batch=&trigger=&number=&page=&per_page=
                                             → {total, page, per_page, items[]}
GET  /api/tasks/{id}                         → 任务行 + metadata(14 字段+Sources+Missing+Partial)
                                               + step_runs[] + source_runs[] + file_rows[]
POST /api/tasks/{id}/rerun                   （任务级后缀，从 parse 起）
POST /api/tasks/{id}/cancel
POST /api/tasks/rerun   {ids[]}              → [{id, ok, error}]（逐条）
POST /api/tasks/{id}/numbers {files:[{path, assigned_number, is_part}]}
                                             （待补号两处 UI 共用这一个写端点）
GET  /api/tasks/{id}/thumb?role=&idx=&w=
GET  /api/tasks/{id}/image?role=&idx=
```

- 同处原文：`**进度面（SSE 还是轮询）留给 #16**`、`鉴权由前置 authelia 承担，进程内不做`。
- 图片端点补充（`gh issue view 11 --comments` 第二条评论）：`GET /api/fs/dirs?path=/media → { path, dirs:[{ name, dir_count, file_count }] }`；`只在 browse_roots 白名单内可用：越界 400、不存在 404、只列目录不返回文件内容`。
- 同处修正原文：`**#9**：§10 里「分页形状归 #11」现在归位 —— 页码分页 + 只搜番号 + 批量只做重跑 + 批次不做独立视图。查询面本身（ix_tasks_status/ix_tasks_created/ix_tasks_batch）够用，**不需要新索引**。`
- 同处 `### 10. 资产与明确不做`：`原型里的假数据全部来自已结票的真实形状（7 状态、14 字段 + Sources/Missing、六档原因码、file_rows 形状、待补号两态）`

**#16 票面本体**（命令：`gh issue view 16`）——需收口项原样：`端点清单与命名、请求/响应载荷、分页/筛选/搜索的形状（#9 的索引已就位）、错误码与错误体、写操作一律 POST + JSON body、进度面（SSE 还是轮询）、鉴权（前置 authelia，进程内不做）、以及哪些面属 V1 哪些属第二批。旧仓 78 条 API 已划出范围（见地图 Out of scope），新面自己定。`
依赖：`blocked-by: 2017fighting/mdc-lite#11`（#11 已结，故解除）。

---

## 3. `prototype/frontend-views.html`（2318 行 / 150377 字节；基线 = 变体 A「运维台」）

文件内三个变体：A 运维台（`aShell`/`aBody`，1104–1600）、B 卡片墙（`bShell`，1605–1784）、C 流程看板（`cShell`，1785–1992）。基线 A。

### 3.1 假数据对象形状（键名层级，原样）

`prototype/frontend-views.html:676-880`

```js
var PIPELINES = [{id:'default', name:'默认流水线'}, {id:'vr', name:'VR 专用'}];        // :676
var ROOTS = ['/media', '/media/vr'];                                                    // :677
var EXCLUDES = ['/media/ignore'];                                                       // :678
var BROWSER_ROOTS = [{path:'/media', label:'媒体库'}];                                  // :679
var FS = { '/media':[['2024','12 个目录 · 386 个文件'], …], '/media/2024':[…], … };    // :680-688
                                                                  // 形状：{ "<绝对路径>": [[子目录名, "N 个目录 · M 个文件"], …] }
var TASKS = [                                                                           // :693-709
  {num, title, status, stage, batch, trigger, updated, sources, missing, files, out, hue, err?}
];
var SCAN_GROUPS = [                                                                     // :711-725
  {primary, number, conf, parts[], marker, dir, size, res, occupied, hue, cands?}
  // conf ∈ 'ok' | 'ambiguous' | 'duplicate_stem'
];
var SCAN_FILTERED = [ {p, r} ];                                                         // :726-730
var DETAILS = {                                                                         // :732-869  （键 = 番号）
  '<number>': {
    number, status, title, hue, out, batch, trigger, updated, parts, link, pipeline,
    fields: [ [字段名, 值文本, 胜出来源, isImage:0|1], … ],   // 14 项，顺序见下
    missing: [字段名…],
    steps:  [ [stepType, 'ok'|'failed'|'—', ms, summary文本], … ],   // 8 步
    srcs:   [ [sourceId, 'ok'|'failed'|'notfound', reasonCode|'', 贡献字段数, ms], … ],
    rows:   [ [kind, role, idx, src, dst, size文本, action], … ]
  }
};
var NUMBERS = [                                                                         // :871-880
  {task, kind, batch, dir, files:[ {p, cands[], part, primary} ]}
];
```

- `fields` 的 14 项固定顺序（`:738-751`）：`title, plot, fanart, extrafanarts, tags, releasedate, actors, poster, director, runtime, rating, series, studio, publisher`。
- `steps` 的 8 步固定顺序（`:754-763`）：`scan, filter, group, parse, scrape, images, nfo, output`。
- `srcs` 的 4 源（`:764`）：`javdb, dmm, javbus, fc2`。
- `rows` 的 kind/role 取值（`:765-770`）：kind ∈ `video|image|nfo`；role ∈ `—|poster|fanart|extrafanart`；action 假数据只出现 `hardlink` 与 `write`。
- `TASKS[].err`（`:706`）是失败原因第二行文本，只在 `failed` 那条上出现。
- 长标题 / 特殊字符夹具：`LONG_TITLE`（`:690`）、`PUNCT_TITLE`（`:691`，含 `<b>テスト</b> & 🎬`）。

标签映射（前端把后端码翻成中文的地方，原样）：

- `ST_LABEL`（`:982-983`）：`queued:排队中, running:运行中, succeeded:刮削成功, partial:部分成功, failed:处理失败, needs_number:待补号, cancelled:已取消`（**7 值**）
- `TRIGGER_LABEL`（`:984`）：`manual:手动触发, monitor:监控触发, webhook:Webhook`（**3 值**）
- `STAGE_LABEL`（`:985-999`）：`—, scan, filter, group, parse, scrape, scrape · javdb, scrape · dmm, scrape · javbus, scrape · fc2, images, nfo, output`
- `STEP_NAME`（`:1000-1003`）：8 步显示名
- `FIELD_LABEL`（`:1004-1009`）：14 字段显示名
- `KIND_LABEL`（`:1010`）：`video:视频文件, image:图片资源, nfo:NFO 元数据`
- `ROLE_LABEL`（`:1011`）：`poster:竖版海报, fanart:横版背景, extrafanart:剧照, —:—`
- `ACTION_LABEL`（`:1012`）：`hardlink:创建硬链接, copy:文件复制, move:移动文件, write:文件写入`
- `LINK_LABEL`（`:1013`）：`hardlink, copy, move`
- `reasonLabel`（`:1020-1023`）六档原因码：`egress_unavailable:出口不可用, timeout:超时, blocked:被拦, cooldown:冷却中, http_5xx:服务端 5xx, parse:解析失败`

### 3.2 各视图实际渲染的字段 / 筛选控件 / 动作按钮

#### (a) 扫描页 —— `aScan()` `:1157-1353`

- **表单/控件**：
  - 面包屑 + 手输路径框 `#scan-path-in` + 「切换目录」（`:1182-1184`）
  - 主按钮「开始扫描该目录」`data-act="scan-run"`（`:1187`）
  - 预设 chips `PRESETS`（`:1192-1199`）
  - 「收起/展开子目录」`data-act="toggle-explorer"`（`:1201`）
  - 内联子目录浏览器表（`:1218-1244`）：列 = 子目录名称 / 包含内容 / 容器内路径 / 目录操作；每行动作 = `nav-dir`、`toggle-root`（`+ 加入扫描` / `✓ 移出扫描`）、`toggle-exclude`（`+ 排除` / `取消排除`）
  - 根目录清单 `S.roots`（`:1250-1266`）：每项 `data-chip="root"`；排除清单 `S.excludes`（`:1267-1280`）：每项 `data-chip="exclude"`
  - 底部 `stickybar`：`data-act="scan-all"`（全选）+ `data-act="create-tasks"`（立即建任务，`:1349-1350`）
- **候选表格列**（`:1313-1316`）：`选择 / 类型 / 候选文件（按智能分组展示）/ 解析番号 / 分组识别依据 / 文件大小 / 视频分辨率 / 路径占用`
  - 行内渲染：主文件 `g.primary` 带 checkbox `data-g="<gi>"`；分片行写「随主文件入组」；番号列 `g.number` 或 `g.cands[]` chips；`g.marker`；`g.size`；`g.res`；`g.occupied` → 「已有活跃任务占用 / 空闲可用」
- **被过滤摘要** `filteredBlock()`（`:1355-1361`）：`SCAN_FILTERED` 的 `{p, r}`
- **加载态**（`:1286-1303`）进度条文案：`目录扫描 41ms / 规则过滤 2ms / 智能分组 运行中… / 番号解析 待执行`，且写 `当前预览截止于分组阶段`（与 #11 裁决的 `--until parse` 不一致——原型这句是旧文本）。

#### (b) 任务列表 —— `aTasks()` `:1362-1453`

- **筛选控件**（`:1376-1396`）：
  - 状态 chips（`aria-pressed`）：`all (137) / running (1) / queued (3) / succeeded (96) / partial (21) / failed (12) / needs_number (2) / cancelled (2)`（`:1378-1380`）—— 7 状态 + 「全部」
  - 搜索框 `#f-q`，label 文本 `搜番号`，placeholder `输入番号快速检索 (例如 SSIS-001)...`（`:1385-1386`）
  - 下拉 `#f-batch`（`全部批次 / b-1043 / b-1042`）、`#f-trig`（`全部来源 / 手动触发 / 监控触发`）、`#f-time`（`全部时间 / 最近 24 小时 / 最近 7 天`）（`:1389-1394`）
- **表格列**（`:1397-1400`）：`选择 / 海报 / 番号与标题 / 状态 / 执行阶段 / 来源 / 所属批次 / 触发 / 更新时间 / 快捷操作`
  - 行内消费字段：`t.num`、`t.files`、`t.title`、`t.status`、`t.stage`（经 `STAGE_LABEL`）、`t.sources`、`t.missing`、`t.batch`、`t.trigger`、`t.updated`、`t.err`（失败原因第二行 `colspan=8` 的 `notice-error`）
- **动作按钮**：行内 `详情`（`data-detail`）+ `重跑`（`data-rerun`）；选中后浮动条 `data-clear-rows`（清空选择）+ `data-act="batch-rerun"`（批量重跑，文案 `批量重跑 = 逐条任务级后缀重跑，返回逐条结果`）（`:1426-1441`）
- **分页**（`:1443-1451`）：`显示 1–7 / 137` + 上一页/页码 1..7/下一页（`data-act="page-prev"|"page-num"|"page-next"`）+ `每页` 下拉 `20/50/100`
- **副标题**（`:1375`）：`共 137 条任务 · 每页 20 条 · 默认按创建时间倒序`
- **空态**（`:1369-1373`）：`没有符合条件的任务` + `data-act="clear-filter"`（清空筛选）

#### (c) 任务详情 —— `aDetail()` `:1454-1542`

- **头部**（`:1460-1462`）：`d.number`、`d.title`、`d.status`、`d.batch`、`d.trigger`、`d.updated`、`d.pipeline`
- **图区**（`:1463-1485`）：海报 `zoomBtn(...'poster',400,600)` + 徽标 `✂ 封套右侧裁切 2:3`；strip = `fanart` + `剧照 1..4`；动作按钮：`data-rerun`（`从解析步骤起重跑`）、`data-act="cancel-task"`（取消任务）、`data-copy="<out>"`（复制落盘路径）；`d.link`（`LINK_LABEL`）、`d.parts`、`d.out`
- **缺失字段 banner**（`:1480-1484`）：`d.missing` → `缺失字段 N 个：… — 任务按部分成功 (partial) 收尾`
- **四个 tab**（`:1486`）：`overview=字段与来源`、`steps=步骤留痕`、`sources=源留痕`、`files=落盘台账`
  1. `overview`（`:1493-1501`）：表头 `元数据字段 / 刮削值 / 胜出来源`；行 = `d.fields` 的 `[名, 值, 来源, 图片标记]`；值 `'—'` 即判定缺失（`miss` 行）
  2. `steps`（`:1502-1511`）：列 `流水线步骤 / 执行状态 / 耗时 / 执行摘要`；行 = `d.steps` 的 `[type, status, ms, summary]`；`stepState()` 只认 `ok→完成 / failed→失败 / 其他→待跑`
  3. `sources`（`:1512-1525`）：列 `刮削源站点 / 请求状态 / 错误原因码 / 贡献字段数 / 耗时`；行 = `d.srcs` 的 `[id, status, reason, 字段数, ms]`；`ok→请求成功`、`failed→请求失败`、其余→`未收录 / 404`；`reason` 走 `reasonLabel` 六档
  4. `files`（`:1526-1537`）：列 `产物类型 / 角色 / 分片序号 / 来源 / 落盘目标相对路径 / 文件大小 / 操作动作`；行 = `d.rows` 的 `[kind, role, idx, src, dst, size, action]`
- **内嵌填号面板**：`if(d.status==='needs_number') h += fillPanel(S.detail);`（`:1540`）
- **空态**（`:1458-1459`）：`任务不存在`

#### (d) 填号面板 `fillPanel(taskNum)` `:1544-1569`（待补号页与详情页共用）

- 说明文案（`:1550-1552`）：`提交确认后 ⇒ 任务状态复位为「排队中」 ⇒ 新运行实例将直接从「番号解析」步骤继续执行…在人工补号完成前，相关文件路径将保持被占用锁定`
- 每行：`f.p` 文件名 + `f.primary ? '主文件 primary' : '分片'` + 候选 chips（`f.cands[]`，`data-num="<key>|<cand>"`）+ 手输 `input.numin` + `data-ispart` 勾选框（`同一作品的分片文件`）
- 底部动作：`data-act="skip-cancel"`（`跳过（取消任务）`）+ `data-act="submit-rerun"`（`确认番号并重跑`）；提示 `最终任务主番号将以主文件 (primary) 输入的番号为准`

#### (e) 待补号队列 —— `aNumbers()` `:1570-1600`

- 副标题（`:1573-1574`）：`算法解析存在歧义、或同主干无分片标记的文件集中在此；确认番号并提交后 ⇒ 任务将从「番号解析」步骤断点重跑`
- 每个卡片：`n.task`、`n.kind`（`ambiguous→解析出多个候选番号（分歧）` / `duplicate_stem→主干文件名相同但均无分片标记`）、`n.batch`、`n.dir`
- 表格列（`:1582-1583`）：`待处理文件 / 算法候选番号 / 最终确认番号 / 分片关系标记`
- 动作同 (d)：`skip-cancel` + `submit-rerun`
- 空态（`:1571-1572`）：`当前没有待补号的任务`

#### (f) 目录选择器弹窗 `pickHTML()` `:1997-2028`

- 对话框 `role="dialog" aria-modal="true"`；标题随 `S.picker.mode`：`加入排除` / `加入待扫描目录`（`（容器内）`）
- 面包屑（挂载点 `BROWSER_ROOTS[0].label` + 逐级 `data-pk="go"`）+ 手输框 `#pkpath` + `data-pk="jump"`（前往）
- 目录列表项：`data-pk="go"`，每项渲染 `d[0]`（目录名）+ `d[1]`（`N 个目录 · M 个文件` 文本）
- 底部：`data-pk="close"`（取消）+ `data-pk="apply" data-pk-mode="root"`（加入待扫描目录）+ `data-pk="apply" data-pk-mode="exclude"`（加入排除）
- 越界/校验错误就地显示 `S.picker.err`（`:2013`）

#### (g) 灯箱 `lightboxHTML()` `:2029-2035`

- `role="dialog" aria-modal="true" aria-label="大图预览"` + `<img src=S.lb.src alt=S.lb.alt>` + `data-lb-close="1"`（关闭）
- 触发：`zoomBtn()` 渲染 `<button data-zoom="<label>">`（`:977-981`）

#### (h) 全局状态 `S` `:1070-1077`

```js
var S = {
  variant:'A', view:'scan', state:'normal', detail:'SSIS-001',
  picked:{}, nums:{}, rows:{}, tab:'overview', page:1,
  roots:['/media'], excludes:EXCLUDES.slice(),
  navDir:'/media', explorerOpen:true,
  picker:{open:false, path:'/media', mode:'root', err:''},
  lb:null, focusBack:null
};
```

- 视图枚举 `VIEWS`（`:1078`）：`scan / tasks / detail / numbers`（**4 个 view，不是 3 个**；`numbers` 是独立视图）
- 状态枚举 `STATES`（`:1079`）：`normal / loading / empty / error`（四态组件）
- 原型控制台顶栏写死 `SQLite · 单副本 · 端口 9208`（`:1114`）；侧栏死链占位 `批次记录 / 步骤运行 / 站点请求 / 落盘台账` 与 `流水线配置 / NFO 模板`（`:1123-1126`）—— 均为 `dead` 样式，未实现。
- 前端 URL 状态：`?variant=&view=&state=`（`readURL` `:1083-1088`，`writeURL` `:1090-1095`）—— 原型把视图状态放 URL 串。

---

## 4. `~/clone/open-mdcng`（旧仓，仅参考）

### 4.1 ADR-0011 原文摘录

文件：`/home/zhao/clone/open-mdcng/docs/adr/0011-inbound-trigger-unified-ingest.md`
标题：`# 触发接入：单一接入路径 + 存储层强制去重`，`## Status: accepted`

关键段（原样）：

- Context：`qBittorrent 的「运行外部程序」是**纯字符串替换、不经 shell、不读响应、失败不重试**（application.cpp: runExternalProgram），且会把整条命令行写进它自己的日志；%F 是内容路径，在 strip-root 模式下等于整个下载目录（torrent.h:150-151）。`
- Decision：
  - `**入站触发统一汇入一条接入路径**（枚举 → 分组 → 去重闸 → 写库），三种触发源不各写一套。`
  - `**闸一（非终态路径已存在 → 跳过计数）由存储层强制**：task_file 持一个非终态标志列，其上建部分唯一索引（UNIQUE (source_path) WHERE is_terminal = false）。插入冲突即计入该批次 skip。闸二（终态已含该路径 → 跳过并提示可重试）仍是查询，不入约束。`
  - `**每次触发都建批次**（全 skip 也留痕），批次带 trigger 枚举 + trigger_ref 文本。`
  - `**写操作端点收 POST + JSON body**；查询串仅作手测次要入口。**鉴权为可选共享 token**，只认 X-Webhook-Token 请求头，enabled=false 时端点 404。`
  - `**webhook 路径不做文件就绪判定**，信任 qB 的 torrentFinished；只过滤 .!qB/.part 与扩展名白名单。**出站通知不做**`
- Considered Options（放弃项）：`**查询串承载路径**：qB 不转义，&/#/空格会破坏参数——本域目录名恰好高频含这些字符。`；`**token 走查询串**：qB 会把它写进自己的日志，外加 shell history 与反代访问日志，三个泄露面。`
- Consequences：`**状态迁移必须同步 task_file 的非终态标志列**（任务转终态 ⇒ 置 is_terminal = true）`；`接入路径只有一份，08 的集成测试不变量「预览与创建走同一条判定路径」自然成立`。

旧仓对应实现（作为对照，非本仓决策）：
- `api/webhook.go:39-63` payload 结构体：`{Path %F json:"path", Name %N json:"name", Hash %I json:"hash", Category %L json:"category"}`；响应 `webhookResponse{JobID *int64 json:"job_id", Reason string json:"reason,omitempty"}`，**202 Accepted**（`api/webhook.go:57-63, 99, 147`）。
- 分类白名单未命中 → `writeJSON(w, http.StatusAccepted, webhookResponse{Reason: "category filtered"})`（`api/webhook.go:96-101`）。
- 未授权 → `401 "unauthorized"`；功能关闭 → `404 "not_found"`（`api/webhook.go:73-79`）。

### 4.2 HTTP 层有没有统一的错误体 / 响应包裹约定？

**有。**（命令：`grep -rn "http.Error|writeJSON|writeError|jsonError|errorResponse" api/ internal/`）

- 统一错误体定义在 `/home/zhao/clone/open-mdcng/internal/apierr/apierr.go`，**唯一的错误响应形状**：

```go
// apierr.go:15-29
// Envelope is the response body for every error from this server:
//
//	{"error": {"code": "not_found", "message": "task 7 does not exist"}}
//
// Code is a stable, machine-readable token the interface maps to Chinese
// (web/src/copy.ts). Message is English prose for logs and issue reports.
type Envelope struct { Error Body `json:"error"` }
type Body struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
```

- 写入口：`func Write(w, status, code, message string)`（`internal/apierr.go:32-39`），`Content-Type: application/json; charset=utf-8`。
- 设计理由（原样，`apierr.go:1-7`）：`It is shared because two packages write it: api for the REST namespace, and web for the shell's own refusals…Defining the shape twice let the two encoders drift — one indented, one not — while the interface parsed a single assumed format.`
- `api/errors.go:9-14` 是薄包装：`// writeError renders an error response in the shared envelope. The shape and its rationale live in internal/apierr…`
- **成功响应没有包裹**：`writeJSON` 直接编码业务结构体（`api/server.go:341-354`，带 `SetIndent("", "  ")`）。
- 一条显式的**空数组约定**：`api/wire.go:1-26` —— `A JSON array is never null: an empty list is [], so a client can iterate it without a guard.`；理由原文：`A nil slice marshals as null, and the interface treats these fields as lists — so null throws Cannot read properties of null (reading 'length') during React's render, the tree unmounts, and the page goes blank with nothing in the server's log.`
- 旧仓错误码用 snake_case 令牌（实测出现）：`bad_request`, `not_found`, `internal_error`, `unauthorized`, `bad_webhook_request`, `source_unavailable`, `number_not_expected`, `invalid_config`, `config_unavailable`, `plugin_testing_unavailable`, `not_a_directory`（`grep -rn writeError api/`）。
- **409 的用法（旧仓）**：`api/taskretry.go:196-213` `statusForRetry()` —— 拒绝的重跑**不是服务端错误**：`409 says that without pretending something broke, so the client shows the reason instead of treating it as a transport failure.`；`not_found→404`、`bad_request→400`、其余→`409`。`api/sourcetest.go:118` 也用 `409 source_unavailable`。
- **旧仓没有任何 cancel 端点**（命令：`grep -rni "cancel" api/*.go` → 零命中），佐证 #15「取消入口归 #16」。
- 旧仓路由清单（命令：`grep -n "mux.Handle" api/server.go`）——本仓**不继承**（地图 Out of scope：旧仓 78 条 API 划出范围），仅作命名风格参照：
  `/api/health`, `/api/scan/preview`, `/api/scan/create`, `/api/fs/list`, `/api/tasks`, `/api/tasks/{id}` (GET/DELETE), `/api/tasks/{id}/number`, `/api/tasks/{id}/number/preview`, `/api/tasks/{id}/retry`, `/api/tasks/{id}/attempt/reset`, `/api/tasks/{id}/override` (GET/PUT), `/api/tasks/retry`, `/api/batches/{id}`, `/api/hooks/qbittorrent` (GET/POST), `/api/config`, `/api/config/reload`, `/api/config/naming/*`, `/api/config/sources*`, `/api/sources/status`, `/api/sources/health`。
  路由注释（`api/server.go:143-145`）：`Method dispatch happens inside each path so a wrong method gets a JSON body the interface can map, rather than the standard library's plain text.`
- 旧仓 ADR-0019（`docs/adr/0019-task-list-query-surface.md`，标题 `# 任务列表查询面：可移植的搜索、白名单排序、键集分页`）——**与本票查询面直接同题**，关键事实：
  - 搜索字段：`q 对 task.number、task.prefix、task_file.source_path、task_file.target_path 做子串匹配`；`LIKE` + Go 侧转义 `%`/`_`/`\`（`ESCAPE '\'`）+ `LOWER()`。
  - 排序白名单：`sort + dir，默认 updated_at / desc`，白名单 = `updated_at、number、status、prefix`。
  - `**LIMIT/OFFSET 分页**：**明确放弃**，且理由是正确性而非性能：列表每秒轮询、新任务插在**顶部**（id DESC），用户翻到第 3 页时顶部插入一条就会把第 2 页的首行挤过去——**漏行**。`
  - 采用键集分页（游标），`游标不进 URL`。
  - 硬义务：`计数与列表共用 taskFilterClause`；`排序键必须加 id 作末位 tie-breaker`；`NULL 排序必须写成 ORDER BY (x IS NULL) ASC, x ASC`。
  - 视图状态：`q / sort / dir / group / batch 全部在 URL`。
  - 空结果：`必须区分「确实没有」与「有，但不在当前分组」`。

---

## 5. `PRODUCT.md`（仓库根，82 行）

命令：`grep -n -i -E "api|端点|endpoint|http|进度|前端|接口|端口|9208|SQLite" PRODUCT.md` + 全文阅读。

### 5.1 与 API / 端点 / 进度 / 前端有关的约束（逐条带行号）

| 行号 | 原文 | 约束类型 |
|---|---|---|
| 7 | `Go 单二进制 + 内嵌 Vite / React 前端；无第三方 UI 组件库（手写组件配合 Tailwind v4 设计令牌单源收敛）。状态存储采用内嵌纯 Go SQLite (`modernc.org/sqlite` + `goose`)。` | 栈 |
| 11 | `Home-Lab / NAS 自建媒体库用户（单用户运维场景）。…` | 用户 |
| 13 | `提供一套现代、轻量、高确定性的媒体刮削与流水线归档控制台。` | 目的 |
| 17–22 | Positioning 五条（单二进制无外部运行时依赖 / 透明强类型流水线 / 前置智能分组 / 高密度专业运维台 / 逐字段胜出源追溯、六档原因码留痕、落盘台账明细、容器内路径选择白名单） | 定位 |
| 26 | `**运行环境**：部署于 Docker / Kubernetes 单容器，挂载 /config（SQLite 与配置，禁止 NFS 卷）与 /media（NFS/本地存储）。**端口 9208**，前置可接入 Authelia SSO（**应用内不做用户鉴权**）。` | **端口 9208 / 无进程内鉴权** |
| 27 | `**网络拓扑**：网络出口依赖路由器透明代理与域名规则分流…应用层不做代理池与出口探测，仅内置轻量域名限速与退避。` | 出口 |
| 28 | `**浏览场景**：现代桌面宽屏（主要工作台，高密度多列）以及移动端（应急查看与补号）。` | 前端 |
| 29 | `**下游协同**：输出规整的 {{ number }}/ 目录、-cd{{ part }} 视频、标准化图片（poster 2:3、fanart 16:9）与规范的 movie.nfo，供 Jellyfin/Emby 自动索引。` | 输出 |
| 34–35 | `1. **扫描预览与勾选建任务**：容器内目录安全选择器（基于 browse_roots 白名单，默认 ["/media"]，支持面包屑、路径校验）。预览执行到 --until parse，表格展示候选文件、解析番号与分片分组，按组勾选建任务。` | **预览 = `--until parse`；勾选以组为单位** |
| 37–40 | `2. **任务列表管理**：状态芯片筛选（7 种确定状态：queued, running, succeeded, partial, failed, needs_number, cancelled）。批次/触发源筛选，**按番号单字段即时搜索，服务端页码分页（20/50/100）**。失败原因第二行直观露出，支持**批量重跑与单任务取消**。` | **查询面直接约束：状态筛选 + 批次/触发源筛选 + 只搜番号 + 服务端页码分页 20/50/100 + 批量重跑 + 单任务取消** |
| 42–46 | `3. **任务详情与追溯（四标签页）**：字段与来源（14 项源级字段…胜出源标注 javdb/dmm/javbus/fc2 及缺失项标记）；步骤留痕（各 step_run 的耗时、输入输出摘要与产物 Plan）；源留痕（逐源请求耗时、状态及六档结构化原因码 egress_unavailable, timeout, blocked, cooldown, http_5xx, parse）；落盘台账（目标路径、哈希、尺寸与动作记录，支持路径一键复制）；媒体灯箱` | 详情面 4 tab |
| 47–50 | `4. **待补号闭环（Needs Number）**：侧边栏独立视图 + 详情页内嵌面板双入口。支持逐文件输入番号、指定「同作品分片」、提交从 parse 步骤重跑或跳过（**取消任务释放文件占用**）。` | 待补号 |
| 52–56 | 严格约束：`**零外部 UI 库**`；`**单文件/内嵌构建**：前端产物内嵌于 Go 二进制；原型 frontend-views.html 为单文件`；`**非功能约束**：无用户鉴权逻辑；**不提供运行时日志查看器**（以结构化 Step/Source runs 替代）；视频不抽帧。` | **无日志查看器端点；不抽帧** |
| 60–61 | Evidence：`本仓 README.md 与 Issue #1（主链规格全图）、Issue #2～#16 决议文档`；`prototype/frontend-views.html … prototype/audit-frontend-views.md` | 证据 |
| 65–71 | Product Principles 四条（运维第一 / 求真留痕 / 自洽收敛 / 容错确定：`支持幂等重跑与资源状态自动复位`） | 原则 |
| 75–77 | Accessibility：`键盘可达性…对话框与灯箱具备 Esc 退出和焦点陷阱管理`；`状态多重编码：7 种状态色采用「形状/点 + 文字 + 色彩」多重编码`；`移动端断点适配…coarse 触控下 ≥44px` | 无障碍 |

**`PRODUCT.md` 里没有的**：端点路径、HTTP 方法、错误码/错误体、鉴权头、SSE/轮询、请求/响应字段名。全部只到「能力面」为止。

---

## 6. 一页速查（供摘要用）

### 6.1 分页 / 筛选 / 搜索能落到哪些列

| 查询面 | 可落列（表.列） | 已有索引 | 出处 |
|---|---|---|---|
| 状态筛选（7 值） | `tasks.status` | `ix_tasks_status` | #9 §10；`PRODUCT.md:38` |
| 批次筛选 | `tasks.batch_id` | `ix_tasks_batch` | #9 §10；`PRODUCT.md:38` |
| 触发源筛选 | `batches.trigger`（`manual\|monitor\|webhook`）—— **注意：`tasks` 表没有 trigger 列** | **无索引** | #9 §2 batches；`PRODUCT.md:38`；原型 `TASKS[].trigger` |
| 时间筛选 / 默认排序 | `tasks.created_at`（原型文案 `默认按创建时间倒序`）、`tasks.updated_at` | `ix_tasks_created` | #9 §2/§10；原型 `:1375` |
| 番号搜索（单字段） | `tasks.number` | **无独立索引**（#11 明确「不需要新索引」） | #11 §8；`PRODUCT.md:38` |
| 失败原因行 | `tasks.last_error` | — | #9 §2；原型 `TASKS[].err` |
| 阶段（「正在刮削」） | 派生：该任务最新一条 `step_runs.type`（**无 stage 列**） | `ix_step_runs_task_run` | #9 §2/§3 |
| 分页 | 页码 + `20/50/100`（#11 裁决）；**旧仓 ADR-0019 是键集游标** | — | `PRODUCT.md:38`；#11 §8；旧仓 ADR-0019 |
| 详情组装 | `tasks` + `step_runs` + `source_runs` + `file_rows` + 输出目录里的图 | `ix_step_runs_task_run` / `ix_source_runs_step_run` / `ix_file_rows_task_run` | #9 §10；#11 §9 |
| 待补号 | `needs_number` 任务的 `task_files` 行（`assigned_number`, `is_part`） | `ux_task_files_task_path` | #9 §4/§10 |
| 监控 CRUD（第二批） | `monitors` / `monitor_dirs`（含 `watermark_at`） | — | #10 §2.1 |

### 6.2 cancel / rerun 的状态码语义

- **cancel**：引擎侧语义已定（与 SIGTERM 同一 ctx 路径；任务落 `cancelled` 终态 ⇒ `task_files.active=0` 释放路径占用）；**端点形状与状态码归 #16**（#15 §6 原文：`入口（端点/按钮）归 #16/#11，本票只保证引擎侧语义存在`）。**旧仓没有任何 cancel 端点**（`grep -rni cancel api/*.go` 零命中）。
- **rerun**：`重跑一个正在跑的任务：**拒绝**（#16 面用 409）。重跑只对终态任务开放。`（#15 §6）。旧仓同类语义实现为 `statusForRetry()`：`404`=任务不存在、`400`=请求坏、**其余一律 `409`**（`api/taskretry.go:196-213`）。
- **重跑范围**：任务级后缀（`parse` 起），不重扫目录；每次开新 `task_runs` 行；按最近一次 run 的 `file_rows` diff 清理；读 `tasks.pipeline_id` 对应的**当前**配置文件（#9 §5）。
- **待补号「跳过」= 取消任务**（转 `cancelled`，`active=0` 释放占用）（#11 §5；`PRODUCT.md:50`）。
- **0 路径 / 空选择 / 越界**：`400`（#10 §1/§3、#11 补充决议）；**webhook 功能关闭 → 404**、token 不匹配 → 401、分类白名单未命中 → **202 + 空 job_id**（旧仓 ADR-0011 + `api/webhook.go:73-101`）。

### 6.3 UI 实际消费哪些字段

- **任务列表行**：`num, title, status, stage, batch, trigger, updated, sources, missing, files, out, hue, err?`（`frontend-views.html:693-709`）
- **扫描候选组**：`primary, number, conf, parts[], marker, dir, size, res, occupied, hue, cands?`（`:711-725`）
- **详情**：`number, status, title, hue, out, batch, trigger, updated, parts, link, pipeline` + `fields[14×(名,值,来源,isImage)]` + `missing[]` + `steps[8×(type,status,ms,summary)]` + `srcs[(id,status,reason,字段数,ms)]` + `rows[(kind,role,idx,src,dst,size,action)]`（`:732-869`）
- **待补号**：`{task, kind, batch, dir, files:[{p, cands[], part, primary}]}`（`:871-880`）
- **目录选择器**：`{path, dirs:[{name, dir_count, file_count}]}`（`FS` `:680-688` + #11 补充决议的 `GET /api/fs/dirs` 形状）
- 前端把状态/触发/步骤/字段/原因码全部走**本地映射表**翻译（`ST_LABEL`/`TRIGGER_LABEL`/`STAGE_LABEL`/`STEP_NAME`/`FIELD_LABEL`/`KIND_LABEL`/`ROLE_LABEL`/`ACTION_LABEL`/`LINK_LABEL`/`reasonLabel`，`:982-1023`）——**载荷里传的是机器码，不是中文**。
