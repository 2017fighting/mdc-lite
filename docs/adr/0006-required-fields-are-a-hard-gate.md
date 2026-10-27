# 0006. 必选字段是程序强制门禁，默认只有 title + fanart

- 状态：Accepted
- 日期：2026-10-27
- 来源：wayfinder 票「字段清单与元数据模型」（#4）

## 背景

旧仓明确「没有必填字段概念」（`scrape/scheduler.go` 写着 *There is no required field in V1*，ADR-0002:16 同），**所有源都失败也只是 `partial`**；真正判 `failed` 的只有图片下载失败与硬链 EXDEV。新 README 把字段分成「必选/可选」两档，这是一处相对旧仓的改变。

## 决定

- 必选 = **程序强制门禁**：`output.with.required_fields`，**默认 `[title, fanart]`**；写了未知字段名**启动即拒绝**。缺则任务 `failed`，除非 `output.on_error: continue`。
- `fanart` 按**字节到手**判（URL 有但抓取/解密失败算缺），因此错误信息能区分「源没给」与「给了但抓不下来」。
- 缺字段 = 该源在该字段无贡献 → 字段级失败 → fallback 照常拉下一个源。
- 默认只留两项：README 的七项当硬门禁会大面积误伤（FC2/素人片常常没有 actors/releasedate/tags，extrafanarts 大多数片子本就没有剧照）；想要更严自己加。

## 替代方案

- **照 README 的七项当默认门禁**：被否 —— 误伤面积大，`partial` 会变成常态失败。
- **沿用旧仓「无必填字段」**：被否 —— 「缺了作品身份或背景图」的任务产出的是一个媒体服务器读不出东西的目录，应该被看见。

## 后果

- 任务状态有三种落点：`succeeded` / `partial` / `failed`。
- 验收必须**同时走到 `succeeded` 与 `partial`**（旧仓已知缺口：javbus 语料永远 `partial`，`succeeded` 从未被验过）。
