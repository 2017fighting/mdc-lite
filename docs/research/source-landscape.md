# 首批刮削源现状核查

> 票：[#13 首批刮削源现状核查](https://github.com/2017fighting/mdc-lite/issues/13)（wayfinder:research，父图 [#1](https://github.com/2017fighting/mdc-lite/issues/1)）
> 本文服务的下游票：[#12 网络出口与反爬边界](https://github.com/2017fighting/mdc-lite/issues/12)
> 核查时间：**2026-10-26（UTC+8）**，全部为当下真实抓取。

## 0. 方法与边界

**怎么测的**

- 工具：`curl`（OpenSSL 后端），真实浏览器 UA `Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36`。
- 出口：容器直连，**无任何代理环境变量**；出口 IP `172.197.168.204`，吉隆坡 / 马来西亚（AS8075 Microsoft）。即**机房 IP、非日本出口**——这正是需要用来暴露「出口地区要求」的出口。
- 频率：单线程串行，请求之间 `sleep 2–4s`；单域请求数在个位数到十几条量级。**没有做压力/限流探测**（刻意克制，见 §5 未验证）。
- 失败重试：每个失败目标重试一次，两次都失败才记「不可达」。dmm 全系、javdb.com 均为**两次复现**同一失败。

**旧仓资料的角色**

`open-mdcng` 只当**假设来源**，全部以本轮实测为准。引用的旧仓文件用 `本地文件:行` 标注；逆向资料取自提交 `8369f4d`。

**本轮抓取记录（状态/体积为实测值）**

| # | 目标 | 状态 | 体积 | 备注 |
|---|---|---|---|---|
| 1 | `https://www.javbus.com/`（无 cookie） | 302 | 2 B | → `/doc/driver-verify` |
| 2 | `https://www.javbus.com/SSIS-001`（带 cookie） | 200 | 41 375 B | 无挑战标记 |
| 3 | `https://javbus.com/SSIS-001`（无 www） | 301 | — | → `www.javbus.com` |
| 4 | `https://www.javbus.com/ZZZZ-9999` | 404 | 20 445 B | `404 Page Not Found!` |
| 5 | `https://www.javbus.com/pics/cover/83ie_b.jpg`（无 Referer） | 403 | 4 547 B | Cloudflare HTML |
| 6 | 同上（带 Referer） | 200 | 188 849 B | image/jpeg |
| 7 | `https://javdb.com/` | **000** | 0 | curl 35 TLS EOF（×2） |
| 8 | `https://javdb.com/api/v2/search?...` | **000** | 0 | 同上（×1） |
| 9 | `https://jdforrepam.com/api/v2/search?q=SSIS-001…` | 200 | 14 130 B | JSON，`success:1` |
| 10 | `https://javdb580.com/api/v2/search?q=SSIS-001…` | 200 | 14 130 B | 镜像，同 body |
| 11 | `https://jdforrepam.com/api/v4/movies/ZY5eq?from_rankings=false` | 200 | 5 582 B | JSON 详情 |
| 12 | `https://www.dmm.co.jp/` | **000** | 0 | curl 35 TLS EOF（×2） |
| 13 | `https://api.video.dmm.co.jp/graphql` | **000** | 0 | 同上（×2） |
| 14 | `https://pics.dmm.co.jp/…/ipx00535ps.jpg` | **000** | 0 | 同上（×2） |
| 15 | `https://adult.contents.fc2.com/article/4982870/` | 200 | 115 427 B | nginx，HTTP/1.1 |
| 16 | `https://adult.contents.fc2.com/article/1234567/` | 200 | 32 865 B | miss 页 |
| 17 | `https://www.avbase.net/works?q=SSIS-001` | 200 | 53 206 B | `__NEXT_DATA__` 含 2 条 works |
| 18 | `https://www.avbase.net/works/idp:IPX-535` | 200 | 55 530 B | prefix 形态 |
| 19 | `https://www.avbase.net/works/IPX-535` | 404 | 10 644 B | 无 prefix 直达失败 |
| 20 | `https://www.avbase.net/search?wd=SSIS-001` | 200 | 30 731 B | `pageProps={}`，0 命中 |
| 21 | `https://airav.io/search_result?kw=SSIS-001` | 200 | 58 414 B | 8 张卡片 |
| 22 | `https://airav.io/video?hid=127999` | 200 | 86 327 B | 详情页 |
| 23 | `https://tp.spfcas.com/rhe951l4q/covers/zy/ZY5eq.jpg` | 200 | 157 287 B | 编码体，无 `Content-Type` |
| 24 | `https://c0.jdbstatic.com/covers/zy/ZY5eq.jpg` | 200 | 157 286 B | 明文 JPEG（首次 25 s 超时，重试 19.06 s 成功） |

---

## 1. 站点现状：可达性 / 反爬 / 需要的出口地区

| 源 | 本出口可达性 | 反爬形态 | 出口要求 | 证据 |
|---|---|---|---|---|
| **javbus** | ✅ 直连可用 | Cloudflare 前置；**无挑战**，但**年龄门是 302 重定向**（不是 JS 挑战） | 无（机房 IP 可用） | 记录 #1–#4 |
| **javdb（网站）** | ❌ TLS 握手期被切断 | IP 层拒绝 | 需要别的出口 | 记录 #7、#8 |
| **javdb（APP API 通道）** | ✅ 直连可用 | 无 CF 挑战；需自算 `jdsignature` 头 | **无**（机房 IP 可用） | 记录 #9–#11 |
| **dmm** | ❌ TLS 握手期被切断 | IP 层拒绝 | **需要日本出口** | 记录 #12–#14 |
| **fc2（adult.contents.fc2.com）** | ✅ 直连可用 | nginx 直出，无 CF、无挑战 | 无 | 记录 #15、#16 |
| **avbase** | ✅ 直连可用 | Cloudflare 前置；`cdn-cgi/challenge-platform` 只是部署痕迹，非挑战 | 无（页面层） | 记录 #17–#20 |
| **airav** | ✅ 直连可用 | 同上，页内引 `cdn-cgi/challenge-platform` | 无 | 记录 #21、#22 |

**逐条说明**

- **javbus 的年龄门是 cookie 门，不是挑战**。无 cookie 时 `https://www.javbus.com/` 与详情页都答 `302 → /doc/driver-verify?referer=…`；响应头带 `server: cloudflare`、`set-cookie: existmag=mag`。旧仓的同一结论见 `open-mdcng/docs/source-notes.md:19-25`。本轮把「最小 cookie 集」量了出来，见 §4。
- **javdb 有两个不同的可达性事实**，必须分开说：
  - **网站 `javdb.com` 在本出口是 IP 层拒绝**：TCP 连得上，TLS 握手期对端直接 EOF（`error:0A000126:SSL routines::unexpected eof while reading`），重试一次同样失败。这与旧仓票 13 的记录一致（`.scratch/open-mdcng/issues/13-tls-fingerprint-poc.md:38`「javdb 的 EOF 是 IP 层的，不是指纹层的」）。
  - **APP 的 JSON API 通道不经过这个封锁**：本轮在 `jdforrepam.com`（旧仓出厂默认域，`open-mdcng/config/defaults.go:155`）与 `javdb580.com`（详情响应 `share_info` 自己写的镜像）上都是 200 + 完整 JSON。旧仓 2026-09-27 的同一结论见 `open-mdcng/docs/source-notes.md:119-127`。**结论：javdb 的可用性绑定在「API 域名」上，而不是网站域名；API 域名是可热配置的。**
- **dmm 是 IP 层拒绝，且不止主站**：`www.dmm.co.jp`、`video.dmm.co.jp`、`api.video.dmm.co.jp/graphql`、以及 **图片 CDN `pics.dmm.co.jp` / `awsimgsrc.dmm.co.jp` / `cc3001.dmm.co.jp`** 全部同一形态失败（TLS EOF）。`/dev/tcp` 直连 `65.9.161.23:443` 成功，说明是 TLS 层拒绝而非路由不通。**这一条把「日本出口」从「拿字段的前提」升级为「连图都要的前提」**——因为 javbus 的样张图与 avbase 的海报/样张图都托管在 dmm 的 CDN 上（§3）。
- **avbase 与 airav 都内联引用 `cdn-cgi/challenge-platform`**（本轮实测两站各有 1 处）。这是 Cloudflare 部署痕迹，**不是**挑战信号——旧仓为此专门开过一张票把该标记从挑战判定里删掉（`.scratch/open-mdcng/issues/36-cloudflare-marker-false-positive.md`）。V1 的反爬判定不要用它。

**未验证**：javbus / javdb API / avbase / airav 的**限流阈值**（本轮刻意低频，没触发任何 429；旧仓记录 javdb 会按 IP 封搜索功能，`.scratch/open-mdcng/research/07-source-landscape.md` §二.4，本轮未复现也未证伪）。Cloudflare 是否会因高频升级为真挑战，同样未验证。

---

## 2. 抓取路径：搜索页 vs 直达详情、HTML vs JSON、番号变换

| 源 | 路径形态 | 请求数 | 数据面 | 番号 → 地址的变换 |
|---|---|---|---|---|
| javbus | **直达详情** | 1 | HTML | 番号原样拼进路径：`https://{domain}/{number}`（`open-mdcng/scrape/sources_builtin/javbus.yaml:33-34`） |
| javdb | **搜索 → 详情**（两段一个抓取单元） | 1（未命中）/ 2（命中） | **JSON API** | 站点 id 是不透明 hash（`SSIS-001 → ZY5eq`），必须先检索；FC2 要改写拼法 |
| dmm | **GraphQL 搜索 → GraphQL 内容** | 1–2 | **JSON（GraphQL）** | 番号 → FANZA 产品 id，且有**零填充两种拼法**（`ssis00001` / `ssis001NN`） |
| fc2 | **直达详情** | 1 | HTML | 番号 → **末尾数字段**即 article id |
| avbase | **搜索页 → canonical 详情** | 1（未命中）/ 2（命中） | `__NEXT_DATA__` JSON blob（HTML 内） | `work_id` 精确匹配 → 详情地址带 prefix：`/works/{prefix}:{work_id}` |
| airav | **搜索页 → 详情** | 1–2 | HTML（SSR） | 搜索卡片自带 `/video?hid={id}` |

**逐条说明**

- **javbus 是「番号即地址」的直达源**，一个番号一次请求，没有搜索腿。旧仓的适用性声明是 `^[A-Z]+$`（FC2 带数字与连字符的家族直接跳过、零请求，`javbus.yaml:27-29`）。本轮实测 `SSIS-001` 一次请求拿到完整详情页（标题、`div#avatar-waterfall a.avatar-box`、`a.bigImage img`、`span.genre label a`、`div.info p` 信息行）。**站点的 404 语义干净**：`/ZZZZ-9999` → HTTP 404（记录 #4），可以直接当 `Found: false`。
  - 附带一条来自旧仓票 71 的**站上规范写法**证据（本轮未复测）：`DEVR-37` 与 `DEVR-037` 都 200，`DEVR-0037`/`DEVR-00037` 都 404（`.scratch/open-mdcng/issues/71-dmm-content-id-four-digit-maker-id.md` §测量）。说明 javbus 会**规整**同义写法，但不接受任意宽度。
- **javdb 的通道是 APP 自己的 JSON API，不是网站 HTML**。搜索 `GET {domain}/api/v2/search?q=…`，详情 `GET {domain}/api/v4/movies/{id}?from_rankings=false`（`open-mdcng/scrape/javdb.go:185-197`、`docs/source-notes.md:105-110`）。本轮实测：
  - 搜索 `SSIS-001` → `data.movies[0].id = "ZY5eq"`，`number = "SSIS-001"`；
  - 详情 → `data.movie`，含 `origin_title`（日文原名）、`title`（中文标题）、`score`（5 分制字符串，实测 `"4.43"`）、`duration`、`release_date`、`maker_name`、`director_name`、`series_name`、`tags[]`、`actors[]`（**带 `gender` 字段**，实测 `SSIS-001` 的演员里混着男优 `平田司` `gender:1`）、`preview_images[]`、`thumb_url`/`cover_url`。
  - **番号变换（FC2）本轮实测成立**：`FC2-PPV-3336019` → `0` 条候选；`FC2-3336019` → 5 条，首条精确命中（`id=K4ZkDv`）。改写规则是锚定正则 `^FC2[-_ ]?(?:PPV[-_ ]?)?(\d+)$`（`javdb.go:204`）。
  - **必须精确匹配**：查 `ZZZZ-9999` 时站点返回的不是空集，而是近似号 `ZZZM-999`（`success:1`）。取第一条会抓错作品——与旧仓在 airav 上的同款教训（`open-mdcng/docs/source-notes.md` airav 节「Exact match is not a nicety」）。
  - **协议语义（本轮全部实测）**：缺 `jdsignature` → `200` + `{"success":0,"action":"ParameterInvalid"}`；签名 400 秒前 → 仍 200 接受；900 秒前 → `400` + `ExpiredSignature`；详情未知 id → `404` + `ResourceNotFound`。**拒绝的诊断在 body 里，不在状态码里**，所以不能只看状态码（`open-mdcng/scrape/javdb.go:63-70`）。
  - 签名的配方是 `{unix}.{salt}.{md5(unix + key)}`，salt/key 是 APP 的共享协议常量（`javdb.go:152-153`）。**常量轮换要发版**，域名不用（`javdb.go:139-150`）。
- **dmm 是两段 GraphQL，番号 → 产品 id 有零填充问题**。搜索用 `legacySearchPPV`，内容用 `ppvContent`，端点 `https://api.video.{domain}/graphql`（`open-mdcng/scrape/dmm.go:284-287`）。**番号变换的坑**：搜索返回的 id 不是番号，`SSIS-001` 的第一条产品是 `ssis00001`、后续是 `ssis001NN`，所以旧仓要**两种拼法都试**（`dmm.go:551-573`、`docs/source-notes.md` dmm 节）。**本轮无法验证任何 dmm 端点**（IP 层拒绝，记录 #12–#14）——这一节全部是旧仓结论，V1 若要接 dmm 必须先有日本出口再重测。
- **fc2 也是直达源，且番号变换只是「取末尾数字段」**：`fc2ArticleID` 取的是**末尾连续数字**而不是「所有数字」（否则 `FC2` 的 `2` 会混进 id），地址 `https://{domain}/article/{id}/`（`open-mdcng/scrape/fc2.go:88-111,127`）。本轮实测 `4982870` → 200 真实商品页。**miss 页是 200**（记录 #16，标题「お探しの商品が見つかりませんでした」、`items_notfound_wp` 出现 1 次），所以必须用正向标记判「没这条」，不能靠状态码，也不能靠「字段全空」——旧仓票 44 就是被这个假阳性坑过（`.scratch/open-mdcng/issues/44-fc2-miss-page-answered-as-found.md`）。
- **avbase 是「搜索 + canonical 详情」两段，且不能拿番号当直达地址**。本轮实测：
  - 真检索页是 `GET /works?q={number}`（参数是 `q` 不是 `wd`）；旧定义里的 `/search?wd=` 是 200 但**只渲染搜索表单**——`pageProps` 是空对象、`SSIS-001` 在 30 731 B 里出现 **0 次**（记录 #20）。这与旧仓票 37 的实测逐字一致（`.scratch/open-mdcng/issues/37-avbase-definition-rewrite.md`）。
  - 检索页的 `__NEXT_DATA__.props.pageProps.works[]` 本轮有 **2 条**（`SSIS-001` 有两条同 `work_id` 的记录：`id=273023 prefix=""` 与 `id=775989 prefix="secondface"`），按 `work_id` 精确匹配取第一条（站点排序）。
  - **`/works/{number}` 不能当直达模板**：`/works/SSIS-001` → 200（因为该条 prefix 为空），`/works/IPX-535` → **404**；正确地址是 `/works/idp:IPX-535` → 200，`work.prefix="idp"`、`work.id=8537`（记录 #18、#19）。旧仓的同一实测见 `.scratch/open-mdcng/issues/37` §Comments。
  - 数据在 `__NEXT_DATA__` 里（`pageProps.work` / `pageProps.works`），DOM 只是它的渲染——poster、演员头像、预告片**只在 blob 里**（`open-mdcng/docs/source-notes.md:52-65`）。
- **airav 是「搜索 + 详情」两段，选择器本轮复核通过**：检索 `GET /search_result?kw={number}`（参数是 `kw`），卡片 `div.col.oneVideo`，详情链接 `a[href^="/video?hid="]`，番号是 `h5` 的首 token，详情 `GET /video?hid={id}`（`open-mdcng/scrape/sources_builtin/airav.yaml:64-65`、`.scratch/open-mdcng/issues/38-airav-definition-rewrite.md`）。本轮实测检索页 8 张卡片、`/video?hid=127999` 200。旧仓记录的「近似号排在精确号前面」（`IPX-535` 先出 `SCPX-535`）本轮**未复测**。

---

## 3. 图片 CDN 形态、编码/混淆与解码方式

| 源 | 图片地址形态 | 需要 Referer？ | 编码/混淆 | 本出口可取？ |
|---|---|---|---|---|
| javbus | **相对地址**，自家 CDN `/pics/cover/*_b.jpg`、`/pics/actress/*_a.jpg`；样张是 dmm 绝对地址 | **要**（无 Referer → 403 CF HTML） | 无，明文 JPEG | 封面/头像 ✅；**样张（dmm）❌** |
| javdb | APP CDN `tp.spfcas.com/{token}/{kind}/…`；站点 CDN `c0.jdbstatic.com/{kind}/…` | 否（两边都不用） | **APP CDN：`key ‖ (jpeg XOR key)`，每图随机 key**；站点 CDN：明文 JPEG | ✅ 两边都通 |
| dmm | `pics.dmm.co.jp`、`awsimgsrc.dmm.co.jp`、`cc3001.dmm.co.jp` | — | — | ❌ TLS 层拒绝 |
| fc2 | **协议相对** `//contents-thumbnail2.fc2.com/{w…}/…` | 否 | 无，明文 JPEG | ✅ |
| avbase | 页面/blob 里的地址指向 **dmm CDN**（`pics.dmm.co.jp` / `awsimgsrc.dmm.co.jp` / `cc3001.dmm.co.jp`）与 `pic.duga.jp` | 否 | 无 | dmm 部分 ❌；`pic.duga.jp` ✅（实测 200） |
| airav | 自家 `https://airav.io/storage/cover/big/{hid}.jpg` | 否 | 无，明文 JPEG | ✅ |

**javdb 的编码与解码（本轮完整复现）**

- 形态：**第 0 字节是密钥，其余字节整体与它异或**，即 `key ‖ (jpeg XOR key)`。它是编码不是加密（密钥随体传输），**不需要逆向**。
- 本轮三个样例，**三种不同的 key**，全部解出合法 JPEG，且解码结果与站点 CDN 同图**逐字节相同**：

  | 资源 | APP CDN 地址 | 原始体积 | key | 解码后 | 站点 CDN 对照 | 是否逐字节相同 |
  |---|---|---|---|---|---|---|
  | cover | `tp.spfcas.com/rhe951l4q/covers/zy/ZY5eq.jpg` | 157 287 B | `0x3e` | 157 286 B | `c0.jdbstatic.com/covers/zy/ZY5eq.jpg` | ✅ 相同 |
  | thumb | `…/small_covers/zy/ZY5eq.jpg` | 14 993 B | `0xb3` | 14 992 B | `c0.jdbstatic.com/thumbs/zy/ZY5eq.jpg` | ✅ 相同 |
  | sample | `…/samples/zy/ZY5eq_l_0.jpg` | 172 426 B | `0xa7` | 172 425 B | `c0.jdbstatic.com/samples/zy/ZY5eq_l_0.jpg` | ✅ 相同 |

  解码代码（Python，等价于 Go 里 `out = bytes(b ^ body[0] for b in body[1:])`）：解出的头两字节必须是 `FF D8`。
- **路径映射**：站点地址 = APP 地址**去掉 token 段**；只有缩略图那类改名——APP 的 `small_covers` 在站点侧叫 `thumbs`。本轮 `small_covers → thumbs` 映射实测成立（`open-mdcng/docs/source-notes.md:141-147` 记的是同一件事）。
- **APP CDN 不返回 `Content-Type`**（本轮三张图 `content_type` 全为空），站点 CDN 返回 `image/jpeg`。别用 Content-Type 判「拿到的是不是图」。
- **站点 CDN `c0.jdbstatic.com` 从本出口很慢**：首次 `--max-time 25` 只收到 112 445/157 286 B 就超时，重试 `--max-time 60` 用了 **19.06 s** 才完成。它没有拒绝请求（没有 403/429），只是慢。**结论：V1 取 javdb 图片应走 APP CDN + 本地解码，而不是把地址改写成站点 CDN**——旧仓的最终选择也是进程内解码（`.scratch/open-mdcng/issues/64-javdb-source.md` §提交后发现的缺陷）。
- 旧仓的一条实测细节本轮未复现：`docs/source-notes.md:121-122` 说「无 User-Agent 会被 `403` + `error code: 1010` 拒绝」。**本轮删掉 UA 头请求同一端点得到 `200` + `success:1`**。这条旧结论现在**不成立**（至少在这个出口/这个端点上是这样）；写进「未验证/已失效」一节。

**其它源的图片细节（本轮实测）**

- **javbus 图片需要 Referer**：`/pics/cover/83ie_b.jpg` 裸请求 → **403 + Cloudflare HTML**（4 547 B）；带上 `Referer: https://www.javbus.com/SSIS-001` → 200 + `image/jpeg`（188 849 B）。头像同理（7 569 B）。**而详情 HTML 本身不需要 Referer**（无 Referer 也 200）——只有图片 CDN 要。
- javbus 详情页的**样张是 dmm 绝对地址**（本轮页面里 10 条 `https://pics.dmm.co.jp/…/ssis00001jp-N.jpg`），从本出口取不到（§1 dmm）。所以**没有日本出口时，javbus 只能拿到封面与头像，拿不到 extrafanart**。
- **fc2 的封面/样张是协议相对地址**（`//contents-thumbnail2.fc2.com/w262h262/…` 与 `/w320/…`），裸请求 200 明文 JPEG，无 Referer 要求。
- **airav 的封面就是 javbus 的封面字节**：`https://airav.io/storage/cover/big/127999.jpg`（188 849 B）与 javbus 的 `/pics/cover/83ie_b.jpg`（带 Referer，188 849 B）**`cmp` 逐字节相同**。也就是说 airav 在封面这一项上**不提供任何 javbus 没有的东西**；它的独有价值只剩中文标题/简介。
- **avbase 的图片全部指向第三方 CDN**：检索页里 `pics.dmm.co.jp` 44 处、`pic.duga.jp` 33 处、`awsimgsrc.dmm.co.jp` 3 处；详情 blob 里 poster = `awsimgsrc.dmm.co.jp/pics_dig/digital/video/ipx00535/ipx00535ps.jpg`、横版 = 同目录 `…pl.jpg`、样张 = `pics.dmm.co.jp/…`、预告 = `cc3001.dmm.co.jp/pv/…`。**dmm 三家从本出口全部 TLS 拒绝**；`pic.duga.jp` 实测 200（`…/unsecure/secondface/0038/noauth/jacket_240.jpg`，44 432 B）。所以**没有日本出口时 avbase 的图片基本拿不到**（只剩 duga 那条重复条目的图）。

---

## 4. 需要的请求头与 cookie

| 源 | 必需 | 实测最小集 | 证据 |
|---|---|---|---|
| javbus（HTML） | 浏览器 UA + 年龄门 cookie | **`dv=1`** | 本轮：`dv=1` 单独 → 200；`age=verified` 单独 → 302；`existmag=all` 单独 → 302；`existmag=all; dv=1` → 200 |
| javbus（图片） | 上面的 cookie + **`Referer`** | `Referer: https://www.javbus.com/{number}` | 裸请求 403，带 Referer 200（记录 #5、#6） |
| javdb（APP API） | 浏览器 UA + **`jdsignature`** | `jdsignature: {unix}.{salt}.{md5(unix+key)}` | 缺签名 → `ParameterInvalid`；签名 400 s 内有效、900 s 过期 |
| dmm | UA + `Content-Type: application/json` + Referer/Origin | 旧仓：`Content-Type` 与 `Referer: https://video.{domain}/` | `open-mdcng/scrape/dmm.go:284-287,422-430`；**本轮无法验证** |
| fc2 | 只要浏览器 UA | 无 cookie、无 Referer | 记录 #15；图片亦裸取 200 |
| avbase | 只要浏览器 UA | 无 cookie、无 Referer | 记录 #17–#20；域名必须带 `www.`（裸域 301） |
| airav | 只要浏览器 UA | 无 cookie、无 Referer | 记录 #21、#22；图片裸取 200 |

**两条要改掉的旧认知**

1. **「javbus 年龄门要预置 `age=verified` cookie」是不准确的。** 本轮逐条量过：真正决定门的是 **`dv=1`**；`age=verified` 单独请求仍然是 302。旧仓出厂配置同时带三个 cookie（`age=verified; existmag=all; dv=1`，`open-mdcng/docs/source-notes.md:25`、`acceptance/sources-real.toml:40-45`），所以它能工作，但**其中两个是多余的**（`existmag=all` 影响是否显示无码/磁力结果，与门无关）。V1 只需写 `dv=1`。
2. **javbus 的图片有 Referer 白名单，HTML 没有。** 旧仓文档只说了「地址是相对的」（`docs/source-notes.md:33-36`），没说图片会被 Cloudflare 拦。V1 的图片下载器必须能按源带 Referer，否则 javbus 的封面会静默变成一张 4.5 KB 的 HTML。

**javdb 签名常量**（照抄自 `open-mdcng/scrape/javdb.go:152-153`，本轮实测签名被接受）：

```
salt = "lpw6vgqzsp"
key  = "71cf27bb3c0bcdf207b64abecddc970098c7421ee7203b9cdae54478478a199e7d5a6e1a57691123c1a931c057842fb73ba3b3c83bcd69c17ccf174081e3d8aa"
jdsignature = "{unix}.{salt}.{md5(unix + key)}"
```

**这个常量不要放进用户可填的配置项**：它不是部署凭据而是 APP 的共享协议常量，放进「密钥」字段会诱导用户填自己的值，然后每个请求都被判为无效签名（旧仓的同一裁决见 `.scratch/open-mdcng/issues/64-javdb-source.md`）。域名相反，**必须**可热配置。

---

## 5. V1 最小可行选择

### 结论：V1 做 3 个源——**javdb（APP API）+ javbus（直达详情）+ fc2（官方站）**

| 源 | 通道 | 请求数/番号 | 无需日本出口？ | 图片 | 独有价值 | 实现成本 |
|---|---|---|---|---|---|---|
| **javdb** | `GET /api/v2/search` → `GET /api/v4/movies/{id}`（JSON） | 1–2 | ✅ | APP CDN + 本地 XOR 解码，**自足** | 字段最广（15 项）、唯一给评分、中文标题 + 日文原名、`extrafanart`、演员带性别 | 中：要自算签名头、要精确匹配、要写解码器、要热配置域名 |
| **javbus** | `GET /{number}`（HTML） | 1 | ✅ | 自家 CDN 封面/头像（**要 Referer**）；样张在 dmm，取不到 | 有码主流的基础字段：标题、标签、演员、制作商、系列、发行日、时长、导演、发行商 | 低：一个 cookie（`dv=1`）+ HTML 解析 |
| **fc2** | `GET /article/{id}/`（HTML） | 1 | ✅ | `contents-thumbnail2.fc2.com`，裸取 | FC2 家族官方站，收录最全；javbus 的 `^[A-Z]+$` 根本不覆盖 FC2 | 低：取末尾数字段 + 正向 miss 标记 |

**为什么是这三个**：三个都能从**没有日本出口的机房 IP** 直连（本轮实测），且都不需要 TLS 指纹库、不需要 FlareSolverr、不需要 cookie 池。三者合起来覆盖 V1 需要的家族面：javdb 覆盖全家族（含 FC2 改写后）并给最全字段，javbus 给有码基础面，fc2 给 FC2 官方面。**V1 的网络层可以先用 Go 标准 `net/http` + 浏览器 UA 起步**，把反爬做成「按源可插拔的请求头/cookie + 图片 Referer」这一层就够——这是给 #12 的输入。

**明确推迟的两个**

- **dmm**：字段最全最准，但**从本出口完全不可达**（主站、GraphQL、以及全部图片 CDN 都在 TLS 握手期被切断，两次复现）。接它 = 先解决「日本出口」这个**部署前提**，而且它的番号 → 产品 id 有零填充双拼法的坑（`ssis00001` / `ssis001NN`）。V1 不接；等 #12 定了代理面再单独开票。
- **avbase**：页面层可达、数据质量高，但它的**图片全部指向 dmm CDN**（本出口不可达），也就是「接了页面、拿不到图」；而且它的字段与 javbus/dmm 大量重叠。价值要在有日本出口之后才完整。V1 不接。
- **airav**（第 4 个候选，可选）：可达、实现成本低（两段 HTML，选择器已复核），但**封面与 javbus 逐字节相同**，独有价值只有中文标题/简介。如果 V1 需要中文元数据，它是**最便宜的一个**；如果 javdb 已经给中文标题（它给），airav 就是重复投入。建议：**V1 不接，留作「中文简介」缺口出现时的第一个补充**。

### 各源抓取路径（照上面结论落地的形状）

```
javdb:  GET https://{api_domain}/api/v2/search?q={q}&from_recent=false&type=movie
          &movie_type=all&movie_sort_by=relevance&movie_filter_by=all&page=1&limit=24
        headers: User-Agent(浏览器), jdsignature
        → 在 data.movies[] 里按 number 精确匹配（大小写/连字符不敏感、数字段必须相同）
        → GET https://{api_domain}/api/v4/movies/{id}?from_rankings=false
        → 图片：tp.spfcas.com 的字节 XOR 解码后再落盘；演员头像若要进 NFO 则用站点 CDN 地址
        FC2 番号先改写：^FC2[-_ ]?(?:PPV[-_ ]?)?(\d+)$ → FC2-<digits>

javbus: GET https://{domain}/{number}
        headers: User-Agent, Cookie: dv=1
        图片: GET https://{domain}/pics/...  headers: Referer: https://{domain}/{number}
        HTTP 404 = 没这条（正常结果，不是错误）

fc2:    取末尾数字段 id → GET https://{domain}/article/{id}/
        命中判据：存在 items_article_headerInfo（正向标记）
        未命中判据：存在 items_notfound_wp（正向标记），HTTP 仍是 200
        图片: //contents-thumbnail2.fc2.com/... 按详情页地址补 scheme，裸取
```

---

## 6. 未验证 / 未定事项（不要当结论用）

1. **dmm 的一切**：端点、GraphQL schema、content id 拼法、图片——本轮只验证了「从本出口不可达」，其余全部沿用旧仓结论。**有日本出口后必须整体重测**。
2. **各站的限流阈值**：本轮刻意低频，没有触发任何 429/冷却。javbus 在 Cloudflare 后面，高频会不会升级为真挑战**未验证**；旧仓记录的「javdb 按 IP 封搜索功能」本轮**未复现也未证伪**。
3. **javdb 的「无 UA → 403 error code:1010」旧结论已失效**：本轮同一端点无 UA 得到 200。是站点改了、还是旧记录测的是别的路径，未查明。
4. **javbus 的镜像域名清单**：旧仓 `research/07-source-landscape.md` 提到 seedmm/javsee 类镜像，但明说来源是评论区（含 AI 生成内容）。本轮只验证了 `www.javbus.com` 与 `javbus.com → www` 的 301，**没有验证任何镜像**。
5. **airav 的近似号排序**（`IPX-535` 先出 `SCPX-535`）本轮未复测。
6. **javdb 的域名轮换机制**：本轮验证了 `jdforrepam.com` 与 `javdb580.com` 两个 API 域可用、`javdb.com` 不可用，但**没有找到域名发现/自动更新的来源**（旧仓是人工热配置）。`share_info` 里的镜像名是本轮唯一见到的「站点自报域名」线索。
7. **avbase 是否有公开 API**：未验证；本轮只用 `__NEXT_DATA__`。
8. **fc2 是否有比「末尾数字段」更严格的 id 规则**：本轮只测了 `FC2-PPV-4982870` 与 `FC2-PPV-1234567` 两个拼法。
9. **各源在 Jellyfin 侧的字段映射**（`user_rating` 十点制换算、`extrafanart` 目录形态）不在本票范围，只在此标注：javdb 的 `score` 是 5 分制字符串（本轮实测 `"4.43"`），旧仓把它与 dmm 的 `reviewSummary.average` 一起 ×2 归一化到 10 分（`open-mdcng/docs/adr/0016-user-rating-is-a-ten-point-value.md`）。

---

## 附：引用到的旧仓资料

- `open-mdcng/docs/source-notes.md`（逐源事实与最近验证表）
- `open-mdcng/scrape/javdb.go`、`scrape/dmm.go`、`scrape/fc2.go`、`scrape/sources_builtin/javbus.yaml`、`scrape/sources_builtin/airav.yaml`
- `open-mdcng/config/defaults.go`（出厂源序与默认域名）
- `open-mdcng/.scratch/open-mdcng/research/07-source-landscape.md`（2025–2026 社区公开知识综述，**未实时抓站**，仅作假设）
- `open-mdcng/.scratch/open-mdcng/issues/13-tls-fingerprint-poc.md`（TLS 指纹/FlareSolverr/限速 PoC）
- `open-mdcng/.scratch/open-mdcng/issues/36,37,38,43,44,64,71`（逐源真实抓取重写与缺陷记录）
- 逆向资料：`git -C /root/clone/open-mdcng show 8369f4d:recovered/backend/scraper/PROVIDERS.md`（原版 21 个 provider 的 base_url 与默认优先级）
