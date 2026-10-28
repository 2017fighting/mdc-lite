# 0017. 候选规则是有序列表，扩展名只在 filter 判；伴随文件有落盘通道

- 状态：Accepted
- 日期：2026-10-28
- 来源：wayfinder 票「Pipeline 步骤模型」（#2）、「番号解析与文件分组规则」（#3）、「输出落盘与链接语义」（#6）；spec 复审补漏

## 背景

spec 展开时留下了三处空白，它们互相牵连：

1. **`filter` 的 `with` 键面从未定义**。§3 只说「候选规则，命中即入」，只有「入」没有「排除」；但同节的 `step_runs` 示例是 `filter: {in: 412, out: 37, skipped: {size: 350, ext: 25}}`（有按体积、按扩展名的排除计数），验收硬要求第 4 条又写「会把小夹具全丢掉的默认值（**如体积下限**）在验收配置里显式置 0」——引用了一个谁都没定义的键。README 的原意是「规则列表：条件 → 动作」，含 `文件大小 < 100 MiB => 排除`。
2. **三处都在管「哪些文件进来」而没有优先级**：`scan.with.extensions`、`filter` 规则、`group.with.companion_extensions`。若 `scan` 只枚举视频扩展名，字幕永远进不了 `FileSet`，`companion_extensions` 就是死配置；若 `scan` 枚举全量，`filter` 会把 `.srt` 当候选。
3. **伴随文件（字幕）没有落盘通道**。#3 裁定字幕「跟随对应视频改名」进输出目录，但 `PlannedWrite.Kind ∈ {video, image, nfo}`、`file_rows.role ∈ {video, poster, fanart, extrafanart, nfo}` 里都没有它的位置——字幕要么被静默丢掉，要么无处记账（重跑清理也管不到）。

## 决定

- **`filter.with.rules` 是有序规则列表，首条命中即停**：每条形如 `{name, when, then}`，`then ∈ {include, exclude}`，`name` 必填且在同一份配置里唯一（它就是 `Candidate.Rule` 与 `step_runs.skipped` 的键）。`when` 的键面 V1 只开四个：`ext_in` / `ext_not_in` / `min_size`（字节，小于则命中）/ `stem_matches`（Go 正则）。出厂默认三条照 README 示例：伴随扩展名加入 → 小于 100 MiB 排除 → 视频扩展名加入。
- **扩展名只由 `filter` 判**：`scan.with.extensions` 取消，`scan` 只做 `roots` / `exclude` / 隐藏文件与 `.part` / `.!qB` 标记过滤。`group.with.companion_extensions` 保留，但它**只决定谁算伴随文件**，不再兼任「能不能进候选」。
- **伴随文件有落盘通道**：`PlannedWrite.Kind` 增加 `companion`，`file_rows.kind` 增加 `companion`、`file_rows.role` 增加 `companion`。`output` 步骤把 `Group.Companions` 按「跟随对应视频改名」的规则写进输出目录（字幕保留自己的语言 token、图片保持原名），并照常进台账，因此重跑清理与幂等跳过对它同样成立。
- **`FileRef` 不携带分辨率**：V1 不引 ffprobe、不抽帧，sanitize 又把分辨率 token 当噪声剥掉，所以「分辨率」在 V1 物理上取不到 → 从预览表与详情页**删掉这一列**（`GET /api/scan/previews/{id}` 的 `groups[]` 不再有 `res`）。

## 替代方案

- **`filter` 用固定键**（`extensions` / `min_size` / `deny_globs`）：被否 —— 背离 README 的「规则列表」写法，且 `step_runs` 的按规则名计数会退化成按固定键计数。
- **固定键 + 可选 `rules` 追加**：被否 —— 两条判定路径要再定义一次顺序，实现与文档都更重。
- **让 `scan.extensions` 兼管伴随文件**：被否 —— 等于在 `scan` 里塞进「视频 vs 伴随」的语义，而那个语义属于 `group`。
- **字幕不进输出目录（只做视频与图片）**：被否 —— 与 #3 的裁定直接冲突，且用户真实需求就是「整理完能直接播」。
- **分辨率从文件名 token 顺带提取**：被否 —— 文件名里的 `1080p` 不可靠（可能缺失、可能与实际不符），显示一个半可信的值比不显示更坏。

## 后果

- 验收「体积下限置 0」有了落点：把 `too-small` 那条规则的 `min_size` 写 0，或整条删掉。
- `filter` 的严格解码必须校验 `then` 枚举、`when` 的未知键、`name` 唯一，坏配置照 §2 的规矩拒绝启动。
- `file_rows` 与 `PlannedWrite` 的枚举变化会连带影响工单 #22（落盘主链）与 #26（详情端点回传 `file_rows`）。
- 预览表与详情页少一列，工单 #27 的列清单要同步。
