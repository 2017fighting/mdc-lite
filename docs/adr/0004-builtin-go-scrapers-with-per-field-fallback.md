# 0004. 只做内置 Go 刮削器，调度照 README 的 per-field fallback

- 状态：Accepted
- 日期：2026-10-27
- 来源：wayfinder 票「刮削器接口契约与首批源」（#5）、「首批刮削源现状核查」（#13）

## 背景

旧仓有声明式 YAML 源（javbus/airav）与原生 Go 源（javdb/dmm/fc2/avbase）两套，还有插件机制；原版则全是内置开关。新仓要的是「轻量、透明、可测」。

## 决定

- **只做内置 Go 源**（registry `map[string]Source`，配置按 id 引用，**无插件**）；首批 4 个：`javdb` / `javbus` / `fc2` / `dmm`。
- 源接口**两阶段**：`Scrape(ctx, Number) → SourceResult{Found, SourceFields}` + `FetchImage(ctx, ImageRef) → []byte`；**解码归源、魔数嗅探 `Ext` 归引擎**。
- 三态 `(err, Found)` → `ok` / `notfound` / `failed`；`notfound` 用**正向标记**判（javbus 404 / fc2 `items_notfound_wp` / javdb 精确匹配后无候选 / dmm 零候选）。
- 调度照 README 的 per-field fallback：全局序 + `priority[field]`（**完整列表替换**）、**波次并发**、无缺口即停；`Fields()` 只做启动校验 + 前端对照，**运行时不跳过任何源**；**不加适用性跳过、不加字段屏蔽**。
- 图片**胜出源全取、未胜出不取、不封顶**；取图失败 = 该源该字段无贡献 → 照常 fallback。
- **源级不重试**；重试/退避/代理/出口全归 Fetcher。

## 替代方案

- 声明式 YAML 源引擎与用户自定义源：已划出范围（grilling 裁决）。
- 外部进程 / WASM 插件：已划出范围。
- 适用性跳过（按番号家族跳过源）：被否 —— 旧仓的 `^[A-Z]+$` 之类会静默少请求，代价是「为什么这个字段没有」无法回答。

## 后果

- 加一个源 = 写一个 Go 实现 + 注册 + 声明 `Fields()`；不需要新 DSL。
- 每源留痕必须能回答「它为什么没贡献字段」（`source_runs` 的 `fields` 只记实际贡献）。
- 站点结构变了要发版（常量与选择器都在代码里）。
