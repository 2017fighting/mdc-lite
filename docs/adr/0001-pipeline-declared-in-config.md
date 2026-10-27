# 0001. 步骤序列由配置文件声明，副作用步骤产出 Plan 由引擎统一 Apply

- 状态：Accepted
- 日期：2026-10-27
- 来源：wayfinder 票「Pipeline 步骤模型：步骤类型、产物契约与配置里的步骤序列」（#2）

## 背景

旧仓把阶段顺序硬编码（ingest → filter/group/parse → organize: scrape/images/nfo/link），配置只能开关，不能换序。新需求是「自由组合、增删步骤」。同时旧仓的 `Plan`/`Execute` 拆分（`organize/plan.go` + `organize/execute.go`，包注释 *Planning and effects are separate on purpose*）站得住，但旧仓**没有 organize 的 dry-run 入口**。

## 决定

- 步骤类型内置（Go），序列与参数由配置文件声明；每步声明 `Requires()` / `Produces()`，**启动时拓扑校验**（类型连通、至少一个落盘步骤、允许省略可选步骤、同 type 重复必须给 `name`）。
- V1 八类步骤：`scan` / `filter` / `group` / `parse` / `scrape` / `images` / `nfo` / `output`；第二批预留 `translate` / `normalize` / `refresh`。
- **副作用步骤只产出 `Plan`**（`Writes []PlannedWrite`），由引擎统一 `Apply`；`output` 是唯一写盘出口，`nfo` 只渲染文本。
- 步骤间传**内存强类型产物**（8 个边界结构体），视频只带路径、图片字节在内存过一手；中间产物不落库。
- `scan` 放进流水线第一步（不是触发层的私事），因此预览与建任务共用同一段前缀。

## 替代方案

- **(a) 硬编码阶段 + 开关**（旧仓）：被否 —— 不能换序。
- **(b) 声明式 YAML 源 / 外部插件**：已划出范围（见 spec 的 Out of Scope）。
- **(c) 每步自带 `Run(dryRun bool)`**：被否 —— 写盘逻辑分散，预览与创建容易分叉。

## 后果

- 配置成为公开 API（`type` 名与 `with` 键面改起来贵）。
- 错误在启动时暴露，而不是运行到第 6 步才炸。
- 预览 / dry-run / 建任务天然共用一份代码；`--until <step>` 与 `--dry-run` 免费得到。
