# AGENTS

## Agent skills

### Issue tracker

issue 与规格都活在 GitHub Issues（`2017fighting/mdc-lite`），用 `gh` CLI 操作。见 `docs/agents/issue-tracker.md`。

### Triage labels

五个规范化 triage 角色，标签串与角色同名：`needs-triage` / `needs-info` / `ready-for-agent` / `ready-for-human` / `wontfix`。见 `docs/agents/triage-labels.md`。

### Domain docs

单 context 布局：根目录 `GLOSSARY.md` + `docs/adr/`（另有 `docs/research/` 存带引用的取证文档、`docs/specs/` 给规格正文留位）。见 `docs/agents/domain.md`。

### Acceptance

`make accept` 是验收环境（真 Jellyfin 钉版本 + 回放真机抓取 + 宿主真二进制，断言落到媒体服务器读到的结果）；PR 门禁是 `make check`，验收跑 main 推送与 nightly。见 `acceptance/README.md`。门禁自检 = `make accept-falsify`（两面都证：破坏→红且原因对、没破坏→绿）。
