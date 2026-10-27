# GLOSSARY

mdc-lite 的领域词汇表。写代码、起 issue、写测试名时用这里的词，不要漂到同义词上。
机器名（键名、枚举值、表列、码串）一律照抄，不要改写拼法。

## 流水线与步骤

- **流水线（pipeline）** —— 一份配置文件声明的一条完整处理链；`id` = 文件名字干（`main.yaml` → `main`），引用一律用 id。一个配置文件 = 一条完整流水线，不做层级合并。
- **步骤（step）** —— 流水线里的一个环节。V1 八类：`scan` / `filter` / `group` / `parse` / `scrape` / `images` / `nfo` / `output`；第二批预留 `translate` / `normalize` / `refresh`。步骤元数据层只有四个键：`type` / `with` / `name` / `on_error`。
- **批次（batch）** —— 一次触发一行（`batches` 表），扇出 N 个任务；全 skip 也留痕。
- **扇出点（fanout）** —— `group` 之后：`scan`/`filter`/`group` 是**批次级**（每触发一次），`parse` 起是**任务级**（每分组一次）。
- **任务（task）** —— 一个分组一行（`tasks` 表）；**调度单位 = 取消单位 = 留痕单位 = 重跑单位**。
- **任务运行（task run）** —— 任务的一次执行（`task_runs` 表）；重跑开新行，`run_no` 自增 1 起。
- **步骤运行（step run）** —— 每步每次运行一条摘要（`step_runs` 表），`summary_json` 是摘要投影（计数 + 前 N 条），不是产物全量。
- **源运行（source run）** —— `scrape` 里每个源一条留痕（`source_runs` 表），含三态与原因码。
- **预览（preview）** —— 跑到 `--until parse` 的一次扫描；异步 202 + 轮询，状态只在内存、不落 DB。
- **dry-run** —— 跑完整链但只出 `Plan`，不写盘、不落库。
- **落盘（Plan / Apply）** —— 副作用步骤（`images`/`nfo`/`output`）只产出 `Plan{Writes []PlannedWrite}`，由引擎统一 `Apply`；`output` 是**唯一写盘出口**。

## 分组与番号

- **候选（candidate）** —— 通过 `filter` 规则、进入处理集合的文件（`Candidates`）。
- **主干（stem）** —— 文件名剥掉噪声（sanitize / 广告域 / 技术 token / 分辨率 / 旗标 / 字幕尾标）后的文本；分组的判据就长在主干上。
- **分片标记（part marker）** —— 判定「这是同一作品的分片」的正面证据。两档：**确定标记**（`cd\d`/`disc\d`/`part\d`/`vol\d`/带分隔的 `-A/-B/-D`，无条件生效）与**条件标记**（尾随 1-2 位数字、粘连字母如 `9ofje121A`，需同目录兄弟文件同意）。
- **分片（part）** —— 同一作品的一个视频片；落盘 `-cd{n}`。
- **primary** —— 组内分片序号最小的文件；无标记文件视为第 1 片。primary 定番号、定目录名。
- **伴随文件（companion）** —— 与视频同组但不参与分片证据的文件（字幕、图片）；字幕跟随对应视频改名。
- **重复版本（duplicate stem）** —— 主干相同而两侧都没有生效分片标记（`ABC-123.mp4` + `ABC-123-U.mp4`）→ 交人判定，**不自动合成一组**。
- **待补号（needs_number）** —— 任务状态之一：番号解析不出（`no_candidate`）、歧义（`ambiguous`）或重复版本（`duplicate_stem`）时等人填号。
- **番号（number）** —— `Number{Raw, Normalized, Family}`；`Family` 是规则标签（`fc2` / `date-code` / `dmm-content-id` / `generic`）。
- **归一化（normalization）** —— 大写、`_`/空格 → `-`、粘连字母数字插连字符、尾号补零到 3 位（只补不截）、去首尾非字母数字、全角 → 半角；手填号与文件名走同一套。FC2 规范形保留 `FC2-PPV-3336019`。
- **包含裁决** —— 解析的求值顺序：全匹配收候选，一条候选的 raw 被另一条更长的 raw 包含则出局；不是 first-match-wins。

## 字段与来源

- **字段（field）** —— V1 **14 个源级字段**：`title` / `plot` / `fanart` / `extrafanarts` / `tags` / `releasedate` / `actors` / `poster` / `director` / `runtime` / `rating` / `series` / `studio` / `publisher`；外加内置 `number`。字段名即模板变量名。
- **必选字段（required fields）** —— 程序强制门禁：`output.with.required_fields`，默认 `[title, fanart]`；`fanart` 按**字节到手**判。
- **元数据（metadata）** —— 14 个源级字段 + `Sources` / `Missing` / `Partial`；`Metadata.Derived`（`TitleCN`/`PlotCN`/`TagsCN`/`ActorsCN`/`PosterCrop`）由第二批写，**调度器在类型上够不着**。
- **胜出源（Sources）** —— 逐字段记下是谁给的（`{title: javdb}`）。
- **缺字段（Missing）** —— fallback 用尽后仍缺的源级字段；`Partial = len(Missing) > 0`。
- **部分成功（partial）** —— 任务状态：跑完但有缺口。

## 源与网络

- **源（source）** —— 内置 Go 刮削器；两阶段接口 `Scrape(ctx, Number) → SourceResult` + `FetchImage(ctx, ImageRef) → []byte`；首批 `javdb` / `javbus` / `fc2` / `dmm`。
- **三态（three states）** —— `ok` / `notfound` / `failed`（由 `(err, Found)` 判）；`notfound` 用**正向标记**判。
- **原因码（reason code）** —— `failed` 的六档：`egress_unavailable` / `timeout` / `blocked` / `cooldown` / `http_5xx` / `parse`；落 `source_runs.reason`，**不进 API 错误体**。
- **波次（wave）** —— 调度的一轮：每个未满足字段的当前首选源去重后并发跑；无缺口即停。
- **字段级 fallback** —— 缺字段 = 该源在该字段无贡献 → 拉下一个源；`priority[field]` 是**完整列表替换**全局序。
- **接缝（Fetcher）** —— 唯一传输接缝；元数据与图片共用代理、限速、冷却、重试、取消。
- **可注册域（registrable domain / eTLD+1）** —— 限速桶与冷却的键；`pics.dmm.co.jp` 与 `www.dmm.co.jp` 同桶。
- **冷却（cooldown）** —— 403/429/503 后该域**快速失败**（默认 5 min，只在内存），不排队。
- **镜像域（mirror domain）** —— 源的有序域名列表；失败切换与重试**共享同一预算**。
- **出口（egress）** —— 出网路径；不是应用的事（用户侧路由器透明分流），应用只把没打通如实报成 `egress_unavailable`。
- **图片角色（Role）** —— `poster` / `fanart` / `extrafanart`（+ `Index`，同 Role 内 0 起）。
- **ImageRef / ImageBlob** —— `ImageRef` 是「地址 + 取它要带的头」（`{Role, Index, URL, Headers}`）；`ImageBlob` 是字节到手（`{…ImageRef, Source, Bytes, Ext}`）。
- **魔数嗅探（magic bytes）** —— 引擎按 `FF D8` / `89 50` 判扩展名，**不信 `Content-Type`**；解码归源（如 javdb 的 `key ‖ (jpeg XOR key)`）。

## 输出与媒体服务器

- **落盘台账（file_rows）** —— 输出产物的持久记录：`{task_id, task_run_id, step_run_id, kind, role, idx, src, dst, bytes, sha256, action, created_at}`；`action ∈ {created, overwritten, skipped, deleted}`。
- **内容相同跳过（content skip）** —— 目标存在即覆盖，但 `sha256` 相同**且文件仍在**则跳过写入（保住 mtime）。
- **段级净化（segment sanitization）** —— 路径模板渲染后逐段处理：非法字符 → `_`、剥首尾空格与 `.`、单段 ≤200 字节、空值回落 `_`；**值不能凭空造出目录层级**。
- **原子写** —— 同目录 `<name>.tmp` → `rename`；媒体库里不留半截文件。
- **EXDEV** —— `hardlink` 跨设备失败 → 任务 `failed`，提示改 `copy|move`，**不静默回退**。
- **裸名图片** —— `poster.*` / `fanart.*` / `extrafanart/fanart{n}.*`（1 起），不带 `{name}-` 前缀；`thumb` 永不写。
- **movie.nfo** —— 唯一正确的 NFO 文件名；多分盘目录里 `<番号>.nfo` 会被静默忽略。

## 任务与存储

- **活跃占用（active）** —— `task_files.active`：非终态任务占着路径；转终态（`succeeded`/`partial`/`failed`/`cancelled`）即置 0 释放。
- **去重闸** —— 闸一靠 `UNIQUE(path) WHERE active = 1` 约束（冲突计入批次 `skipped`）；闸二是查询，只提示「可重跑」。
- **崩溃复位（interrupted）** —— 启动时把 `running` 的 run 记 `interrupted`、非终态任务复位 `queued` 重跑；超 `retry_limit`（默认 5）转 `failed`。
- **任务状态** —— 7 值：`queued` / `running` / `succeeded` / `partial` / `failed` / `needs_number` / `cancelled`。
- **批次状态** —— 5 值：`running` / `succeeded` / `partial` / `failed` / `cancelled`。
- **任务槽（task slot）** —— `concurrency.tasks`（默认 4）：同时跑几个任务。
- **前缀单飞（prefix single-flight）** —— `scan`/`filter`/`group` 同一时刻只跑一个；tick 用 `TryLock` 拿不到就跳过本轮且水位线不推进。
- **在飞请求（in-flight request）** —— `concurrency.requests`（默认 8）：Fetcher 全局在飞上限。
- **单写连接（single writer）** —— 写句柄 `SetMaxOpenConns(1)`；读走独立只读池。SQLite 走 WAL + `busy_timeout(5000)` + `synchronous(NORMAL)`。
- **优雅中断（graceful interrupt）** —— SIGTERM：停取新任务 → cancel → 最多 25s → 落 `interrupted`。
- **单副本（single replica）** —— V1 的不变量；多副本是将来新的一次努力。

## 触发与监控

- **触发（trigger）** —— `manual` / `monitor` / `webhook`（webhook 值预留，V1 不开端点）。
- **接入路径（intake）** —— 三个触发源汇入的同一条路径；触发只注入 `{roots, exclude}` 覆盖 `scan.with`。
- **`trigger_ref`** —— 触发来源标识：monitor 的根路径 / manual 预览选定的根 / webhook 的 torrent hash。
- **水位线（watermark）** —— 每根一条持久基线（ctime）；晚于它的文件才进视野，**存量不刮**。
- **静默窗口（quiet window）** —— 就绪判据：同一 `(size, mtime)` 稳定 ≥ `quiet_window_seconds`（默认 120）。
- **就绪（readiness）** —— 组内文件全部静默；**整组就绪才提交**，卡住的文件只冻结它自己那一组。
- **pending 集合** —— 本轮 ctime 晚于水位线、尚未提交的文件集合；清空且 walk 无错才推进水位线。
- **悬空引用（dangling reference）** —— 监控引用的 pipeline 已被删；启动时停该监控、其余照跑，不拒绝启动。

## 接口与前端

- **机器码（machine code）** —— 载荷只传枚举值/错误码，中文映射在前端本地表。
- **错误体** —— `{error:{code, message, detail?}}`；`detail` 可选结构化（如 `detail.missing_paths[]`）。
- **包裹（envelope）** —— 列表恒 `{items, total, page, per_page}`，单体响应裸对象，空列表恒 `[]`。
- **轮询进度面** —— 列表 3s / 详情 2s，且仅在存在非终态任务时；不上 SSE。
- **三视图** —— 扫描预览 / 任务列表 / 任务详情（+ 待补号面板两处入口）。

## 验收

- **验收环境（`make accept`）** —— 真 Jellyfin（钉版本）+ 回放夹具的刮削源 + 宿主真二进制，一条命令从零复现。
- **夹具（fixture）** —— 真机抓取、逐字不改；manifest 每行带 `url/status/bytes/captured/sha256`；构造夹具只允许 `defect-reproduction` / `parser-shape` 两种理由。
- **门禁自检** —— 每个门禁自证两面（破坏 → 红且原因对；没破坏 → 绿）+ 每次运行留「我跑了」的正向标记。
