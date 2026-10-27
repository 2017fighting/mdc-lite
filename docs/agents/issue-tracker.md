# Issue tracker: GitHub

本仓的 issue 与规格都活在 GitHub Issues 上（`2017fighting/mdc-lite`）。所有操作走 `gh` CLI。

## 约定

- **建 issue**：`gh issue create --title "..." --body "..."`。多行正文用 heredoc。
- **读 issue**：`gh issue view <number> --json number,title,body,labels,comments`。
- **列 issue**：`gh issue list --state open --json number,title,body,labels,comments --jq '[.[] | {number, title, body, labels: [.labels[].name], comments: [.comments[].body]}]'`，按需加 `--label` / `--state`。
- **设成某 issue 的子 issue**：`gh issue create --parent <parent> ...`，或事后 `gh issue edit <parent> --add-sub-issue <child>`（`gh` 2.94+）。旧版：`gh api --method POST repos/<owner>/<repo>/issues/<parent>/sub_issues -F sub_issue_id=<child-db-id>`（**database id**，见下 **Blocking**）。子 issue 不可用时，在子 issue 正文顶部写 `Part of #<parent>`。
- **评论**：`gh issue comment <number> --body "..."`。
- **加/去标签**：`gh issue edit <number> --add-label "..."` / `--remove-label "..."`。
- **关闭**：`gh issue close <number> --comment "..."`。

仓库从 `git remote -v` 推断；在 clone 内跑 `gh` 时自动带上。

## PR 作为需求入口

**PR as a request surface: no.**（本仓不把外部 PR 当 feature request；`/triage` 读这个标记。）

## 当某个技能说「publish to the issue tracker」

建一个 GitHub issue。

## 当某个技能说「fetch the relevant ticket」

按上面的 **读 issue** 读它。

## Wayfinding operations

`/wayfinder` 用。**地图**是一张带 **子 issue** 作票的 issue。

- **地图**：一张带 `wayfinder:map` 标签的 issue，承载 Notes / Decisions-so-far / 雾区正文。`gh issue create --label wayfinder:map`。
- **子票**：以 GitHub 子 issue 挂到地图上（见 **设成某 issue 的子 issue**）。子 issue 不可用时，把子票写进地图正文的 task list，并在子票正文顶部写 `Part of #<map>`。标签：`wayfinder:<type>`（`research`/`prototype`/`grilling`/`task`）。被认领后 assign 给驱动这张地图的人。
- **Blocking**：用 GitHub **原生 issue 依赖**——唯一在 tracker 自己的 UI 里可见的表示。加边：`gh api --method POST repos/<owner>/<repo>/issues/<child>/dependencies/blocked_by -F issue_id=<blocker-db-id>`，其中 `<blocker-db-id>` 是阻塞者的数字 **database id**（`gh api repos/<owner>/<repo>/issues/<n> --jq .id`，**不是** `#number`、也不是 `node_id`）。GitHub 报的 `issue_dependencies_summary.blocked_by`（只算未关闭的阻塞者）就是活的闸门。依赖功能不可用时，退化为子票正文顶部的 `Blocked by: #<n>, #<n>` 行。所有阻塞者都关闭时，票才算 unblocked。
- **Frontier 查询**：列出地图的未关闭子票（`gh issue list --state open`，限定在地图的 sub-issues / task list 内），去掉任何还有未关闭阻塞者的（`issue_dependencies_summary.blocked_by > 0`，或 `Blocked by` 行里还有未关闭 issue）以及任何已有 assignee 的；按地图顺序取第一张。
- **认领**：`gh issue edit <n> --add-assignee @me`，本 session 的第一次写操作。
- **结票**：`gh issue comment <n> --body "<答案>"`，然后 `gh issue close <n>`，再把「要点 + 链接」追加到地图的 Decisions-so-far。
