# NFO 模板语言与 Jellyfin/Emby 方言

> wayfinder 票 [#14](https://github.com/2017fighting/mdc-lite/issues/14) 的调研产物。
> 证据等级沿用旧仓约定：**A** = 源码逐字 / 官方文档明示；**B** = 官方 issue 实测报告；
> **C** = 社区推断或未实测。**未验证** 一律显式标注，不写成结论。
>
> 调查方式：直读两家服务器源码（`raw.githubusercontent.com`，抓取日 2026-10-26，均为各自
> `master`）+ 官方文档 + 把两个 Go 模板库装进临时模块**实际执行**一整套模板（不是读 README 转述）。
> **未对真实媒体服务器实测**；所有服务器行为结论来自源码与文档，真机验证归验收阶段。

---

## 一、结论速览

1. **模板方案选 `gonja v2`**（`github.com/nikolalohinski/gonja/v2`，MIT，活跃维护，最新 tag `v2.9.1`）。
   它是三者里**唯一真的是 Jinja2 语法**的引擎，实测覆盖循环 / 条件 / 过滤器 / 宏 / 继承 / `loop.*`
   / `namespace` / `tojson` 等 README 想要的全部能力。`pongo2` 是 **Django 语法**不是 Jinja2，
   `text/template` 连 `{% %}` 和过滤器都没有——选它们等于改掉 README 的承诺。
2. **`movie.nfo` 是唯一正确的 NFO 文件名**，单视频 / 多分盘 / 多版本通吃，Jellyfin 与 Emby
   两家的候选列表都覆盖它（顺序不同，但都命中）。`<番号>.nfo` 在多分盘目录里是静默失败。
3. **`<outline>` 在 Jellyfin 和 Emby 都不存在**——两家的解析器都没有 `case "outline"`。
   Kodi 系的 `<outline>` 写进去没有任何效果，简介只能写 `<plot>`。
4. **`<rating>` 在 Jellyfin 里落到 `CommunityRating`（社区评分），不是用户个人评分**。
   NFO 没有任何标签能设置 Jellyfin 的用户评分。这一位要按「外部评分」而不是「我的评分」来用。
5. **「文件基名以目录基名开头」是 Jellyfin 聚合视频的前提，与 NFO 被发现是两件事**：
   NFO 靠固定名 `movie.nfo` 被发现，视频靠前缀匹配被聚合成一个条目。校验器两条都要管。
6. **渲染 NFO 时必须打开引擎的自动转义**（gonja 默认关、pongo2 默认开）。字段值里一个 `&`
   就能让整份 XML 非法；`&amp;`/`&lt;`/`&#34;` 都是合法 XML 字符引用。

---

## 二、Go 侧模板方案

### 2.1 维护状态与许可（A）

| | gonja | pongo2 | text/template |
|---|---|---|---|
| 模块 | `github.com/nikolalohinski/gonja/v2` | `github.com/flosch/pongo2/v6` | 标准库 `text/template` |
| 语法 | **Jinja2** | Django（`doc.go` 自述 "Django-syntax like template-engine"） | Go 自己的 `{{ }}` |
| Star | 173 | 3081 | — |
| Archived | false | false | — |
| 最近 push | 2026-10-12 | 2026-05-02 | Go 1.27.2 |
| 最新 tag | `v2.9.1` | `v6.1.0`（另有 `v7.0.0-alpha.1` 预发布） | — |
| License | MIT | MIT | BSD-3-Clause（Go 自身） |
| 维护状态 | 活跃；最近提交集中在 Jinja2 兼容性修复 | 活跃但节奏慢；v7 仍是 alpha | 稳定 |

- gonja 是 `noirbizarre/gonja` 的 fork；原仓最后 push **2023-09-27**，gonja v2 的 README 明说
  「`v1.X.X` versions are not maintained」（`README.md` § Migrating from v1 to v2）。
  也就是说上游已停，**维护中的是 `NikolaLohinski/gonja` v2 这条线**。
- 出处：GitHub REST API `repos/{owner}/{repo}`（star / archived / license / pushed_at）、
  `repos/.../tags`；本地模块 `$GOMODCACHE/github.com/nikolalohinski/gonja/v2@v2.9.1/README.md`、
  `.../flosch/pongo2/v6@v6.1.0/doc.go`、`.../pongo2/v6@v6.1.0/context.go:11`。

### 2.2 语法覆盖度（A，实测）

验证方式：临时模块引入两个库，把同一批模板跑一遍。**已实际执行**，结果如下。

**gonja v2.9.1 —— 全部通过：**

| 能力 | 实测模板 | 结果 |
|---|---|---|
| 变量 / 属性 / 下标 | `{{ a.Name }}`、`{{ fields["plot"] }}`、`{{ tags[0] }}` | ✅ |
| `{% for %}` + `loop.*` | `{% for a in actors %}{{ loop.index }}:{{ a.Name }}{% endfor %}` | ✅ |
| `loop.first/last/index0/revindex/length/cycle` | 逐个验证 | ✅ |
| `{% for %}...{% else %}` | 空列表走 else | ✅ |
| `{% for x in xs if cond %}`（循环内过滤） | `{% for t in tags if t != "t2" %}` | ✅ |
| 元组解包 | `{% for i, v in items %}`（`items` 是 `[][2]string`） | ✅ |
| `{% if %}/{% elif %}/{% else %}` | ✅ |
| 过滤器链 + 参数 | `{{ tags \| join(", ") }}`、`{{ x \| default("d") }}`、`{{ title \| upper \| trim }}` | ✅ |
| 过滤器 | `join default length sort sort(attribute=) map(attribute=) selectattr groupby dictsort tojson replace trim truncate wordwrap int float escape unique abs batch slice sum min max` 等 **54 个**（`builtins/filters.go` 的 `Filters` map）+ **39 个 test**（`builtins/tests.go`） | ✅ |
| 条件表达式 | `{{ "yes" if has_series else "no" }}` | ✅ |
| `{% set %}` / 块式 set / `namespace()` | 循环里累加计数 | ✅ |
| `{% macro %}` + 调用 + `*args/**kwargs` | 生成 `<actor>` 片段 | ✅ |
| `{% extends %}` + `{% block %}` + `{% include %}` + `{% import %}` | 用文件系统 loader 验证继承 | ✅ |
| `{% raw %}` / `{# #}` / `{% autoescape true %}` | 原样输出 / 注释 / 切换转义 | ✅ |
| `{% do %}` / `{% break %}` / `{% continue %}` / `{% call %}` / `{% trans %}` / 递归 `for` | 控制结构集已注册 | ✅ |
| `range()` / `dictsort` / `lipsum` / `cycler` / `joiner` | 全局函数 | ✅ |

- 控制结构清单出处：`.../gonja/v2@v2.9.1/builtins/control_structures/all.go`
  （`autoescape block extends filter for from if import include macro raw set with do break
  continue trans call`）。
- 过滤器清单出处：`.../builtins/filters.go`；测试清单 `.../builtins/tests.go`。
- `loop.*` 字段出处：`.../builtins/control_structures/for.go:34-90`。
- 已知与 Python Jinja2 的差异（库自己写在 README § Limitations）：`format` 用 Go 的 `fmt`
  语法而不是 Python 的 `str.format`；`escape` 立即生效（因此没有 `force_escape`）；
  只实现了 Python 内建类型方法的子集。这三条对「生成 XML」没有影响。

**pongo2 v6.1.0 —— 同一批 Jinja2 模板大面积失败：**

| Jinja2 写法 | pongo2 结果 |
|---|---|
| `{{ tags \| join(", ") }}` | **解析错**（Django 用 `join:", "` 冒号传参） |
| `{{ missing \| default("x") }}` | **解析错**（Django 用 `default:"x"`） |
| `{{ title \| trim }}` | **`Filter 'trim' does not exist`** |
| `{{ "a" if x else "b" }}` | **解析错**（无条件表达式） |
| `{% for %}...{% else %}` | **解析错**（`Tag 'else' not found`） |
| `loop.index` / `loop.first` / `loop.last` | **渲染出空**（Django 叫 `forloop.Counter` / `forloop.First`） |
| `range(3)` | **渲染出空** |
| `{{ fields.items() }}` | **渲染出空**（`{% for k,v in fields %}` 可用） |
| `{% set %}` / `{% macro %}` / `{% extends %}`+`{% block %}` | ✅（Django 有对应物） |
| `{% if %}/{% elif %}/{% else %}` / 下标 / 属性 / `{# #}` | ✅ |

pongo2 用**它自己的 Django 方言**能表达等价物（`join:", "`、`forloop.Counter`），
但它**不是 jinja2 语法**。选它就必须把 README 的「采用 jinja2 语法」改成「采用 Django 语法」，
用户也没法直接搬社区里现成的 Jinja2 NFO 模板。
- 出处：本地模块 `.../pongo2/v6@v6.1.0/tags_*.go`（`RegisterTag("for"|"if"|"extends"|"block"|
  "macro"|"include"|"import"|"set"|"with"|"autoescape"|"filter"|"cycle"|"firstof"|"spaceless"|
  "templatetag"|"widthratio"|"lorem"|"now"|"ssi"|"comment"|"ifchanged"|"ifequal"|"ifnotequal")`）
  与 `filters_builtin.go`（`add capfirst center cut date default escape first join length
  lower upper slice split title truncatechars truncatewords wordwrap yesno` 等，**无 `trim` /
  `map` / `selectattr` / `groupby` / `unique` / `tojson`**）。

**`text/template`（Go 1.27.2）—— 全部通过，但每一处都要换写法：**

| README 想要 | text/template 写法 | 备注 |
|---|---|---|
| `{% for a in actors %}` | `{{ range $i, $a := .actors }}...{{ end }}` | 索引变量叫 `$i`，没有 `loop.*` |
| `{% if %}` | `{{ if }}...{{ else }}...{{ end }}` | 比较要函数：`{{ if gt .Runtime 60 }}` |
| `{{ x \| join(", ") }}` | 需要自注册 `FuncMap`，且管道把值传**最后一个参数** | `strings.Join` 参数顺序相反，要包一层 |
| `{{ x \| default("d") }}` | 需要自注册 `default` | 缺失 key 在 `map[string]any` 上得到 `<no value>`，会**执行期报错** |
| `{% extends %}`/`{% block %}` | `{{ define }}` + `{{ template }}` / `{{ block }}` | 没有「子模板覆盖父模板」的等价物 |
| `{% macro f(a) %}` | `{{ define "f" }}` + `{{ template "f" . }}` | 单参数、无默认值/关键字参数 |
| 自动转义 | **`text/template` 不转义** | `html/template` 才转义，但它是 HTML 上下文感知，不是为 XML 设计 |

结论：`text/template` 能生成同样的字节，但它把「用户写模板」变成「用户写 Go 模板语法」，
README 的 jinja2 承诺作废，社区模板不能复用；并且没有内置过滤器、没有真正的继承。
- 出处：`https://pkg.go.dev/text/template`；实测见附录。

### 2.3 XML 转义：一个必须做的配置（A，实测）

同一份数据 `plot = 'A & B <tag> "quoted" \'apos\''`：

```
gonja 默认（AutoEscape: false）  <plot>A & B <tag> "quoted" 'apos'</plot>          ← XML 非法
gonja AutoEscape: true           <plot>A &amp; B &lt;tag&gt; &#34;quoted&#34; &#39;apos&#39;</plot>  ← 合法
pongo2 默认（autoescape = true）  <plot>A &lt;b&gt; &amp; &quot;c&quot;</plot>              ← 合法
text/template                    <plot>A & B <tag> "quoted" 'apos'</plot>          ← XML 非法
```

- gonja 默认关：`.../gonja/v2@v2.9.1/config/config.go:48`（`AutoEscape: false`）；
  打开后 `{{ plot | safe }}` 可显式放行。
- pongo2 默认开：`.../pongo2/v6@v6.1.0/context.go:11`（`var autoescape = true`），
  `pongo2.SetAutoescape(false)` 可关。
- **建议：渲染 NFO 时开自动转义**。这不是可选项——源站简介里出现 `&`、`<`、引号是常态，
  不转义会产出服务器读不了的 XML（`XmlReader` 直接抛 `XmlException`，两家解析器都吞掉异常、
  静默不应用任何元数据）。

### 2.4 gonja 的接入注意点（A，实测）

- `gonja.FromString` / `FromBytes` / `FromFile` 都固定用全局 `gonja.DefaultConfig`。
  要用自定义配置（`AutoEscape: true`），要么改全局
  （`gonja.DefaultConfig` 是 `*config.Config`，可写），要么照 `examples/advanced_configuration`
  用 `exec.NewTemplate(rootID, config, loader, environment)` 自己拼。**建议后者**，
  全局可变配置在并发渲染时是个坑。
- `StrictUndefined` 默认 `false`（`config/config.go:49`）：未知变量渲染成空串。
  这对「可选字段没爬到时整段不输出」正好合适；`true` 会让缺字段直接报错，不要开。
- 空字段的推荐写法是 `{{ x | default("") }}` 或 `{% if x %}`，与 `StrictUndefined: false` 配合。
- 未决的小风险：gonja 有一个**未关闭**的 issue「Unbounded `strings.Repeat` in Multiply operator
  allows OOM DoS」（2026-06-22）。模板是本地用户自己写的（信任模型是配置文件，不是多租户），
  风险可接受，但值得记一笔。

### 2.5 README 里那行输出映射写法要改（A）

README「输出」一节写的是：

```
{ for idx,extrafanart_i in extrafanarts } => {config.OUTPUT_DIR}/{number}/extrafanart/fanart{idx}.png
```

- **`{ for ... }` 不是合法 Jinja2**，Jinja2 是 `{% for ... %}`；`{config.OUTPUT_DIR}` 是配置插值，
  不是模板变量。这行是伪代码，落规格时得改成真正的模板（或在 Go 里直接生成路径）。
- 实测：`{% for idx, f in fanarts %}` 对 `[]string` 解包得到的是 `(值, 空)`，不是 `(下标, 值)`。
  要下标必须用 `loop.index` / `loop.index0`：`extrafanart/fanart{{ loop.index }}.png`（✅ 实测通过）。
- 建议：**图片路径不要交给用户模板**，整理层按固定布局产出（旧仓 ADR-0007 就是这个分工）；
  用户模板只管 `movie.nfo` 的**内容**。这样也避免用户写出破坏 Jellyfin 图片发现规则的文件名。

---

## 三、Jellyfin / Emby 读 NFO 的实际要求

### 3.1 文件名与读取顺序（A）

**Jellyfin** —— `MediaBrowser.XbmcMetadata/Savers/MovieNfoSaver.cs:47-69`（逐字）：

```csharp
internal static IEnumerable<string> GetMovieSavePaths(ItemInfo item)
{
    var path = item.ContainingFolderPath;
    if (item.VideoType == VideoType.Dvd && !item.IsPlaceHolder)
        yield return Path.Combine(path, "VIDEO_TS", "VIDEO_TS.nfo");

    // only allow movie object to read movie.nfo, not owned videos (which is itemtype video, not movie)
    if (!item.IsInMixedFolder && item.ItemType == typeof(Movie))
        yield return Path.Combine(path, "movie.nfo");

    if (!item.IsPlaceHolder && (item.VideoType == VideoType.Dvd || item.VideoType == VideoType.BluRay))
        yield return Path.Combine(path, Path.GetFileName(path) + ".nfo");
    else
        yield return Path.ChangeExtension(item.Path, ".nfo");
}
```

读取侧 `Providers/BaseVideoNfoProvider.cs:60-65` 取这份列表里**第一个真实存在**的文件。

**Emby** —— `MediaBrowser/Emby`（最后公开提交 2024-03-27）
`MediaBrowser.XbmcMetadata/Savers/MovieNfoSaver.cs:27-62`，**顺序与 Jellyfin 不同**：

```csharp
list.Add(Path.ChangeExtension(item.Path, ".nfo"));      // ← 先试 <视频基名>.nfo
if (!item.IsInMixedFolder)
    list.Add(Path.Combine(item.ContainingFolderPath, "movie.nfo"));   // ← 再试 movie.nfo
```

读取侧 `Providers/BaseVideoNfoProvider.cs:52-56` 同样是「取第一个存在的」。

**推论（A）**：

| 场景 | Jellyfin 命中 | Emby 命中 |
|---|---|---|
| 单视频 `ABC-123/ABC-123.mkv` + `movie.nfo` | `movie.nfo`（先于 `<基名>.nfo`） | `movie.nfo`（`<基名>.nfo` 不存在，回退） |
| **多分盘** `ABC-123/ABC-123-cd1.mkv` + `movie.nfo` | `movie.nfo` ✅ | `movie.nfo` ✅（先试 `ABC-123-cd1.nfo`，不存在） |
| 多分盘 + 写 `ABC-123.nfo` | **读不到**（候选里根本没有这个名字） | **读不到**（同上） |
| 单视频目录里 `ABC-123.nfo` 与 `movie.nfo` 并存且内容不同 | 读 `movie.nfo` | 读 `ABC-123.nfo` ← 两家分叉 |

→ **只写 `movie.nfo`**：两家都读，且不会分叉。

**多分盘时 `item.Path` 指向第一个分片**，这是 `ABC-123.nfo` 读不到的原因（B）：
- jellyfin/jellyfin#10493（2024-03-19 CLOSED NOT_PLANNED）：`Movie Title.nfo` + `-cd1/-cd2` →
  「Movie is recognized … with an additional part, as expected. **But nfo ist not parsed**」。
- jellyfin/jellyfin#15946（2026-01-05 建、当日 CLOSED NOT_PLANNED）：10.11.5 起
  `My Awesome Movie (2025).nfo` + `-disk1/-disk2` → 「it didn't read the nfo file from multi-part
  media folder」。
- jellyfin/jellyfin#17036（2026-06-08 CLOSED NOT_PLANNED）：请求为 multipart 加 `<基名>.nfo` 回退，
  被驳回。

**Jellyfin 官方文档** `https://jellyfin.org/docs/general/server/media/nfo/`（A）：
Movies 的合法文件名是 `movie.nfo`、`VIDEO_TS.nfo` 或「for DVDs `<filename of the movie>.nfo`」。
同一页还写明：「Local metadata will always be fetched and has priority over remote metadata
providers」「It's currently not possible to disable .nfo metadata」。

**Emby 官方文档没有 NFO 页面**（A，负面证据）：`emby.media/support/sitemap.xml` 里全部
`/support/articles/*` 中与元数据相关的只有 `Metadata-manager.html`，没有任何 NFO 说明页；
`Movie-Naming.html` 只讲文件夹式识别（「Emby will then use the folder name to determine the
movie」）。**Emby 侧的 NFO 行为以 `MediaBrowser/Emby` 公开源码为准；注意该仓库自 2024-03-27
后未再公开更新，现行 Emby Server 是闭源的**，所以 Emby 结论的时效性只能到此为止。

### 3.2 字段面：两家解析器逐字对照（A）

Jellyfin `MediaBrowser.XbmcMetadata/Parsers/BaseNfoParser.cs` 的 `switch (reader.Name)`
穷举出的标签：

```
actor aired aspect aspectratio biography communityrating country countrycode credits criticrating
customrating dateadded director displayorder durationinseconds enddate fanart fileinfo format3d
formed genre height language lastplayed localtitle lockdata lockedfields mpaa name originaltitle
playcount plot premiered rating ratings releasedate review runtime sortname sorttitle
streamdetails studio style subtitle tag tagline thumb title trailer uniqueid value video watched
width writer year
```

`MovieNfoParser.cs` 额外：`album artist id name set`。

Emby `MediaBrowser.XbmcMetadata/Parsers/BaseNfoParser.cs` 的标签集（同一份代码的早期分叉；
注意 `role`/`sortorder` 是 **`actor` 子节点**里的 case，不是顶层标签）：

```
actor aired aspectratio biography country countrycode credits criticrating customrating dateadded
director displayorder enddate fileinfo format3d formed genre language localtitle lockdata
lockedfields mpaa name originaltitle plot premiered rating releasedate review role runtime
sortorder sorttitle streamdetails studio style tag tagline title trailer video writer year
```

Emby `MovieNfoParser.cs` 额外：`album artist id name set`。

**差异（A，都是逐字比对的结果）：**

| 标签 | Jellyfin | Emby | 说明 |
|---|---|---|---|
| `plot` / `review` / `biography` | 三者同一分支，都写 `item.Overview` | 同 | 同文件里**最后一个出现者胜**（官方文档明示） |
| **`outline`** | **没有这个 case** | **没有这个 case** | 写了**完全无效**。简介只能写 `<plot>` |
| `premiered` / `releasedate` / `aired` / `formed` | 同一分支，都写 `PremiereDate` | 只有 `premiered` / `releasedate` | Jellyfin 多两个别名 |
| `year` | `ProductionYear`（`> 1850`） | 同 | `premiered` 分支会 `ProductionYear ??= releaseDate.Year` |
| `tag` / `style` | 同一分支，`AddTag`，可重复 | 同（`style` 与 `tag` 同一分支） | tag **不按 `/` 拆分** |
| `genre` | **按 `/` 拆分**成多值 | **按 `/` 拆分** | 值里含 `/` 会被切开 |
| `country` | **按 `/` 拆分** | **按 `/` 拆分** | 两家都拆 |
| `uniqueid` | 有（`type` 属性指定 provider） | **没有** | Emby 走 `<imdbid>` / `<tmdbid>` 之类 |
| `actor` | `GetPersonFromXmlNode()`，子节点 `name/role/type/order(或 sortorder)/thumb` | 同类方法，子节点 `name/role/type/sortorder` | 两家的 actor 子节点都认 `role`/`sortorder` |
| `set` | `ReadInnerXml()` + `ParseSetXml`，接受 `<name>` 子节点**或裸文本** | 同样两种都接受 | |
| `rating` | `item.CommunityRating` | `item.CommunityRating` | **不是用户评分**（见 3.4） |
| `customrating` | `item.CustomRating` | 同 | |
| `criticrating` | `item.CriticRating` | 同 | |
| `runtime` | `RunTimeTicks`（**分钟**，取空格前那一段） | 同 | |
| `thumb` / `fanart` | `FetchThumbNode`（`thumb` 有 `aspect` 属性） | **两个 case 都没有**（图片走本地发现，不读 NFO 内路径） | 见 3.6 |
| `ratings` | 有（多个 `rating` 子节点 + `name` 属性） | **没有** | Jellyfin 独有 |

出处：Jellyfin `BaseNfoParser.cs`（`sorttitle` 297-299、`plot/review/biography` 300-304、
`customrating` 415-417、`runtime` 418-425、`actor` 482-489、`rating` 534-542、`ratings` 543-545、
`aired/formed/premiered/releasedate` 556-568、`genre` 576-593、`tag/style` 595-605、
`uniqueid` 607-625、`thumb` 626-628、`fanart` 629+）、`MovieNfoParser.cs`（`set` 71-93、
`ParseSetXml` 117-155）、`MediaBrowser.Controller/Extensions/XmlReaderExtensions.cs:83-155`
（`GetPersonFromXmlNode`）；Emby `BaseNfoParser.cs`（`actor` 553-572、`country` 412-421、
`genre` 677-695、`style`/`tag` 695-705、`GetPersonFromXmlNode` 878-960、
provider id 兜底 721-737）、`MovieNfoParser.cs`（`set` 57-90）。

### 3.3 `outline` 不存在 —— 票面点名的字段之一要划掉（A）

票面把 `outline` 和 `plot` 并列。实测两家解析器的 case 列表里**都没有 `outline`**，
所以 `<outline>` 是死标签。Kodi 用 `<outline>`，Jellyfin/Emby 不用。
**结论：不写 `<outline>`，只写 `<plot>`。**

### 3.4 `<rating>` 的语义不是「用户评分」（A）

Jellyfin `BaseNfoParser.cs:534-542`：`case "rating"` → `item.CommunityRating`。
Emby 同样 → `CommunityRating`。官方文档的标签表里，用户数据只有
`watched` / `playcount` / `lastplayed`（「User data importing is only possible for a single user」），
**没有任何标签能写用户的个人评分**。

→ 旧仓字段表把「UserRating → `<rating>`」当映射，语义上是**外部评分**落进 Jellyfin 的
CommunityRating。落规格时要么改名（`rating` / `community_rating`），要么明确接受
「这个值在服务器上显示为社区评分」。

### 3.5 多分盘时的行为（A）

**Jellyfin**（`https://jellyfin.org/docs/general/server/media/movies/` § Multiple Parts）：

```
Movie Name (2010)
├── Movie Name-cd1.mkv
├── Movie Name-cd2.mkv
└── Movie Name-cd3.mkv
```

- 支持的 part 类型：`cd` `dvd` `part` `pt` `disc` `disk`；
- 支持的分隔符：空格 `.` `-` `_`；`<parttype>` 与 `<partnumber>` 之间可省；
- `<partnumber>` **可以是任意数字**，或字母 `a`-`d`；
- 「Multiple parts can also be used in conjunction with multiple versions (stacked multi-versions)」，
  但官方给的组合示例是**剧集**（`S01E01 - 720p - Part 1.mkv`），电影侧没有示例。

**Emby**（`https://emby.media/support/articles/Movie-Naming.html` § Split video files）：

- stacking 后缀：`part#` `cd#` `dvd#` `pt#` `disk#` `disc#`，**`#` 只能是 1–9 或 A–D**；
  另有 `moviename#.ext`（`#` 为 A–D）；
- 「Split videos require all parts be in the same movie folder with **no other videos present in
  that folder**」。

→ **两家的 part 编号上限不同**（Jellyfin 任意数字 vs Emby 1–9 / A–D）。番号场景下
`-cd1`/`-cd2` 够用；但如果一个任务分片超过 9 个，Emby 不再聚合。**登记为验收项**。

→ 多分盘 + 多版本同目录共存：Jellyfin 官方只给了剧集示例，Emby 明文说「no other videos present」。
这条旧仓已登记为实测项（`.scratch/open-mdcng/research/05-nfo-emby-jellyfin.md` § 四、§ 七），
本轮**未验证**，不在源码层面能定论。

### 3.6 图片命名（A，源码 + 官方文档）

**Jellyfin 官方表**（`https://jellyfin.org/docs/general/server/media/movies/` § Metadata Images，
Movies 列 ✅）：

| Filename | Type | 备注 |
|---|---|---|
| `poster` / `folder` / `cover` / `default` / `movie` | Primary | 可裸名或后缀名（`movie-poster.jpg`） |
| `backdrop` / `fanart` / `background` / `art` | Backdrop | 多张直接编号：`backdrop-1.jpg`、`backdrop2.jpg` |
| `extrafanart`（文件夹） | Backdrop | |
| `banner` | Banner | |
| `logo` / `clearlogo` | Logo | |
| `landscape` / `thumb` | Thumb | |

**Jellyfin 源码** `MediaBrowser.LocalMetadata/Images/LocalImageProvider.cs`：
- 视频的 Primary 候选顺序：先试 `<视频基名>`（`FileNameWithoutExtension`），再 `poster` `folder`
  `cover` `default` `movie`（`_videoImageFileNames`，行 55-62；主逻辑 258-305）。
  → **若同时存在 `ABC-123.jpg` 与 `poster.jpg`，前者赢**。我们只写 `poster.jpg`，不写
  `<番号>.jpg`，就不会撞。
- Thumb：先 `landscape` 再 `thumb`（行 242-249）。
- **`extrafanart/` 里的图片文件名不参与匹配**（行 347-357）：
  `PopulateBackdropsFromExtraFanart` 把该目录下**所有**受支持扩展名的图片都当作 Backdrop。
  也就是说 Jellyfin 侧 `fanart1.jpg` 只是惯例，`foo.jpg` 也认。

**Emby 官方表**（`https://emby.media/support/articles/Movie-Naming.html` § Video images）：

| Image Type | Supported file names |
|---|---|
| Primary | `{name}.ext`、`{name}-poster.ext`、`{name}-cover.ext`、`{name}-default.ext`、`{name}-movie.ext`、`folder.ext`、`poster.ext`、`cover.ext`、`default.ext`、`movie.ext` |
| Backdrop | `backdrop.ext, backdropX.ext`、`fanart.ext, fanart-X.ext`、`background.ext, background-X.ext`、`art.ext, art-X.ext`、**`extrafanart (subfolder)/fanartX.ext`** |
| Banner | `{name}-banner.ext`、`banner.ext` |
| Logo | `{name}-clearlogo.ext`、`clearlogo.ext`、`{name}-logo.ext`、`logo.ext` |
| Thumb | `{name}-thumb.ext`、`{name}-landscape.ext`、`thumb.ext`、`landscape.ext` |
| Disc | `{name}-disc.ext`、`{name}-cdart.ext`、`disc.ext`、`cdart.ext` |

Emby 支持的图片扩展名（官方文档）：「jpg , jpeg , png , gif and tbn」；
Jellyfin 的图片扩展名在源码里：`.png .jpg .jpeg .webp .tbn .gif .svg`
（`MediaBrowser.Controller/Entities/BaseItem.cs:58-59`）。
→ **两家的交集是 `jpg/jpeg/png/gif/tbn`；`webp`/`svg` 只有 Jellyfin 认**。落盘用 jpg/png 最稳。

**两边都认的公共子集**（推荐落盘清单）：

| 我们写 | Jellyfin | Emby | 结论 |
|---|---|---|---|
| `poster.jpg` | Primary ✅ | Primary ✅ | 裸名两边都认 |
| `fanart.jpg` | Backdrop ✅ | Backdrop ✅ | 裸名两边都认 |
| `thumb.jpg` | Thumb ✅ | Thumb ✅ | 裸名两边都认 |
| `logo.png` / `banner.jpg` | Logo / Banner ✅ | ✅ | 裸名两边都认 |
| `extrafanart/fanart1.jpg`、`fanart2.jpg`… | ✅（目录里任何图片都认） | ✅（文档明示 `fanartX.ext`） | **唯一两家都明示的多背景图写法** |

→ 多背景图**只写 `extrafanart/`**，不双写根层 `backdrop-N`（根层多张会让文件列表变长，且
`extrafanart/` 已覆盖两家）。图片一律**裸名**，不用 `{name}-poster.jpg` 前缀式
（多分盘目录里会随分片数量膨胀，且裸名已够）。

### 3.7 `IsInMixedFolder`：`movie.nfo` 被读的隐含条件（A，源码）

Jellyfin `MovieNfoSaver.cs:56` 的 `movie.nfo` 分支带 `!item.IsInMixedFolder` 条件。
本轮把它的计算读到底了（旧仓研究里标的是「验收期确认」）：

```csharp
// Emby.Server.Implementations/Library/Resolvers/Movies/MovieResolver.cs:287
var isInMixedFolder = resolverResult.Count > 1 || parent?.IsTopParent == true;

// 同文件 478-496：目录解析出恰好一个条目、且没有照片、没有「其他子目录」时，重置为 false
if (!isPhotosCollection && result.Items.Count == 1) {
    var hasPhotos = photos.Any(...);
    var hasOtherSubfolders = multiDiscFolders.Count > 0;
    if (!hasPhotos && !hasOtherSubfolders) {
        movie.IsInMixedFolder = false;
        ...
    }
}
```

- `parent?.IsTopParent`（`BaseItem.cs:746`）**只对 plugin 文件夹 / channel / LiveTV collection 为真**，
  普通 Movies 库根（`CollectionFolder`）不是 top parent → 这一项在实际布局里恒为 `false`。
- 所以实际判定退化成 **`resolverResult.Count > 1`**：一个目录解析出**多个不同作品**才算 mixed。
  我们的「一番号一目录、里面只有一个作品（单视频 / 多分盘 / 多版本都被聚合成一个条目）」
  → `Count == 1` → `IsInMixedFolder = false` → **`movie.nfo` 会被读**。
- 顺带一个源码级细节：`extrafanart` **不在** `AllExtrasTypesFolderNames` 里
  （`Emby.Naming/Common/NamingOptions.cs:714-716`，名单来自 495-620 的 `VideoExtraRules`：
  `trailers backdrops theme-music behind the scenes deleted scenes interviews scenes samples
  sample shorts featurettes extras extra other clips trailer sample theme -trailer .trailer
  _trailer`）。因此在 `FindMovie` 里它会被算进 `multiDiscFolders`（`MovieResolver.cs:427`）。
  这只在「视频直接躺在库根、且 `parent.IsTopParent == true`」时才有影响——正常布局下
  `IsInMixedFolder` 本来就是 `false`。**结论：不影响我们的布局，但不要指望把视频直接摊在库根。**
- ⚠️ 这条是**源码级结论，未在真机上验证**。若多分盘 + 多版本同目录导致 `Count == 2`，
  `movie.nfo` 会突然不被读——这正是旧仓登记的验收项。

### 3.8 「文件基名以目录基名开头」是 Jellyfin 的硬要求（A）

Jellyfin 官方文档（§ Multiple Versions）逐字：

> Each file **must** begin exactly with the base name of the parent folder - including any year
> and/or metadata provider IDs - before adding a version label. **This prefix must match
> character-for-character**; otherwise, the files will be treated as separate items.

同一页 § Naming：「The video files within the folder should have the same name as the folder.」

- 这是**前缀**匹配，不是相等：`ABC-123/ABC-123-cd1.mkv`、`ABC-123/ABC-123-流出.mkv` 都满足。
  只有 `ABC-123-流出/ABC-123.mkv`（目录名被加长）才破坏它。
- 这条规则服务的是**视频被聚合成一个条目**，不是 NFO 被发现（后者靠固定名 `movie.nfo`）。
  两件事互不替代，校验器两条都要管 —— 与旧仓
  `docs/adr/0006-nfo-fixed-filename.md:23` 的结论一致。
- Emby 的多版本要求不同：「Each version must begin with the folder name, followed by `" - "`」，
  且「Up to 8 different versions」。**Jellyfin 用 `-`/`.`/`_`/`[]` 分隔，Emby 要求 `" - "`**
  → 想两边都聚合，版本标签的分隔符要用 `" - "`（旧仓默认的 `-流出` 只满足 Jellyfin）。

---

## 四、模板变量集

### 4.1 旧仓的「24 个命名变量」到底是什么（A）

旧仓有两个不同的模板面，别混：

- **命名模板**（目录 / 文件名）的变量集在 `open-mdcng/config/naming.go:50` 的
  `TemplateVariables()`，共 **25 个**，注释（`config/naming.go:41-49`）明说
  「It is the 25 the spec settles on: the original tool's 24 plus `{translated_title}`」。
  原始 24 个的清单在 `.scratch/open-mdcng/issues/05-organize-metadata.md:25`：

  ```
  number publish_number series_name serial_number first_letter series category actor
  first_actor title originaltitle year director studio publisher runtime release
  source_filename filename source_path subtitle mosaic resolution
  ```

  （该行只列了 23 个却自称 24；`config/naming.go:41-49` 解释差的第 24 个是 **`part`**
  —— 多分盘分片序号。第 25 个是 `translated_title`。）
- **NFO 生成**在旧仓**不是模板**：`open-mdcng/organize/nfo.go:108` 的 `RenderNFO` 用
  `encoding/xml` 的结构体 marshal 直接产出，字段面写死在 `nfoMovie`（`organize/nfo.go:44`）。
  所以旧仓**没有**「NFO 模板变量集」这个东西——README 要求的 jinja2 NFO 模板是新东西。

### 4.2 建议：命名模板与 NFO 模板共用一套词汇（建议，非源码结论）

原则：**同一个变量名在两个模板面里含义相同**。命名模板用不到 `plot`/`tags`/`actors`，
NFO 模板用不到 `filename`/`source_path`/`first_letter`，但重叠部分必须一致，
否则用户要记两套词。做法：

- 取命名变量集作为**基础词表**（25 个），NFO 模板在其上**追加**它独有的字段，
  **不暴露** `filename` / `source_filename` / `source_path` / `first_letter`（在 NFO 里无意义）；
- 列表字段（`tags` / `actors` / `extrafanarts`）用**列表**传入，模板里 `{% for %}` 展开，
  而不是拼好的字符串（Jellyfin 的多值就是重复元素，不是分隔符拼接的字符串）；
- `part` 保留（NFO 里可能想写进 `<tag>` 或注释）；
- 变量名与字段名对齐用 `snake_case`（与旧仓命名变量一致），不要混 `camelCase`。

### 4.3 建议的最小 NFO 模板变量集

按 README「字段」一节的四类（必选 / 可选 / 处理后 / 内置）整理。**加粗** = NFO 模板比命名
变量集新增的。

| 变量 | 类型 | 来源（README 分类） | 说明 |
|---|---|---|---|
| `number` | string | 内置 | 番号 |
| `title` | string | 必选 | 源站标题（**确定性**，翻译不改它） |
| `translated_title` | string | 处理后 | 译文；翻译关时为空（**命名变量集里已有**） |
| `originaltitle` | string | 可选 | 原名；空则回落 `title` |
| **`sorttitle`** | string | 派生 | **建议由整理层算好**（= 番号前缀 + 原名），不交给用户拼 |
| **`plot`** | string | 必选 | 简介（**不要 `outline`**） |
| **`plot_cn`** | string | 处理后 | 译文简介 |
| `release` | string | 必选 | 发布日期（`YYYY-MM-DD`） |
| `year` | string | 派生 | 从 `release` 取前 4 位 |
| `runtime` | int | 可选 | 分钟 |
| **`rating`** | float | 可选 | 落 `<rating>` → **Jellyfin 读成 CommunityRating**（见 3.4） |
| `series` | string | 可选 | 落 `<set><name>` |
| `studio` | string | 可选 | 落 `<studio>` |
| `publisher` | string | 可选 | 无专用标签位 → 落 `<tag>`（旧仓写法 `发行: {publisher}`） |
| `director` | string | 可选 | 落 `<director>` |
| **`trailer`** | string | 可选 | 落 `<trailer>`，Kodi 格式受限 |
| **`tags`** | []string | 必选 | 落重复的 `<tag>`；**不按 `/` 拆分**（`genre` 才拆） |
| **`tags_cn`** | []string | 处理后 | 归一化后的标签 |
| **`actors`** | []{name, thumb} | 必选 | 落重复的 `<actor><name>`（+ 可选 `<thumb>`） |
| **`actors_cn`** | []string | 处理后 | 归一化后的演员名 |
| **`extrafanarts`** | []string | 必选 | **不进 NFO**，供图片落盘（建议由整理层直接处理，不进模板） |
| **`genres`** | []string | 可选 | ⚠️ 写 `<genre>` 会被 Jellyfin/Emby 按 `/` 拆；只有当值里确定无 `/` 才写 |

不建议暴露的命名变量：`filename`、`source_filename`、`source_path`、`first_letter`、
`resolution`、`subtitle`、`mosaic`、`category`、`serial_name`、`publish_number`、`serial_number`
——它们是**命名/调度**的词汇，NFO 内容里没有位置；要保留最多降到 `<tag>`。

---

## 五、最小 NFO 字段面建议

一份**最小且两家都认**的 `movie.nfo`（顺序无关，多值一律重复元素）：

```xml
<?xml version="1.0" encoding="UTF-8"?>
<movie>
  <title>ABC-123 显示标题</title>              <!-- 番号前缀，列表页可见 -->
  <originaltitle>ABC-123 原名</originaltitle>
  <sorttitle>ABC-123 原名</sorttitle>          <!-- 与 originaltitle 同值（旧仓 ADR-0017） -->
  <plot>简介</plot>                            <!-- 不写 outline -->
  <premiered>2024-01-02</premiered>            <!-- 双写，覆盖 Kodi 系歧义 -->
  <releasedate>2024-01-02</releasedate>
  <runtime>120</runtime>                       <!-- 分钟 -->
  <rating>8.5</rating>                         <!-- → CommunityRating，不是用户评分 -->
  <set><name>系列名</name></set>
  <studio>片商</studio>
  <director>导演</director>
  <tag>标签1</tag>
  <tag>标签2</tag>
  <tag>发行: 发行商</tag>                       <!-- 无专用位的字段降级为 tag -->
  <actor><name>演员 A</name><thumb>...</thumb></actor>
  <actor><name>演员 B</name></actor>
</movie>
```

**刻意不写：**

| 不写 | 原因（出处） |
|---|---|
| `<outline>` | 两家解析器都没有这个 case（3.3） |
| `<genre>` / `<country>` | 会被按 `/` 拆成多值（`BaseNfoParser.cs:576-593`）；标签用 `<tag>` 更安全 |
| `<uniqueid>` / `<id>` | 我们没有 IMDb/TMDb ID；写了会误导服务器拿去匹配错误的作品。两家行为不同：Jellyfin 认 `<uniqueid type=…>`，Emby **不解析 `<uniqueid>`**（只认 `<id>` 的 `IMDB`/`TMDB`/`TVDB` 属性与 `<imdbid>`/`<tmdbid>` 类标签，见 3.2） |
| `<dateadded>` | 让服务器记自己的入库时间 |
| `<fileinfo><streamdetails>` | 归后处理插件（ffprobe），整理层不探测 |
| `lockedfields` / `lockdata` / `watched` / `playcount` / `lastplayed` | 仅 Jellyfin 读且无 Emby 对应，NFO 会失去可移植性 |
| `<thumb>` / `<fanart>`（NFO 内的图片路径） | 图片走**文件名发现**（`poster.jpg` / `extrafanart/`），不在 NFO 里写路径；NFO 里写 URL 会**优先于**本地图片（官方文档：「Artwork defined in .nfo files with local paths or URLs have priority over remote image providers and images in the media folders」），是自找麻烦 |

**落盘清单（沿用旧仓结论，本轮复核过来源）：**

```
{number}/
├── movie.nfo                     固定名，每目录一份
├── poster.jpg                    裸名
├── fanart.jpg                    裸名
├── thumb.jpg                     裸名
├── logo.png / banner.jpg         可选
└── extrafanart/
    ├── fanart1.jpg               1 起编号；Jellyfin 认目录里任何图片，Emby 认 fanartX
    └── fanart2.jpg
```

---

## 六、未验证 / 未决

1. **真机行为全部未验证**：本轮只读源码与文档，没有起 Jellyfin / Emby 实例。
   所有 `IsInMixedFolder`、`movie.nfo` 是否被读、图片是否被认的结论都是源码级，
   真机验证归验收阶段。
2. **Emby 的时效性**：`MediaBrowser/Emby` 最后公开提交 **2024-03-27**，现行 Emby Server
   闭源。Emby 侧结论只对「最后一份公开源码」负责。
3. **多分盘 + 多版本同目录**：Jellyfin 官方只给剧集示例，Emby 明文要求「no other videos」。
   若该组合让 `resolverResult.Count > 1`，`movie.nfo` 会突然不被读。旧仓已登记为验收项，
   本轮未实测。
4. **`IsInMixedFolder` 未真机验证**；`extrafanart` 计入 `multiDiscFolders` 的实际后果
   （只影响「视频摊在库根」的场景）也未实测。
5. **Emby 多版本分隔符**：文档要求 `" - "`，Jellyfin 允许 `-`/`.`/`_`/`[]`。两边同时满足
   的写法未实测。
6. **`pongo2 v7.0.0-alpha.1`**：只看到 tag，没有 release notes；v7 的语法面未评估。
7. **gonja 的 `format` 过滤器用 Go 语法**、`escape` 立即生效、Python 内建方法只实现了子集
   （库自己列的 Limitations）。对生成 XML 无影响，但用户若从 Python 模板搬 `"%.2f" | format`
   会踩到。
8. **gonja 的 OOM issue**（未关闭，2026-06-22）未评估可利用性。
9. **票面点名的 `research/05-nfo-emby-jellyfin.md` 路径有误**：实际在
   `/root/clone/open-mdcng/.scratch/open-mdcng/research/05-nfo-emby-jellyfin.md`
   （`research/` 下没有这个文件）。`docs/adr/0006-*.md`、`docs/adr/0017-*.md`、
   `organize/nfo.go` 路径正确。

---

## 附录：验证方法（可复现）

- **服务器源码**：`raw.githubusercontent.com/jellyfin/jellyfin/master/...`、
  `raw.githubusercontent.com/MediaBrowser/Emby/master/...`（抓取日 2026-10-26）。
  关键文件：`MediaBrowser.XbmcMetadata/Savers/MovieNfoSaver.cs`、
  `.../Parsers/BaseNfoParser.cs`、`.../Parsers/MovieNfoParser.cs`、
  `.../Providers/BaseVideoNfoProvider.cs`、`MediaBrowser.LocalMetadata/Images/LocalImageProvider.cs`、
  `Emby.Server.Implementations/Library/Resolvers/Movies/MovieResolver.cs`、
  `Emby.Naming/Common/NamingOptions.cs`、`MediaBrowser.Controller/Providers/ItemInfo.cs`、
  `MediaBrowser.Controller/Entities/BaseItem.cs`。
- **官方文档**：`https://jellyfin.org/docs/general/server/media/nfo/`、
  `https://jellyfin.org/docs/general/server/media/movies/`、
  `https://emby.media/support/articles/Movie-Naming.html`。
- **issue**：`jellyfin/jellyfin#10493`、`#15946`、`#17036`（均为 CLOSED NOT_PLANNED）。
- **模板引擎实测**：临时模块（Go 1.27.2）引入
  `github.com/nikolalohinski/gonja/v2 v2.9.1` 与 `github.com/flosch/pongo2/v6 v6.1.0`，
  对同一批模板分别执行；`text/template` 用标准库跑同一批。测试程序是临时的，**不随本文件提交**。
- **旧仓引用**（只读）：`/root/clone/open-mdcng/organize/nfo.go`、
  `/root/clone/open-mdcng/config/naming.go`、
  `/root/clone/open-mdcng/docs/adr/0006-nfo-fixed-filename.md`、
  `/root/clone/open-mdcng/docs/adr/0017-title-slots-carry-the-number.md`、
  `/root/clone/open-mdcng/.scratch/open-mdcng/research/05-nfo-emby-jellyfin.md`、
  `/root/clone/open-mdcng/.scratch/open-mdcng/issues/05-organize-metadata.md`。
- **逆向资料**（`git -C /root/clone/open-mdcng show 8369f4d:<path>`）：
  `recovered/backend/config/fx_config_nfo.json`（NFO 配置 68 字段，含
  `enable_outline`/`enable_originalplot`/`enable_plot`/`tagline_format` 等开关）、
  `recovered/backend/api/DTO_FIELDS.md`（元数据 68 字段枚举，含 `Outline`/`OriginalPlot`/
  `Title`/`OriginalTitle`/`Tags`/`UserRating`/`UserVotes`/`Score` 等）。
  **注意：原版的 `enable_outline` 开关在 Jellyfin/Emby 上是空操作**（见 3.3），
  照搬旧配置面会继承一个假开关。
