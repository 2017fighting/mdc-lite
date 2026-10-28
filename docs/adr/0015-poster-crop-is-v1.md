# 0015. `poster.crop` 进 V1：poster 缺时从 fanart 右侧裁 2:3

- 状态：Accepted（修正 ADR-0006 与「字段清单与元数据模型」的原始裁决）
- 日期：2026-10-28
- 来源：wayfinder 票 #4（原裁「第二批」）→ 原型复审（`prototype/frontend-views` @ `f6d021c`）→ spec #17 修订

## 背景

README 第 113 行本就把 `poster.crop` 写成产物：「根据fanart右侧裁剪得来」。但 #4 §9 与 #6 把它和「图片转格式/裁剪」一起归入第二批，理由是「V1 主链先跑通」；旧仓更彻底，`organize` 层完全不做图像处理（`docs/adr/0007-organize-layer-no-image-processing.md`）。

原型重塑时把这条链路画成了 V1 的领域真实性：14 字段来源表标 `dmm (自动裁切)`、步骤留痕标「从横版封套右侧裁切 poster.jpg (2:3)」、落盘台账记裁切动作。据此复核发现原裁决有个洞：

- `output.with.required_fields` 默认 `[title, fanart]`，且 `fanart` 按**字节到手**判 → `fanart` 是**保证**。
- `poster` 只有 javdb 与 dmm 供给；**javbus 与 fc2 的字段表里根本没有 `poster`**。
- 所以「V1 不裁切」= 从这两个源刮出来的任务**永远没有 `poster.jpg`**，媒体服务器只能拿 `fanart` 凑。

## 决定

- `images` 步骤在该任务 `poster` 为空、`fanart` 字节在手时，**从 fanart 右侧按 2:3 裁切**、编码 JPEG 落 `poster.jpg`；不缩放、不改 `fanart` 本身。
- **只用 Go 标准库 `image/jpeg`**：不引 ffmpeg、不引第三方图像库（单二进制不因一张裁切而膨胀）。
- **留痕**：`Sources[poster] = "<源 id> (自动裁切)"`（如 `dmm (自动裁切)`）；`Missing` 不含 `poster`；`step_runs` 的 `images` 摘要与 `file_rows` 都记裁切动作。
- **失败语义**：裁切失败 = poster 仍缺 → `Missing` → 任务 `partial`，**不 `failed`**（`poster` 不在默认 `required_fields`）。
- 仍不做：`poster.crop` 之外的裁剪、转格式之外的处理、演员头像。

## 这是对「派生字段不进 Sources」的有意例外

#4 §5 定死「派生字段（`Metadata.Derived`）不进源优先级、不进 `Sources`/`Missing`」，为的是让第二批不会推翻元数据模型。`PosterCrop` 成为**唯一例外**：它进 `Sources`。

理由：裁出来的 poster 与源直接给的 poster，在媒体服务器眼里是同一个东西（都是 `poster.jpg`）。若把它藏在 `Derived` 里，详情页会显示「poster 无人供给」，而磁盘上明明有 `poster.jpg` —— 那是在骗排障的人。

`TitleCN` / `PlotCN` / `TagsCN` / `ActorsCN` **仍在第二批**，`Metadata.Derived` 的结构与「调度器在类型上够不着它」这条约束不变。

## 替代方案

- **(a) 维持第二批**：被否 —— javbus/fc2 的任务永远缺 `poster.jpg`，而这正是「媒体库里看着不像正经条目」的最直观原因。
- **(b) 让 `poster` 进默认 `required_fields`**：被否 —— 会把 javbus/fc2 的每个任务判成 `failed`，把「缺一张图」升级成「整条任务失败」。
- **(c) 从 javdb 的 `thumb_url` 兜底**：部分可行但不够 —— 只有 javdb 有；javbus/fc2 仍然无解，且 `thumb` 是小图（14 KB 级），做 `poster.jpg` 会糊。
- **(d) 引第三方图像库（如 `disintegration/imaging`）**：被否 —— 标准库 `image/jpeg` 足够做矩形裁切。

## 后果

- javbus / fc2 的任务也有了 2:3 海报，媒体库观感统一。
- 详情页与列表的「来源」列会出现 `<源 id> (自动裁切)` 这种值 —— 前端映射表要能显示它，别当未知源。
- 验收多三条回归：有 fanart 无 poster → 出 2:3 poster.jpg；有 poster → 不裁；裁切失败 → `partial` 且 `Missing` 含 poster。
- 裁切参数（2:3、右侧）是**代码常量**，不开配置键 —— 与「不做设计系统、不定像素值」同一哲学：先有对的形状，再谈可调。
