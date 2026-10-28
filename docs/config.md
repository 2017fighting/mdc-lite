# 配置与启动

进程级配置 `/config/config.yaml` 与流水线目录 `/config/pipelines/` 是两份文件、互不合并。
一个流水线文件 = 一条完整流水线，不做层级合并。

## 覆盖顺序

`flag > ${VAR} > 文件 > 默认`

- 命令行 flag 见 `mdc serve --help` 与 `mdc config check --help`。
- 环境变量只有 `${VAR}` 插值这一种形式，`${VAR:-默认}` 给回落值。**缺失即启动报错**——一份悄悄丢掉值的配置比拒绝启动更坏。
- 插值后的标量按内容重判类型，所以数值键也能走环境变量：`retry_limit: ${RETRY}`（`RETRY=7` → `7`）。**加了引号的标量仍是字符串**，所以 `listen: "${ADDR}"` 不会被推断成别的东西。
- 文件缺失不是错误：全键有默认，首次启动不生成任何文件。

## 进程级键

| 键 | 默认 | 说明 |
|---|---|---|
| `listen` | `":9208"` | HTTP 监听地址 |
| `db_path` | `/config/mdc.db` | SQLite 路径；所在卷**不得是 NFS**（WAL 不能跨网络文件系统） |
| `pipelines_dir` | `/config/pipelines` | 只读**顶层** `*.yaml`/`*.yml`、不递归；指向目录的符号链接启动报错；改后缀 = 临时禁用该流水线 |
| `templates_dir` | `/config/templates` | `@文件` 模板根；禁 `..`、禁绝对路径 |
| `log` | `{level: info, format: text}` | 只写 stdout，没有日志文件 |
| `browse_roots` | `[{path: "/media"}]` | 容器内目录选择器白名单；`label` 缺省 = `path` |
| `concurrency` | `{tasks: 4, requests: 8, images: 4}` | 三个数只配在这一处，步骤级不开 `with.concurrency` |
| `retry_limit` | `5` | 崩溃复位后的重跑上限 |
| `proxy` | 空（直连） | 进程级出口代理，源级可覆盖 |
| `webhook` | `{enabled: false, token: "", categories: {}}` | 第二批；形状现在定，V1 不开端点 |

`mdc config check` 打印生效的键**及其来源**（`flag` / `env` / `file` / `default`），密钥值只显示 `(set)` / `(unset)`，代理 URL 里的口令替换成 `***`。

## 流水线文件

顶层只有两个键：`name`（可选显示名，默认 = id）与 `steps`。**没有 `id` 键**——`id` 就是文件名字干（`main.yaml` → `main`），引用一律用 `id`。

步骤元数据层固定四个键：`type` / `with` / `name`（同一 type 出现两次时必填）/ `on_error`（`fail` | `continue`，默认 `fail`）。参数一律收在 `with:` 下。

## 步骤 `with` 的默认值

每个键都有默认，所以 `with: {}` 与整段省略 `with` 等价。两个例外：`output.with.root`（写盘步骤不写落点就是配置错误，不是默认值）与 `parse.with.rules`（空规则表什么都解析不出，同样是配置错误）。

| 步骤 | 默认 |
|---|---|
| `scan` | `roots: []`、`exclude: []`（由触发注入覆盖） |
| `filter` | 无（`rules` 必填） |
| `group` | `max_distance: 2`、`max_files_for_levenshtein: 50`、`companion_extensions` 为字幕与图片扩展名 |
| `parse` | 无（`rules` 必填） |
| `scrape` | `sources` 为全局序 `[javdb, dmm, javbus, fc2]`、`timeout: 5m` |
| `images` | 无键 |
| `nfo` | `template: default` |
| `output` | `dir_template: {{ number }}`、`file_template: {{ number }}-cd{{ part }}`、`link: hardlink`、`required_fields: [title, fanart]`；**`root` 必填** |

`scrape.with.sources` 是有序列表，每项是**裸 id** 或**带 id 的对象**：`sources: [javdb, {id: dmm, timeout: 30s, domains: [dmm.co.jp]}]`。源级的四个旋钮（`proxy` / `domains` / `rate` / `timeout`）就挂在这一项里，位置与设置不分家。`priority.<field>` 是**完整替换**全局序，不是追加。

`group.with.sanitize` 是「字面量 / 正则」混合表：裸字符串按字面量剥，`r:` 前缀（如 `r:[-_. ]1080p$`）按 Go 正则剥。

`filter.with.rules[].when` 的四个键（`ext_in` / `ext_not_in` / `min_size` / `stem_matches`）**互斥**：一条规则同时写两个时语义未定义，所以启动即拒。要同时卡扩展名与体积就写两条规则。

规则表**有序、首条命中即停**，所以兜底规则要写在最后（示例配置的最后一条 `everything-else` 就是）。没有被任何规则命中的文件按 `no_match` 拒绝——它是拒绝原因，不是规则名，因此带下划线。

`group.with.part_patterns` 是用户追加的**确定标记**正则：必须匹配在名字**末尾**，**第一个捕获组是分片序号**（`(?i)(?:^|[-_. ])slice(\d)$`）。没有捕获组的 pattern 会在启动时被拒——它会匹配、却什么也决定不了。

`parse.with.rules` 是**位置列表**：`{builtin: default}` 在它所在的位置展开内置四条（`fc2` / `date-code` / `dmm-content-id` / `generic`），写在它前面的 `{pattern: …}` 先被记到番号上，写在后面则让位；**表里完全不写 `builtin` = 不用任何内置规则**。空表拒绝启动（一行也解析不出）。用户模式用**第一个捕获组**当番号（没有捕获组时用整段匹配），`family` 缺省 `custom`。

求值顺序是**全匹配 + 包含裁决**，不是首条命中：所有规则全跑，匹配区间被另一条**更长**区间包含的候选出局（被更严格规则拒绝的区间同理），再按归一化后的番号去重；剩多个不同番号就是 `ambiguous`，交人填号。归一化内置（大写、`_`/空格 → `-`、粘连字母数字插连字符、尾号补零到 3 位**只补不截**、去首尾非字母数字、**全角 → 半角**且在匹配前折叠），外加 per-prefix 补零宽度表（`snis00440hhb` → `SNIS-440`）。手填番号与文件名走同一套，所以 `ssis-1` 与 `SSIS-1` 都是 `SSIS-001`。

## 手动跑一条流水线（`mdc run`）

```
mdc run --pipeline <id> --roots <dir>[,<dir>|<file>] [--until <step>] [--dry-run]
```

一次扫描、一份打印出来的摘要：walk 了多少、进了多少候选、滤掉多少及原因、分了哪些组；停在 `parse` 时还打印每组的番号（原文 / 归一化 / 规则家族）与待补号的组及原因。它是预览、监控 tick 与调试的**同一段前缀**——触发只注入 `{roots, exclude}` 覆盖 `scan.with`，其余一律照流水线文件——所以规则写错在这里就能看见。

- `--roots` 可重复，也可逗号分隔；元素可为目录（递归）或文件（只取该文件）。
- `--until` 接**步骤 type** 或**步骤 `name`**；不写就跑到最后一步。八类步骤全部可跑。
- `--dry-run` 只产出 `output` 的 `Plan`，不落盘。不写 dry-run 就 `Apply` 那份 Plan：图片 / NFO / 伴随文件走同目录 `.tmp` → `rename`，视频按 `output.with.link` 硬链 / 复制 / 移动；内容相同（`sha256` 相同且文件仍在）则跳过并保住 mtime。
- `mdc run` **不落库**（不建批次、不写任务台账），所以它的写盘没有 `file_rows`；任务重跑的清理与幂等要靠任务运行（见下节）。
- 目录里任一流水线文件坏了就整体拒绝（与 `serve` 同一条规矩），不会「用好的那条跑、坏的装作没看见」。

## 拒绝启动的情形

任一坏则**拒绝启动**，且**加载目录下全部文件**——包括没有被任何触发引用的（今天没人用的流水线，明天会被用）。

- 未知键（含 `with` 里的 typo，含两层嵌套）
- 未知步骤 type
- 拓扑不连通（例如 `parse` 之前没有 `group`）
- 没有落盘步骤（缺 `output`）
- 同一 type 出现两次却没给 `name`
- `output.with.root` 缺失（必填、无默认）
- 枚举与值级校验（`then`、`link`、`required_fields` 的字段名、模板变量名、正则、并发数 ≥1）

报错形状：`文件:行:列: 第 N 步 (type): ...`，例如

```
config/pipelines/main.yaml:24:18: step 2 (filter): unknown field "rules[0].when.ext_in2"
```

行号与列号指向**出问题的那个键**。严格性落在 `with` 的类型化解码上：`map[string]any` 会静默吞掉 typo，而把子节点重新 marshal 再解码会丢掉位置，所以实现是「按 struct 的 yaml tag 走查原始 `yaml.Node`」。文件内的 YAML 锚点与 `<<:` 都允许，merge key 不绕过未知键检查。

## 启动与探活

`mdc serve` 起来后：

- `/api/health`（别名 `/healthz`）→ `{status, version, uptime_ms, db:{ok, path, migrations}}`；就绪 200。
- DB 打不开、迁移没跑完、或 7 张业务表不全 → **503** `db_unavailable`，`db.error` 说原因。进程**仍然起来**，因为一个能回答「我为什么没就绪」的进程比一个 crash loop 有用。
- 前端产物内嵌；未跑 `make web` 的检出（或没有 Node 的机器）服务占位页，不影响 `go test ./...`。
- SIGTERM：停止监听 → 最多等 25s 收尾 → 退出。

## 存储

`modernc.org/sqlite`（纯 Go，`CGO_ENABLED=0`）+ 内嵌 goose 迁移，启动 `Up`，只前进无回滚。迁移建出 7 张业务表（`batches` / `tasks` / `task_runs` / `task_files` / `file_rows` / `step_runs` / `source_runs`）与 `goose_db_version`。

连接面：写句柄 `SetMaxOpenConns(1)` + `_txlock=immediate`，读走独立只读池（`SetMaxOpenConns(4)` + `query_only`）。pragma 一律走 DSN（`journal_mode(WAL)` / `busy_timeout(5000)` / `synchronous(NORMAL)`），不用连接级 `Exec("pragma …")`——那样只对执行它的那条连接生效。

## 门禁

`make check` = `go vet` + `go test ./...` + `golangci-lint` + 前端 `typecheck`，CI 在 PR 上跑同一组。CI 另有一个 job 证明两件事：二进制是**静态**的，以及配置门禁**真的会红**（把 `ext_in` 改成 `ext_in_typo` 后 `config check` 必须失败，且失败原因对）。

## 引擎前缀（`scan` / `filter` / `group`）的实现裁决

写在这里，因为它们是实现时才暴露出来的洞：

- **`part_patterns` 的首捕获组是分片序号**，且必须匹配在末尾。没有捕获组的 pattern 会「匹配但不决定任何事」，所以启动即拒（`空转的键比没有键更坏`）。
- **条件标记的「兄弟同意」要求两侧的尾标不同**。字面判据是「剥掉它之后主干相同的文件 ≥2 个」，但那会把 `ABC-123.mp4` + `ABC-123-1.mp4` 判成同一组的两片、且都落在第 1 片——落盘时两条同名产物，正是 ADR-0003 要避免的静默覆盖。所以同意只发生在「≥2 个带条件尾标的文件、且尾标不同」之间（`9ofje121A` + `9ofje121B`）。
- **距离合并不得让一个组出现两个同号分片**。② 是无条件的两两合并，单链会传染：一个目录里 30 部相邻番号各自 `-cd1`/`-cd2` 会被串成一组（`WORK-100-cd2` 与 `WORK-101-cd1` 只差一个字符）。所以 ② 只在「合并后组内分片序号仍两两不同」时才生效——一部作品不会有第 3 片的两份。
- **「像分片却没合」的告警有两个上界**：距离 ≤ `3 × max_distance`，且两条主干共享至少一半前缀。否则一个目录里的分区对会把摘要刷成一片噪声，而提示就不成其为提示了。
- **`scan` 只按名字滤三样东西**：隐藏项、`.part` / `.!qB`、`exclude` 子树。另外两件不是判断而是硬约束：隐藏**目录**整棵剪掉（否则 `.Trash-1000` 之类会像正常文件一样进来），指向目录的符号链接不跟随（会成环），指向文件的符号链接按目标文件收下（媒体库里很常见）。
- **`filter` 的 `stem_matches` 判的是「原始 basename 去扩展名」**：剥离噪声只发生在 `group` 一处，所以过滤阶段还没有「主干」。
- **`mdc run` 不碰数据库**：前缀的产物全在内存里；任务级后缀的写盘由 `output` 出 `Plan`、`Apply` 执行，非 `--dry-run` 时才会写输出目录。

## 落盘主链（`scrape` / `images` / `nfo` / `output`）的实现裁决

- **`output` 是唯一写盘出口**，但它自己不写：`Run` 只产出 `Plan{Items[].Writes[]{Kind, Role, Index, Mode, Src, Path, Dst, Data}}`，由 `Apply` 统一执行；`--dry-run` 就是不 `Apply`。`nfo` 只渲染文本。
- **图片命名是固定布局**（spec §8）：`poster.*` / `fanart.*` / `extrafanart/fanart{i+1}.*`（1 起），`thumb` 永不写。扩展名按 **magic bytes** 嗅探：`jpg`/`jpeg`/`png`/`gif`/`tbn` 原样，`webp`/`svg`/嗅探失败 → `.jpg`（只改名字，不重编码字节）——所以一张 webp 封套会以 `.jpg` 落地，且**不能被裁切**（标准库没有 webp 解码器），于是 `poster` 仍缺、任务 `partial`。
- **`poster.crop`**（ADR-0015）：`poster` 为空且 `fanart` 字节在手时，从右侧按 2:3 裁、`image/jpeg` 编码；`Sources[poster] = "<源 id> (自动裁切)"`、`Derived.PosterCrop = true`、`file_rows.src` 记 fanart 的来源 URL；裁切失败只让任务 `partial`。
- **NFO 只写 `movie.nfo`**，模板环境开 autoescape（字段里的 `&` 必须变 `&amp;`，否则两份解析器都会静默吞掉整份元数据）；默认模板：`<plot>`（无 `<outline>`）、`<premiered>` + `<releasedate>` 双写、`<rating>`、`<tag>发行: X</tag>`、`tags` 重复 `<tag>`（不用 `<genre>`）、actor 带 `<role>女优|男优</role>`、`originaltitle` = `title`、`sorttitle` = `title` + 分片。
- **路径模板逐段净化**：模板自己的 `/` 决定层级，渲染值里的 `/` 变 `_`；非法字符与控制字符 → `_`，剥首尾空格与 `.`，单段 ≤200 字节（UTF-8 边界），空值回落 `_`。
- **幂等与清理**：目标存在即覆盖，但 `sha256` 相同且文件仍在则跳过；重跑按**上一次 run 的 `file_rows`** 删掉本次不再产出的产物，且**只删台账里记过、落在该任务输出目录内**的文件。清理与台账写在同一条结束事务里。
- **原子写**：图片 / NFO / 伴随文件 / `copy` 都是同目录 `.tmp` → `rename`；`hardlink` 直接产最终名；`move` 就是 `rename`。跨设备 `EXDEV` → 任务 `failed`，错误点名「改 `output.with.link=copy|move`」，**不自动回退**。
- **伴随文件有落盘通道**（ADR-0017）：字幕跟随对应视频改名（保留自己的语言 token，例如 `ABC-123.zh.srt` 跟着 `ABC-123-cd1.mp4` 变成 `ABC-123-cd1.zh.srt`），图片保持原名；`Kind = companion` / `role = companion`、照常进台账，因此幂等与清理对它同样成立，且**不受 `link` 开关影响**。
- **`required_fields` 默认 `[title, fanart]`，`fanart` 按字节到手判**；缺则 `output` 步失败（→ 任务 `failed`），除非 `output.on_error: continue`（→ 任务 `partial`，能写的照写）。任何 14 字段缺失都让 `Metadata.Partial = true`。
- **多分片时 primary 也带 `-cd1`**：spec §4 的「primary 裸名」与 §10 的 `file_template` 默认值冲突；取 §10——模板对**每个**分片生效（`{{ number }}-cd{{ part }}` → `ABC-123-cd1.mp4`），因为它是配置面写死的键，取证文档里的多分盘目录示例也是 `-cd1`；组内只有一个文件时仍保留源文件名。
- **任务重跑从 `parse` 之后开始，不重跑 `parse`**：番号已经是 `tasks.number` 这一行事实（可能由人填过），重跑 `parse` 只会用文件名的推断去覆盖它。所以重跑用 `task_files` 重建 parse 产物（`Options.Seed`），再从 `scrape` 起跑；重扫目录、重跑剥离规则都不发生。
- **`scrape` 的调度是顺序版**：按 `scrape.with.sources` 顺序逐源 `Scrape`，标量字段先到先得、多值字段整段取自同一源；图片字段只有在 `FetchImage` 成功后才算贡献（失败则轮到下一个源）。波次并发、`priority[field]` 与 `concurrency` 的上限落在同一接缝上（票 #24 / #25），不改产物形状。

## 本票落下的实现裁决

写在这里，因为它们是实现时才暴露出来的洞：

- **`scrape.with.sources` 一项两形**：spec §6 说它是有序列表，§7 说源级旋钮「挂在 `scrape.with.sources.<id>` 下」——同名而形状不同。取「列表项可为字符串或对象」，于是 `sources: [javdb, {id: dmm, timeout: 30s}]` 既保住有序，又让旋钮跟着自己的 id 走。校验覆盖空 id、重复 id、未知旋钮、负值。
- **`filter.when` 四个键互斥**：spec 只要求「未知键报错」，但一条规则同时写 `ext_in` 与 `min_size` 时语义未定义。互斥把未定义变成启动期错误；要同时卡两个条件就写两条规则。
- **`${VAR}` 插值后按内容重判类型**：否则 `retry_limit`、`concurrency.*` 这类数值键无法从环境变量设置，「flag > ${VAR} > 文件 > 默认」在数值键上就不成立。加引号的标量仍是字符串。
- **每个步骤的 `with` 都有默认**（见上表），`with` 整段省略 = `with: {}`。
- **`/healthz` 是 `/api/health` 的别名**：k8s 惯例探针路径，一行代码省掉部署侧的 rewrite。`mdc version` / `mdc help` 同理是常规便利，不在 spec 的子命令面里。
- **`with` 结构体每步新建**：registry 存的是构造函数而不是共享实例，否则一个文件的合法值会漏进另一个文件，校验结果取决于目录读取顺序。
- **health 的 nil 走接口**：`api.Store` 存 `nil *store.Store` 是非 nil 接口，处理器会 panic 而不是回 503，所以 CLI 用独立变量并只在打开成功时赋值。
