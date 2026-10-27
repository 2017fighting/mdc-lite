# 0007. 模板统一用 gonja v2：NFO 开 autoescape，路径关转义

- 状态：Accepted
- 日期：2026-10-27
- 来源：wayfinder 票「NFO 模板语言与 Jellyfin/Emby 方言」（#14）、「配置文件 schema 与校验」（#8）

## 背景

README 说 NFO 用 jinja2 语法。候选里 `pongo2` 是 Django 语法（`join:", "` 冒号传参、`forloop.Counter`、无 `trim`/`map`/`tojson`、无 `{% for %}...{% else %}`），`text/template` 连 `{% %}` 和过滤器都没有 —— 选后两者等于改掉 README 的承诺。另外字段值里一个 `&` 就会让 XML 非法，而两家媒体服务器解析器都会吞掉异常、静默不应用任何元数据。

## 决定

- **模板引擎 = gonja v2**（`github.com/nikolalohinski/gonja/v2`，MIT，活跃）；**路径模板也用它**。
- **NFO 环境开 autoescape**；**路径环境关转义** + 渲染后逐段净化。
- 两个环境**必须各自 `config.New()` 构造**（`gonja.DefaultConfig` 是包级可变全局，改全局会串味）；`StrictUndefined` 保持默认 `false`（可选字段缺失渲染成空串正是要的）。
- 模板来源三选一：内建名（`default`）/ `@文件`（相对 `templates_dir`，禁 `..` 与绝对路径）/ 内联字符串；能力面全开、不沙箱。
- 只写 `movie.nfo`（`<番号>.nfo` 在多分盘目录里会被静默忽略）；`<outline>` 是死标签（简介只写 `<plot>`）。

## 替代方案

- `pongo2`：被否 —— Django 语法，不是 Jinja2。
- `text/template`：被否 —— 无过滤器，写法要整体改写。
- 自定义迷你模板：被否 —— 用户要求 jinja2 语法，自造语法没有生态。

## 后果

- 路径模板的变量面（14 字段 + `number` + `part`）必须在启动时校验，未知变量启动即拒。
- 路径值里的 `/` 变成 `_`，用户数据不能凭空造出目录层级。
- 上游 `go-yaml/yaml` 已归档：**解析库选型**（自维护 fork / `goccy/go-yaml`）留给实现阶段，只要求「严格 + 位置 + 锚点可用」三条。
