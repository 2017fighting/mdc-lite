# 0014. 验收是一条命令（真 Jellyfin + 回放夹具 + 宿主真二进制），门禁自证两面

- 状态：Accepted
- 日期：2026-10-27
- 来源：wayfinder 票「验收与测试策略」（#7）

## 背景

旧仓 787 个测试、零覆盖率测量，事故照样发生；acceptance 从不进 CI（`grep accept .github/workflows/ci.yml` 无输出），理由是「需要 Docker 和一 GB 镜像、要跑几分钟的测试就是没人跑的测试」。门禁自证只覆盖前端两个门禁。旧仓验收环境自己记的缺口是「什么都不渲染界面」，而它第一次被用起来就是查一个打不开的任务页。

## 决定

- **验收环境 = `make accept` 一条命令**：docker compose 起**真 Jellyfin（镜像钉版本、可覆盖）** + 回放夹具的刮削源 + 宿主上的真二进制；**断言落到 Jellyfin 侧读到的结果**（含多分盘 + `extrafanart` 同目录的 `IsInMixedFolder`）。
- **边界**：PR 门禁 = `go vet` + `go test ./...` + lint + 前端 build/typecheck（快，无 Docker）；**验收跑 main 推送 + nightly**（一直在跑，但不塞进每个 PR）。
- **防空转 = 门禁自检两面都证**：破坏 → 红且原因对；没破坏 → 绿；每次运行留「我跑了」的正向标记。
- **一套夹具，单测与验收共用**（真机抓取、逐字不改、manifest 带 `url/status/bytes/captured/sha256`）；构造夹具只允许 `defect-reproduction` / `parser-shape`。
- 真站只出现在 opt-in 夹具刷新与 CLI `mdc source test`；**不做真站漂移探测、不做任何真站门禁**。
- 无覆盖率门禁、无数量目标；语料驱动 + 每条规则带反例；存储层用真 SQLite 临时文件 + 真临时目录。
- 前端只加**极简 Playwright 冒烟**（三视图 + 待补号填号），无 axe。

## 替代方案

- 验收继续不进 CI：被否 —— 那就是「没人跑的检查」。
- 用假 Jellyfin / 只断言产物存在：被否 —— 要验的是「媒体服务器读到什么」。
- 覆盖率门禁：被否 —— 旧仓的教训是覆盖率不是事故的解药。

## 后果

- 12 条硬要求写进实现（库根容器内外同路径、非点开头 scratch 根、生成短片而非零字节、断言不数条目数、显式 `-timeout`、非 200 图片致命、`succeeded` 与 `partial` 都要走到、Jellyfin 版本钉住、首轮前放 primer 目录、Emby 不进断言、扫描由验收环境自己触发）。
- 媒体库刷新仍留第二批（与谁触发扫描无关）。
