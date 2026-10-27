# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Stack

Go 单二进制 + 内嵌 Vite / React 前端；无第三方 UI 组件库（手写组件配合 Tailwind v4 设计令牌单源收敛）。状态存储采用内嵌纯 Go SQLite (`modernc.org/sqlite` + `goose`)。

## Users

Home-Lab / NAS 自建媒体库用户（单用户运维场景）。通常在家庭服务器、k8s 单容器或私有云中部署，配合 qBittorrent、NFS 存储与 Jellyfin/Emby 媒体服务器，管理与整理大量日本番号视频及分片媒体文件。用户关注批量吞吐效率、分片识别准确率、元数据来源可追溯性与极端边界（缺号/同主干/冲突分片）下的手工快速排障。

## Product Purpose

提供一套现代、轻量、高确定性的媒体刮削与流水线归档控制台。
通过透明可控的声明式 Pipeline 步骤序列（扫描 → 规则过滤 → 智能分组 → 番号解析 → 多源刮削 → 图片处理 → NFO 生成 → 输出落盘），彻底解决旧版刮削工具黑盒配置、臃肿脆弱、分片识别混乱、排障困难的痛点。

## Positioning

相较于旧版臃肿且黑盒的 mdc-ng / open-mdcng，mdc-lite 是**轻量纯粹的 Pipeline 流水线工具与专业排障运维台**：
- **单二进制无外部运行时依赖**：无 Python/Node/外部 Chrome/FlareSolverr 依赖，极低内存与 CPU 占用。
- **透明强类型流水线**：单配置文件即完整流水线，清晰的步骤依赖与不可变中间产物契约。
- **前置智能分组**：解析前基于编辑距离与分片标记识别 CD1/CD2/分盘，消除命名歧义。
- **高密度专业运维台**：无通用后台模板感，逐字段胜出源追溯、六档原因码留痕、落盘台账明细、容器内路径选择白名单，专为排障而生。

## Operating Context

- **运行环境**：部署于 Docker / Kubernetes 单容器，挂载 `/config`（SQLite 与配置，禁止 NFS 卷）与 `/media`（NFS/本地存储）。端口 9208，前置可接入 Authelia SSO（应用内不做用户鉴权）。
- **网络拓扑**：网络出口依赖路由器透明代理与域名规则分流（如 DMM 日本出口），应用层不做代理池与出口探测，仅内置轻量域名限速与退避。
- **浏览场景**：现代桌面宽屏（主要工作台，高密度多列）以及移动端（应急查看与补号）。
- **下游协同**：输出规整的 `{{ number }}/` 目录、`-cd{{ part }}` 视频、标准化图片（poster 2:3、fanart 16:9）与规范的 `movie.nfo`，供 Jellyfin/Emby 自动索引。

## Capabilities and Constraints

### 核心能力（V1 主链）
1. **扫描预览与勾选建任务**：
   - 容器内目录安全选择器（基于 `browse_roots` 白名单，默认 `["/media"]`，支持面包屑、路径校验）。
   - 预览执行到 `--until parse`，表格展示候选文件、解析番号与分片分组，按组勾选建任务。
2. **任务列表管理**：
   - 状态芯片筛选（7 种确定状态：`queued`, `running`, `succeeded`, `partial`, `failed`, `needs_number`, `cancelled`）。
   - 批次/触发源筛选，按番号单字段即时搜索，服务端页码分页（20/50/100）。
   - 失败原因第二行直观露出，支持批量重跑与单任务取消。
3. **任务详情与追溯（四标签页）**：
   - **字段与来源**：14 项源级字段（title, plot, poster, fanart 等）的展示、胜出源标注（javdb/dmm/javbus/fc2）及缺失项标记。
   - **步骤留痕**：流水线各 step_run 的耗时、输入输出摘要与产物 Plan。
   - **源留痕**：逐源请求耗时、状态及六档结构化原因码（`egress_unavailable`, `timeout`, `blocked`, `cooldown`, `http_5xx`, `parse`）。
   - **落盘台账**：输出文件（视频链接、NFO、图片）的目标路径、哈希、尺寸与动作记录，支持路径一键复制。
   - **媒体灯箱**：海报与剧照的大图预览能力。
4. **待补号闭环（Needs Number）**：
   - 侧边栏独立视图 + 详情页内嵌面板双入口。
   - 支持逐文件输入番号、指定「同作品分片」、提交从 parse 步骤重跑或跳过（取消任务释放文件占用）。

### 严格约束
- **零外部 UI 库**：禁止引入 Ant Design, MUI, Chakra, Element 或各类通用后台套件；以纯原生 HTML/CSS + Tailwind v4 打造。
- **单文件/内嵌构建**：前端产物内嵌于 Go 二进制；原型 `frontend-views.html` 为单文件，后续生产代码亦保持轻盈自洽。
- **非功能约束**：无用户鉴权逻辑；不提供运行时日志查看器（以结构化 Step/Source runs 替代）；视频不抽帧。

## Evidence on Hand

- **产品架构与设计规格**：本仓 `README.md` 与 Issue #1（主链规格全图）、Issue #2～#16 决议文档。
- **原型与技术审计**：`prototype/frontend-views.html`（已包含运维台/卡片墙/流程看板探索及最终 A 基线收口），`prototype/audit-frontend-views.md`（机械检测 18/20，具备完善的无障碍结构、键盘导航及响应式断点）。

## Product Principles

1. **运维第一，胜于繁饰（Operate over Decorate）**
   界面是用来完成扫描、查号、补号、排障的精密工作台。信息密度、扫描速度与清晰的视觉层级高于空洞的美学装饰。
2. **求真留痕，有据可查（Truthful Traceability）**
   每一个最终落盘的字段都有清晰的胜出源贡献记录；每一次失败都有精确的原因码归因；不做模糊静默降级。
3. **自洽收敛，轻盈纯粹（Zero Bloat & Unified Tokens）**
   拒绝第三方组件库的风格绑架与依赖包膨胀。所有表面、字阶、间距、语义状态色均收敛于统一的设计令牌系统。
4. **容错确定，闭环兜底（Deterministic Resilience）**
   任务失败隔离不阻断流水线；歧义番号明确转入 `needs_number` 待补号池；支持幂等重跑与资源状态自动复位。

## Accessibility & Inclusion

- 键盘可达性：全交互控件支持键盘 Tab 聚焦与直观的 `:focus-visible` 焦点环，对话框与灯箱具备 Esc 退出和焦点陷阱管理。
- 状态多重编码：7 种状态色采用「形状/点 + 文字 + 色彩」多重编码，不依赖单一色彩传达语义。
- 触控与排版：移动端断点适配，交互点击区域保障（coarse 触控下 ≥44px），可见文字逐元素满足对比度要求。
