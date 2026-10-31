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
| `group` | `companion_extensions` 为字幕与图片扩展名（组的身份是番号，没有距离阈值可调） |
| `parse` | 无（`rules` 必填）；`sanitize: []`、`part_patterns: []` |
| `scrape` | `sources` 为全局序 `[javdb, dmm, javbus, fc2]`、`priority` 把 14 个字段全写全（见下）、`timeout: 5m`；源级 `timeout` 30s、`rate` 1 req/s + burst 2 + 冷却 5m |
| `images` | 无键 |
| `nfo` | `template: default` |
| `output` | `dir_template: {{ number }}`、`file_template: {{ number }}{% if part %}-cd{{ part }}{% endif %}{{ version }}`、`link: hardlink`、`required_fields: [title, fanart]`；**`root` 必填** |

`scrape.with.sources` 是有序列表，每项是**裸 id** 或**带 id 的对象**：`sources: [javdb, {id: dmm, timeout: 30s, domains: [dmm.co.jp]}]`。源级的四个旋钮（`proxy` / `domains` / `rate` / `timeout`）就挂在这一项里，位置与设置不分家。

`priority.<field>` 是**完整替换**全局序，不是追加；**未列出的字段用 `sources`**。出厂默认把 14 个字段全写全，且只列**声明了该字段**的源（`poster`/`rating` 只有 javdb 与 dmm；`actors`/`director`/`series`/`publisher` 没有 fc2；`plot`/`extrafanarts` 没有 javbus）。两条相关规矩：

- **写到 `priority` 里的源必须声明该字段**，否则**拒绝启动**：`priority.poster: [javbus]` 会报 `文件:行:列: 第 N 步 (scrape): priority.poster names "javbus", which does not declare that field`。`Fields()` 只在启动校验与前端对照用，**运行时不据此跳过任何源**——候选列表就是全部事实。
- **`priority` 里的 id 必须是 `sources` 里有的**，否则同样拒绝启动（`priority.title names "dmm", which is not in sources`）：没配置的源在运行期会被丢掉，那个字段就少一个候选而没人知道。
- 文件里**写了 `sources` 但没写 `priority`**，逐字段序就是那份 `sources`（出厂表描述的是出厂源列表，用户换了源列表就以用户的为准）；**写了 `priority`** 时它整体替换出厂表，未列字段回落到 `sources`（不会与出厂表合并）。

四个源各自声明的字段（`Fields()`，也就是能写进 `priority` 的集合）：

| 源 | 管道 | 声明的字段 | 需要注意 |
|---|---|---|---|
| `javdb` | APP JSON API：搜索 → 详情 | 全部 14 个 | 要 `jdsignature`（代码常量、每请求现算）；FC2 号改写 `FC2-<digits>` 后才检索；图片走 APP CDN，源内 XOR 解码 |
| `dmm` | GraphQL：搜索（两种 id 拼法，串行命中即停）→ 内容 | 全部 14 个 | **要日本出口**（`proxy` 旋钮就是给它用的）；`releasedate` 由 RFC3339 转 `YYYY-MM-DD`；`rating` = 五星均分 ×2；样本图第一张同时是 `fanart` |
| `javbus` | 详情页直取（番号即地址） | `title` / `fanart` / `tags` / `releasedate` / `actors` / `director` / `runtime` / `series` / `studio` / `publisher` | cookie `dv=1`；图片要 `Referer`（源内常量）；404 = 没这条 |
| `fc2` | 详情页直取（`/article/{末尾数字段}/`） | `title` / `plot` / `fanart` / `extrafanarts` / `tags` / `releasedate` / `runtime` / `studio` | miss 页是 **HTTP 200**，靠 `items_notfound_wp` 正向标记判；无演员表 |

`domains` 是有序列表，**失败即切下一个**；一项可以写裸域（`javbus.com`，按 `https` 访问），也可以写带 scheme 的完整地址（`http://127.0.0.1:9001`）——后者是让夹具回放接管某个源的循正规做法，源里的其他每一行（选择器、cookie、Referer）都还是生产那一份。`timeout` 管**单次 HTTP 请求**（默认 30s），`scrape.with.timeout` 管**整个番号**（默认 5m）。`rate` 的 `requests_per_second` / `burst` / `cooldown` 三个字段各自独立，只写一个时其余取默认。

`parse.with.sanitize` 是「字面量 / 正则」混合表：裸字符串按字面量剥，`r:` 前缀（如 `r:[-_. ]1080p$`）按 Go 正则剥。表挂在 `parse` 上，因为 parse 先于 group 运行（ADR-0019），剥离的目的是让番号规则看到干净的文本。旧配置里的 `group.with.sanitize` / `group.with.part_patterns` 现在会在启动期被当作未知键拒绝，迁到 `parse.with` 即可。

`filter.with.rules[].when` 的四个键（`ext_in` / `ext_not_in` / `min_size` / `stem_matches`）**互斥**：一条规则同时写两个时语义未定义，所以启动即拒。要同时卡扩展名与体积就写两条规则。

规则表**有序、首条命中即停**，所以兜底规则要写在最后（示例配置的最后一条 `everything-else` 就是）。没有被任何规则命中的文件按 `no_match` 拒绝——它是拒绝原因，不是规则名，因此带下划线。

`parse.with.part_patterns` 是用户追加的**确定标记**正则：必须匹配在名字**末尾**，**第一个捕获组是分片序号**（`(?i)(?:^|[-_. ])slice(\d)$`）。没有捕获组的 pattern 会在启动时被拒——它会匹配、却什么也决定不了。

`parse.with.rules` 是**位置列表**：`{builtin: default}` 在它所在的位置展开内置四条（`fc2` / `date-code` / `dmm-content-id` / `generic`），写在它前面的 `{pattern: …}` 先被记到番号上，写在后面则让位；**表里完全不写 `builtin` = 不用任何内置规则**。空表拒绝启动（一行也解析不出）。用户模式用**第一个捕获组**当番号（没有捕获组时用整段匹配），`family` 缺省 `custom`。

求值顺序是**全匹配 + 包含裁决**，不是首条命中：所有规则全跑，匹配区间被另一条**更长**区间包含的候选出局（被更严格规则拒绝的区间同理），再按归一化后的番号去重；剩多个不同番号就是 `ambiguous`，交人填号。归一化内置（大写、`_`/空格 → `-`、粘连字母数字插连字符、尾号补零到 3 位**只补不截**、去首尾非字母数字、**全角 → 半角**且在匹配前折叠），外加 per-prefix 补零宽度表（`snis00440hhb` → `SNIS-440`）。手填番号与文件名走同一套，所以 `ssis-1` 与 `SSIS-1` 都是 `SSIS-001`。

## 手动跑一条流水线（`mdc run`）

```
mdc run --pipeline <id> --roots <dir>[,<dir>|<file>] [--until <step>] [--dry-run]
```

一次扫描、一份打印出来的摘要：walk 了多少、进了多少候选、滤掉多少及原因、分了哪些组；停在 `group` 时还打印每组的番号（原文 / 归一化 / 规则家族）与每个待补号文件的原因。它是预览、监控 tick 与调试的**同一段前缀**——触发只注入 `{roots, exclude}` 覆盖 `scan.with`，其余一律照流水线文件——所以规则写错在这里就能看见。

- `--roots` 可重复，也可逗号分隔；元素可为目录（递归）或文件（只取该文件）。
- `--until` 接**步骤 type** 或**步骤 `name`**；不写就跑到最后一步。八类步骤全部可跑。
- `--dry-run` 只产出 `output` 的 `Plan`，不落盘。不写 dry-run 就 `Apply` 那份 Plan：图片 / NFO / 伴随文件走同目录 `.tmp` → `rename`，视频按 `output.with.link` 硬链 / 复制 / 移动；内容相同（`sha256` 相同且文件仍在）则跳过并保住 mtime。
- `mdc run` **不落库**（不建批次、不写任务台账），所以它的写盘没有 `file_rows`；任务重跑的清理与幂等要靠任务运行（见下节）。
- 目录里任一流水线文件坏了就整体拒绝（与 `serve` 同一条规矩），不会「用好的那条跑、坏的装作没看见」。

## 手动试一个源（`mdc source test`）

```
mdc source test --id <id> --number <番号> [--dump] [--pipeline <id>] [--dump-dir <dir>]
```

一件事：**这个源对这个番号现在到底说什么**。它走程序自己的 Fetcher 与解析器（同一份 UA、cookie、Referer、限速），所以打印出来的就是任务会拿到的东西——`curl` 加肉眼看 HTML 既验不了 cookie，也验不了选择器。

- 逐字段打印 14 个源级字段：`title` 这类给出值，`plot` 这类标「not declared by this source」（能力声明里没有），`series` 这类标「declared, absent on this page」（声明了但这页没有）。**这三者是不同的答案**，混成一个就会误读一个源。
- 再打印 `status:`（`ok` / `notfound` / `failed`）与 `reason:`（六档原因码，仅 `failed`）、耗时。
- 源给了图片地址就真的去取一次，打印字节数与 magic bytes 判出的类型——一张「403 页面存成 cover.jpg」在这里就现形了。
- `--dump` 把这次的原始响应逐字写进夹具目录（默认 `internal/source/<id>/testdata/captures`），并在同目录 `fixtures.json` 里记 `path` / `leg` / `url` / `status` / `bytes` / `captured` / `sha256`。夹具只能这样来：**由程序自己的 Fetcher 抓、逐字不改**，写坏了它就是坏了的证据。
- 两段式源（javdb 的搜索→详情、dmm 的搜索→内容）每次请求都留一份，文件名带 `leg`：`javdb-SSIS-001-search.json` / `javdb-SSIS-001-detail.json`、`dmm-SSIS-001-search-ssis00001.json` / `dmm-SSIS-001-content.json`。回放测试按请求本身（`q`、`queryWord`、`ppvContent(id:)`）选对应那份。
- 退出码：`ok` 与 `notfound` 为 0（「站点没这条」是一个答案），`failed` 或图片没取到为 1。
- `--pipeline` 选哪条流水线的 `scrape.with.sources` 提供这个源的四个旋钮；不写就用第一条带 `scrape` 的流水线。

## 网络层（`Fetcher`）

元数据与图片共用一个 `Fetcher`，一个进程一个实例：代理、限速、冷却、重试、取消都在这一处，四个源自己不重试、不管代理、不管退避。

- **只用 `net/http` + 浏览器 UA**。不接 TLS 指纹库，不做 FlareSolverr 也不留占位键——一个解析了但不生效的键比没有这个键更坏。
- **限速按可注册域（eTLD+1）分桶、进程内全局**：`pics.dmm.co.jp` 与 `www.dmm.co.jp` 同一个桶（`dmm.co.jp`），跨源跨流水线共享。默认 1 req/s + burst 2。后缀表用 `golang.org/x/net/publicsuffix` 内嵌的公共后缀表，不是手写的短名单——否则 `a.github.io` 与 `b.github.io` 会被当成同一个站。没有可注册域的地址（夹具的 IP 字面量、`localhost`、单标签主机名）回落到 host:port，这样两个本地夹具服务器不会互相限速；解析不出的主机按「后两段」退化——**宁可多合桶，不可少合桶**。
- **冷却**：403 / 429 / 503 同一惩罚 → 该可注册域冷却 5 min（源级 `rate.cooldown` 可改），**快速失败、不排队**，状态只在内存（重启即忘）。惩罚只按状态码判，不认 body 标记。
- **重试只在 Fetcher 内**：连接错 / TLS EOF / 5xx 重试 **2 次**，退避 1s、4s 加 jitter；**404、其他 4xx、超时与冷却不重试**。`domains` 的镜像切换与重试**共享同一预算**（总共三次尝试，不是每个域三次）。等待与退避都尊重 ctx。
- **404 是答案不是错误**：Fetcher 把 404 原样交给源，源自己判 `notfound`。其余取不到的情况都带六档原因码（`egress_unavailable` / `timeout` / `blocked` / `cooldown` / `http_5xx` / `parse`），落 `source_runs.reason`。**「取不到」永不写成 `notfound`**；通用层只按状态码判惩罚，站点拦截页由源自己用正向标记认出来报 `blocked`。
- 出口是操作者的事：`proxy` 为空就是直连（**不读 `HTTP_PROXY` 环境变量**，也不做可达性预检），打不通就如实记 `egress_unavailable`。

## 拒绝启动的情形

任一坏则**拒绝启动**，且**加载目录下全部文件**——包括没有被任何触发引用的（今天没人用的流水线，明天会被用）。

- 未知键（含 `with` 里的 typo，含两层嵌套）
- 未知步骤 type
- 拓扑不连通（例如 `parse` 之前没有 `filter`，或 `group` 之前没有 `parse`）
- 没有落盘步骤（缺 `output`）
- 同一 type 出现两次却没给 `name`
- `output.with.root` 缺失（必填、无默认）
- 枚举与值级校验（`then`、`link`、`required_fields` 的字段名、模板变量名、正则、并发数 ≥1）
- `scrape.with.sources` 里的 id 不是本二进制有的源（typo）；`priority.<field>` 里的 id 不在源列表或**不声明该字段**

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

## 引擎前缀（`scan` / `filter` / `parse` / `group`）的实现裁决

写在这里，因为它们是实现时才暴露出来的洞：

- **`part_patterns` 的首捕获组是分片序号**，且必须匹配在末尾。没有捕获组的 pattern 会「匹配但不决定任何事」，所以启动即拒（`空转的键比没有键更坏`）。
- **`parse` 逐文件独立判定**：消费 `Candidates`，一个文件一条 `ParsedCandidate`（番号 + 分片 + 版本旗标），解析不出的文件进 `Unresolved`。条件标记（孤立尾号 `-1`、粘连字母 `9ofje121A`）在 parse 阶段不进分片：只有 `group` 看得到同番号的兄弟，也只有它能让兄弟同意——≥2 个文件剥掉尾标后同干、且尾标不同，才认。
- **`group` 的唯一判据是归一化番号**（ADR-0019）：同一番号的分片与多版本（原版 / `-C` / `-U` / `-UC`）全在一个组，跨扫描目录也合；相邻番号（`ABP-123` / `ABP-124`）天然分开，不再有编辑距离与「像分片却没合」的告警。
- **primary 的选择是「先分片、后版本」**：分片序号最小者优先（无标记算第 1 片），同一分片按「原版 > 中文 > 破解 > 中文+破解」；依据是文件名里的旗标，不打开文件。整组的刮削番号、元数据与输出目录都跟着 primary 走。
- **伴随文件靠名字或番号挂靠**：字幕/图片先按主干找视频，再按自己解析出的番号找组，同目录优先；都找不到就进 `unmatched`（`no_group`），绝不凭同目录就认领。`ABC-123-cd1.srt` 记住的是第 1 片，`ABC-123.zh.srt` 跟 primary。
- **未能解析出番号的文件不成为组**：它留在 `Parsed.Unresolved`，一个文件一条待补号任务（`need_number`），不影响其它番号。
- **`scan` 只按名字滤三样东西**：隐藏项、`.part` / `.!qB`、`exclude` 子树。另外两件不是判断而是硬约束：隐藏**目录**整棵剪掉（否则 `.Trash-1000` 之类会像正常文件一样进来），指向目录的符号链接不跟随（会成环），指向文件的符号链接按目标文件收下（媒体库里很常见）。
- **`filter` 的 `stem_matches` 判的是「原始 basename 去扩展名」**：剥离噪声发生在 `parse`（ADR-0019），所以过滤阶段还没有「主干」。
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
- **命名规则是「先分片、后版本」**（ADR-0020，取代早先「多分片时 primary 也带 `-cd1`、单文件保留源文件名」的裁决）：`{number}[-cd{part}][-中文][-破解]`，默认 `file_template` 就是这句话。三件事随之改口：**源文件名不再进库**（组内只有一个文件时也按规则渲染，`MIDA-557-C.mp4` → `MIDA-557-中文.mp4`，`MIDV-804.2025.1080p…-MTeam.mkv` → `MIDV-804.mkv`，否则同一部的两次下载会在库里成两条）；**`-cd{n}` 只在名字写了分片标记时出现**（`ABC-123.mp4` 与 `ABC-123-cd2.mp4` 同组时前者仍是 `ABC-123.mp4`）；**版本顺序是规则的**（`-UC` 与 `-CU` 都是 `-中文-破解`）。
- **字幕跟的是「分片 × 版本」，不只是分片**：组内同一分片有多个版本是 ADR-0019 后的新形状，字幕按自己名字里的版本旗标选跟随对象——`ABC-123-C.zh.srt` 跟着中字版落成 `ABC-123-中文.zh.srt`；名字里没有版本证据的字幕跟该分片的主版本（原版）。版本旗标跟重跑一样从文件名重读（`versionFlags`），不需要新列。
- **模板变量多了三个**：`version`（`""` / `-中文` / `-破解` / `-中文-破解`）、`chinese`、`uncensored`，`part` 在未标记分片时**渲染为空**（`{% if part %}` 因此能直接用）。`dir_template` 用同一套上下文，分片与版本在目录名上是空值——目录按番号，不按版本。
- **`task_files.part_index` 是名字里的分片标记，无标记就是 0**：任务落盘走 `task_files` 重建的组（重跑也从这里，见下条），若这里存 `Part.Index()` 的「无标记算第 1 片」，单文件作品会莫名变成 `-cd1`。「无标记算第 1 片」只留在排序里（`Part.Index()`）。
- **同一目标名只准一个来源**：`Apply` 写第二个进同一路径的文件时直接失败并点名两个源文件（`ABC-123-cd1-1080p.mp4` 与 `ABC-123-cd1-720p.mp4` 都解析成「cd1、原版」就是这种情况）。先写的那一个留在盘上，任务 `failed` ——静默覆盖是唯一看不见的结局。
- **任务重跑从 `group` 之后开始，不重跑 `parse`/`group`**：番号已经是 `tasks.number` 这一行事实（可能由人填过），重跑 `parse` 只会用文件名的推断去覆盖它。所以重跑用 `task_files` 重建 parse 产物（`Options.Seed`），再从 `scrape` 起跑；重扫目录、重跑剥离规则都不发生。
- **`scrape` 按波次并发调度**：每波 = 每个未满足字段在其候选列表里的**当前首选源**去重后的集合，并发各问一次；一波回来后，每个字段取「候选序里**最早**已答且贡献了的源」——**不靠返回顺序**（每字段一个游标，位置决定优先级）。仍有缺口 → 下一波；**无缺口即停**；每个源每番号**只问一次**。某个源抽风只影响它负责的字段：它的候选位置之后的人接手，步骤本身不失败。
- **图片字段两阶段**：`Scrape` 只给地址；只有「将要胜出」的那个源会被 `FetchImage`（**未胜出不取**），字节到手才算贡献（**胜出源全取、不封顶**）；取图失败 = 该源该字段无贡献 → 照常落到下一个候选（候选可能已经在手，或需要下一波去问）。
- **`concurrency.requests` 是进程级在飞请求上限**，落在唯一传输接缝 `Fetcher` 上（元数据与图片同一套）：先等域名桶、再占全局槽，等待与获取都尊重 ctx。
- **`source_runs.fields` 记的是这个源**赢下**的字段**（读合并后元数据的 `Sources`），不是它返回的全部字段：一个答案每个值都被更早的候选压掉的源，贡献是零。图片字段只有字节到手才会赢，所以这一条同时覆盖了「取不到图 = 没贡献」。
- **镜像切换时头也跟着换**：`fetch.Request.MirrorHeaders` 给每个镜像一份头，dmm 的 `Referer: https://video.{domain}/` 因此在切到备用域时不会还指着第一个域。
- **四个源都在这一版里**：`javdb` / `dmm` / `javbus` / `fc2`。`scrape.with.sources` 里写一个本二进制没有的 id 现在是**启动期错误**（typo 会让那个源永远不跑，静默什么都没刮），而不是被跳过——`mdc source test` 也照此点名说明本二进制知道哪些。

## 本票落下的实现裁决

写在这里，因为它们是实现时才暴露出来的洞：

- **`priority` 的出厂表与「换了源列表」的关系**：出厂表逐字段写明 14 个字段（只列声明了该字段的源）。文件里写了 `sources` 却没写 `priority` 时，逐字段序就是那份 `sources`——否则出厂表的 id 仍是候选，缩小的源列表会调度出零个源。写了 `priority` 时整体替换出厂表，未列字段回落到 `sources`（`ScrapeWith.UnmarshalYAML` 里做，避免 yaml 往默认 map 里合并）。校验分两处：`config` 查字段名/空表/重复 id，`internal/sources.Validate` 查 id 存在与 `Fields()` 声明——后者只有到「配置 + 二进制」都有时才成立，所以放在 `loadAll` 里，`config check` / `serve` / `run` / `source test` 一律拒绝。
- **`priority` 里允许写未配置的源**：出厂表可能点名某条流水线没选的源（`sources: [javbus]`），那是缩小的部署而不是 typo，所以只查能力，不要求 id 在 `sources` 里。
- **源侧归一化不留到下游**：`rating` 出 0–10（javdb `score`、dmm `reviewSummary.average` 各 ×2，`0` 视为未满足），`releasedate` 出 `YYYY-MM-DD`（dmm 的 RFC3339 先转，fc2 的 `2026/09/25` 先转），多值字段**不去重**（去重是引擎的 `tags`/`extrafanarts` 归一化，属第二批）。`rating` 的 `0` 判据落在 `scrape` 的 `contributes` 里，所以「源说了 0」不会把字段判成已满足。
- **`Fetcher` 支持 POST**：dmm 的 GraphQL 是唯一要 POST 的站点。`fetch.Request` 因此多一个 `Method` / `Body`（空 = GET），元数据与图片仍走同一个 Fetcher、同一套代理/限速/冷却/重试/取消。`concurrency.requests` 就挂在这个接缝上（**先等域名桶、再占全局槽**），所以任何步骤、任何源都超不过它。
- **两段式源的夹具**：`Dumper` 返回 `[]Capture`，`Capture` 带 `Leg`（`search` / `detail` / `content` / `search-<term>`）。夹具文件名带 leg（`javdb-SSIS-001-detail.json`），回放测试按请求本身选对应那份；`fixtures.json` 每行多一个 `leg`。
- **`mdc source test` 不再要求源在该流水线的 `sources` 里**：它按 id 取源的旋钮（流水线列了就用列的那份），没列就用出厂默认。命令问的是「这个源现在说什么」，不该取决于哪条流水线碰巧选了它。

- **`scrape.with.sources` 一项两形**：spec §6 说它是有序列表，§7 说源级旋钮「挂在 `scrape.with.sources.<id>` 下」——同名而形状不同。取「列表项可为字符串或对象」，于是 `sources: [javdb, {id: dmm, timeout: 30s}]` 既保住有序，又让旋钮跟着自己的 id 走。校验覆盖空 id、重复 id、未知旋钮、负值。
- **`filter.when` 四个键互斥**：spec 只要求「未知键报错」，但一条规则同时写 `ext_in` 与 `min_size` 时语义未定义。互斥把未定义变成启动期错误；要同时卡两个条件就写两条规则。
- **`${VAR}` 插值后按内容重判类型**：否则 `retry_limit`、`concurrency.*` 这类数值键无法从环境变量设置，「flag > ${VAR} > 文件 > 默认」在数值键上就不成立。加引号的标量仍是字符串。
- **每个步骤的 `with` 都有默认**（见上表），`with` 整段省略 = `with: {}`。
- **`/healthz` 是 `/api/health` 的别名**：k8s 惯例探针路径，一行代码省掉部署侧的 rewrite。`mdc version` / `mdc help` 同理是常规便利，不在 spec 的子命令面里。
- **`with` 结构体每步新建**：registry 存的是构造函数而不是共享实例，否则一个文件的合法值会漏进另一个文件，校验结果取决于目录读取顺序。
- **health 的 nil 走接口**：`api.Store` 存 `nil *store.Store` 是非 nil 接口，处理器会 panic 而不是回 503，所以 CLI 用独立变量并只在打开成功时赋值。
