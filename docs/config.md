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

每个键都有默认，所以 `with: {}` 与整段省略 `with` 等价。例外是 `output.with.root`：写盘步骤不写落点就是配置错误，不是默认值。

| 步骤 | 默认 |
|---|---|
| `scan` | `roots: []`、`exclude: []`（由触发注入覆盖） |
| `filter` | 无（`rules` 必填） |
| `group` | `max_distance: 2`、`max_files_for_levenshtein: 50`、`companion_extensions` 为字幕与图片扩展名 |
| `parse` | 无（`rules` 为空 = 不解析） |
| `scrape` | `sources` 为全局序 `[javdb, dmm, javbus, fc2]`、`timeout: 5m` |
| `images` | 无键 |
| `nfo` | `template: default` |
| `output` | `dir_template: {{ number }}`、`file_template: {{ number }}-cd{{ part }}`、`link: hardlink`、`required_fields: [title, fanart]`；**`root` 必填** |

`scrape.with.sources` 是有序列表，每项是**裸 id** 或**带 id 的对象**：`sources: [javdb, {id: dmm, timeout: 30s, domains: [dmm.co.jp]}]`。源级的四个旋钮（`proxy` / `domains` / `rate` / `timeout`）就挂在这一项里，位置与设置不分家。`priority.<field>` 是**完整替换**全局序，不是追加。

`group.with.sanitize` 是「字面量 / 正则」混合表：裸字符串按字面量剥，`r:` 前缀（如 `r:[-_. ]1080p$`）按 Go 正则剥。

`filter.with.rules[].when` 的四个键（`ext_in` / `ext_not_in` / `min_size` / `stem_matches`）**互斥**：一条规则同时写两个时语义未定义，所以启动即拒。要同时卡扩展名与体积就写两条规则。

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

## 本票落下的实现裁决

写在这里，因为它们是实现时才暴露出来的洞：

- **`scrape.with.sources` 一项两形**：spec §6 说它是有序列表，§7 说源级旋钮「挂在 `scrape.with.sources.<id>` 下」——同名而形状不同。取「列表项可为字符串或对象」，于是 `sources: [javdb, {id: dmm, timeout: 30s}]` 既保住有序，又让旋钮跟着自己的 id 走。校验覆盖空 id、重复 id、未知旋钮、负值。
- **`filter.when` 四个键互斥**：spec 只要求「未知键报错」，但一条规则同时写 `ext_in` 与 `min_size` 时语义未定义。互斥把未定义变成启动期错误；要同时卡两个条件就写两条规则。
- **`${VAR}` 插值后按内容重判类型**：否则 `retry_limit`、`concurrency.*` 这类数值键无法从环境变量设置，「flag > ${VAR} > 文件 > 默认」在数值键上就不成立。加引号的标量仍是字符串。
- **每个步骤的 `with` 都有默认**（见上表），`with` 整段省略 = `with: {}`。
- **`/healthz` 是 `/api/health` 的别名**：k8s 惯例探针路径，一行代码省掉部署侧的 rewrite。`mdc version` / `mdc help` 同理是常规便利，不在 spec 的子命令面里。
- **`with` 结构体每步新建**：registry 存的是构造函数而不是共享实例，否则一个文件的合法值会漏进另一个文件，校验结果取决于目录读取顺序。
- **health 的 nil 走接口**：`api.Store` 存 `nil *store.Store` 是非 nil 接口，处理器会 panic 而不是回 503，所以 CLI 用独立变量并只在打开成功时赋值。
