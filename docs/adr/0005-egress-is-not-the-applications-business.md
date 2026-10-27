# 0005. 出口不是应用的事，不接 TLS 指纹库、不做 FlareSolverr

- 状态：Accepted
- 日期：2026-10-27
- 来源：wayfinder 票「网络出口与反爬边界」（#12）、「首批刮削源现状核查」（#13）

## 背景

dmm 全线（主站、GraphQL、以及图片 CDN）在无日本出口时是 TLS 握手期被切；javdb 的网站域名是 IP 层拒绝（APP API 域名可用）；javbus 的年龄门是 cookie 不是 JS 挑战。旧仓选了 `bogdanfinn/tls-client`（实测 +10.10 MB、16 个模块进二进制），并接了 FlareSolverr。

## 决定

- 应用侧只用 **`net/http` + 浏览器 UA**；**不接 TLS 指纹库**（不引 `tls-client`）、**不做 FlareSolverr 也不开占位键**（空转的键比没有键更坏）。
- **出口由用户侧路由器透明代理 + 按域名分流承担**；应用**不做启动校验、不做可达性预检**，只把没打通如实报成 `failed` + `egress_unavailable`。
- **`Fetcher` 是唯一传输接缝**：元数据与图片共用；限速按可注册域（1 req/s + burst 2）、冷却快速失败（403/429/503 → 5 min，只在内存）、重试只在 Fetcher 内（2 次 + 退避 + jitter）。
- 源级只开四个旋钮：`proxy` / `domains`（有序 + 失败切换）/ `rate` / `timeout`（默认 30s）；UA、cookie `dv=1`、图片 `Referer`、`jdsignature` 常量全是**代码常量**（协议常量不是部署凭据）。
- 失败面 = 三态 + **六档原因码**（`egress_unavailable` / `timeout` / `blocked` / `cooldown` / `http_5xx` / `parse`）；**「取不到」永不写成 `notfound`**。

## 替代方案

- `tls-client` 传输层：留档为**反爬升级路径**（实测撞上指纹层/JS 层拒绝时再开票），不是预防性接入。
- FlareSolverr 适配器：留档同上；javbus 的年龄门是 cookie，用不上。
- 应用内代理池 / 出口健康检查 / 住宅出口管理：不做（本部署靠透明分流）。

## 后果

- 换传输层只动 `Fetcher` 一处。
- 通用层只按状态码判惩罚，站点专属拦截页由源自己用正向标记认出来报 `blocked`（旧仓的通用 body 标记曾把健康站点误冷却 300 秒）。
- 限速桶必须能吃 IP 字面量主机（验收夹具），否则要么崩、要么所有夹具串成一个桶。
