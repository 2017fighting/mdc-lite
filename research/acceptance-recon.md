> 本文件是「验收与测试策略」（#7）的取证附件：对旧仓 `open-mdcng`（分支 `rewrite` @ `b1b9b31`，2026-10-01）验收/测试面的逐条取证，事实 only、无建议。
> 结论见 https://github.com/2017fighting/mdc-lite/issues/7 的结票评论。

# t7 — old-repo recon: acceptance / testing strategy (facts only)

Repo under examination: `/root/clone/open-mdcng`, checked-out branch `rewrite` (the Go rewrite, "old repo").
The same git object store also holds an unrelated-history branch `origin/main` = the reverse-engineering
work for `mdc-ng-re2` (`git merge-base origin/main origin/rewrite` → empty; `git ls-tree origin/main` shows
`report/ evidence/ recovered/ scope.md …`, no `REWRITE.md`).

Baseline numbers, measured (`go test` funcs via `grep -rhE '^func Test' --include='*_test.go'`):

| metric | value | command |
|---|---|---|
| commits on `rewrite` | 122 | `git log --oneline \| wc -l` |
| ADRs | 19 | `ls docs/adr/*.md \| wc -l` |
| ticket files | 68 (53 `**Status:** done` + 15 `Status: resolved`) | `ls .scratch/open-mdcng/issues/*.md \| wc -l` |
| `_test.go` files | 106 | `find . -name '*_test.go' \| wc -l` |
| `func Test…` in those files | **787** | see above |
| Go LOC | 64 944 | `find . -name '*.go' -exec cat {} + \| wc -l` |
| packages containing tests | 18 dirs | `find . -name '*_test.go' -exec dirname {} \; \| sort -u \| wc -l` |
| acceptance test | 1 `TestAcceptance` + 22 `t.Run` subtests | `acceptance/acceptance_test.go:96` |
| Playwright specs | 2 files, 11 `test(` occurrences | `web/e2e/accessibility.spec.ts` (8), `web/e2e/polling.spec.ts` (3) |

The "787 Go tests + one acceptance" framing is accurate as a count of test functions; the phrase
"7 次空转的检查" itself appears **only** in commit messages and in one ticket file, never as an enumerated list.
Provenance of the phrase in the new repo's own sessions: `/root/.pi/agent/sessions/--root-clone-mdc-lite--/*.jsonl`
(e.g. `2026-10-26T08-07-06-755Z_01a1773f-…jsonl` contains a prior recon that reported
`go test funcs total: 787` and the summary line "空转的检查被反复重修"), and the wayfinder ticket body
`.scratch/open-mdcng/…`-style text in those sessions: "旧仓 7 次「空转的检查」（canary 恒等式、44px 规则是死代码、
门禁漏扫详情页、退出码被管道改写）".

---

## 1. The "7 次空转的检查"

### 1a. The canary that was a tautology/identity — DOCUMENTED

- Commit: `74dacdc` — `fix(web): 那条 canary 断言是恒等式——第七次「空转的检查」` (2026-10-01).
- Ticket text: `.scratch/open-mdcng/issues/72-frontend-rewrite-shadcn.md:88` —
  `**第七次：我为修这个模式而写的守卫，自己就是空转的。**`
- The check, verbatim from the commit body:
  ```
  expect(routes().length).toBe(STATIC_ROUTES.length + DETAIL_ROUTES.length)
  ```
  and, in the same commit body: "而 `routes()` 就是这两个数组的拼接，所以这是恒等式，**永远不可能失败**。"
- Where it lived: `web/e2e/accessibility.spec.ts`, test `'every route is covered'` (pre-fix). The
  definition it compared against is still there: `web/e2e/accessibility.spec.ts:61`
  `const routes = (): [name: string, path: string][] => [...STATIC_ROUTES, ...DETAIL_ROUTES]`.
- Why it could not fail: the left side was defined as the concatenation of the two arrays on the right
  (`74dacdc` diff, removed block: `expect(routes().length).toBe(STATIC_ROUTES.length + DETAIL_ROUTES.length)`).
- Replacement (current code): `web/e2e/accessibility.spec.ts:88-106` — duplicate-path check, exactly-4
  detail routes, each detail path matched against `/^\/tasks\/\d+(\?|$)/`, and `seededId > 0`.
  Falsified by hand: "把 id 换成 `undefined` 后报红" (`74dacdc` body).
- Related follow-up (a *different* failure shape, author counts it as the 8th): `89b9d0b` —
  `fix(web): canary 的正则只锚定路径，别把 tab 的 query string 也算进去`. The first regex was
  `/^\/tasks\/\d+$/`, which is false for 3 of 4 real detail routes because they carry `?tab=…`
  (`Received string: "/tasks/3?tab=metadata"`). Author's own taxonomy: "前七次是**测量指向了错的东西**，
  这一次是**守卫自己的措辞过严**".

### 1b. The 44px rule that was dead code — DOCUMENTED

- Commit: `493fcb1` — `fix(web,ci): 44px 规则是死代码；CI 首次跑就抓到它` (2026-09-30).
- Ticket text: `.scratch/open-mdcng/issues/72-frontend-rewrite-shadcn.md:71` — `**第四课：CI 是唯一能揭穿「本机绿色」的地方。**`
- Where it lived: `web/src/index.css`, inside `@media (pointer: coarse)`:
  ```css
  @media (pointer: coarse) {
    .touch-target {
      min-height: 44px;
      min-width: 44px;
    }
  }
  ```
  Introduced by `5221290` (`build(web): Tailwind v4 + shadcn 工具链…`, `git log -S".touch-target" -- web/src/index.css`),
  and **no element in the tree ever carried the class** — `git log -S".touch-target"` returns only `5221290`
  and the removal `493fcb1`.
- Why it could not fail: the gate asserts `44` on real boxes, but the CSS rule matched no element, so the
  assertion was never exercised with the intended minimum. Local runs are *fine* pointers (24px floor) and
  stayed green; CI runs the `phone` profile. Commit body: "手机上实际是 32–36px。本机单次跑是 fine pointer
  （24px 地板）所以全绿，CI 跑 phone 档，21 个测试失败"; and "**一条匹配不到元素的 CSS 规则，和一门指向错误
  目标的门禁，是同一种谎。**"
- Current state: `web/src/index.css:406` (`@media (pointer: coarse)`) now scopes to
  `button, a[href], input:not([type='checkbox']):not([type='radio']), select, textarea, [role='tab'], [role='button']`
  with `min-height: 44px` at `:414` and `min-width: 44px` at `:437`; the explanatory comment is at `:391-405`,
  including "The scope is the interactive element list, NOT a `.touch-target` class."
- Same commit caught a second real defect on the phone profile: two controls sharing one accessible name
  (table-header sort button `按番号升序` vs the sort strip's visible `番号`; on phones the header is hidden).

### 1c. The CI gate that failed to scan the detail pages — DOCUMENTED

- Commit: `db9fd0a` — `fix(web): 可访问性门禁漏扫了整个任务详情页，并修它暴露出的 JSON 滚动区` (2026-09-30).
- Ticket text: `.scratch/open-mdcng/issues/72-frontend-rewrite-shadcn.md:51` — `**第二课：一个从没被看见失败过的门禁，可能根本没在检查东西。**`
- Where it lived: `web/e2e/accessibility.spec.ts`. Pre-fix shape (from the `db9fd0a` diff):
  ```ts
  let detailRoutes: [name: string, path: string][] = []
  test.beforeAll(async ({ request }) => { … detailRoutes = [ … ] })
  const routes = () => [...STATIC_ROUTES, ...detailRoutes]
  ```
- Why it could not fail: `test()` registers cases **while the file body is evaluated**, so the four detail
  routes computed in `beforeAll` never became tests. Commit body: "**只跑了 10 个测试而不是 20 个，UI 最多的
  那一页从没被扫过。**" Coverage went 86 → 153 assertions after the fix.
- How it was found: the falsification script (`verify-gate-fails.sh`) "先证明了**门禁本身是空的**".
- Fixed shape: `web/e2e/accessibility.spec.ts:33-58` — the id is resolved at file scope via top-level `await`
  (`const seededId = await (async () => …)()`), and `DETAIL_ROUTES` is a `const` built from it.
- The fix also surfaced a real WCAG 2.1.1 violation (metadata tab's `overflow-auto` JSON `<pre>` keyboard-unreachable).

### 1d. Exit codes rewritten by a pipe — DOCUMENTED

- Commits: `907a586` — `docs(issue-72): 退出码会被管道改写——第四个「测量在说谎」的实例`;
  `f7833eb` — `docs(web): e2e-tools 说明——别用管道吃掉脚本的退出码（这个错我犯过两次）`.
- Ticket text: `.scratch/open-mdcng/issues/72-frontend-rewrite-shadcn.md:64` — `**第三课：退出码也会被外层包装悄悄改写。**`
- Where it lived: the *invocation* of the falsification script, not the script. Commit body `907a586`:
  "后台任务把那次证伪跑报成 succeeded，而它的输出里印着 FAIL。原因是退出码取自管道最后一个命令（`| tail -25`），
  `PIPESTATUS` 在换行之后已失效。"
- Why it could not fail: `script.sh | tail -25` makes the shell report `tail`'s status (0), so a FAIL run is
  recorded as `succeeded`. The script's own output contradicted it.
- Contract now written down: `web/e2e-tools/README.md:14` "**Do not pipe it.** Its exit status *is* the answer,
  and a pipe replaces it with the last command's"; `:22` "It is listed here because the failure mode is silent
  and it always reads as good news"; `:80` "**Do not pipe either script**".
- In the scripts: `web/e2e-tools/verify-gate-fails.sh:112-120` — "The exit status is this script's contract, so
  it must not be able to be lost"; `web/e2e-tools/verify-polling-gate-fails.sh:26` — "Exit status: 0 when the
  gate failed as expected. Do NOT pipe this script".
- In CI: `.github/workflows/ci.yml:208-213` — "`set -o pipefail` is not needed here (no pipes), and that is
  deliberate: piping these scripts would replace their exit status with the last command's, which is a mistake
  that was made twice by hand."

### 1e. Other documented instances (the "seven" cannot be reconstructed exactly)

**The repo never enumerates a list of seven.** Only these ordinals exist in the corpus:

| ordinal | instance | source |
|---|---|---|
| 第一课 | gate pointed at a **static mockup** on `localhost:8090`, not the app; reported "32/32 全清" twice; real app started at 2/84 | issue-72:47; `d580d76` |
| 第二课 | gate never registered the detail pages (= 1c) | issue-72:51 |
| 第三课 / "第四个「测量在说谎」的实例" | exit code eaten by a pipe (= 1d) | issue-72:64; `907a586` |
| 第四课 | 44px rule is dead code (= 1b) | issue-72:71 |
| 第七次 | canary assertion is an identity (= 1a) | issue-72:88; `74dacdc` |
| 第八次 (different shape) | canary regex too strict, `?tab=` query string | `89b9d0b` |

So **ordinals 5 and 6 are not documented anywhere I could find** — they are only implied by the jump
第四课 → 第七次. The phrase "at least seven" in the new repo's sessions is the parent agent's paraphrase of the
git log, not a list in the old repo.

Other *documented* "a check that looks like a guarantee and checks nothing" instances in the same repo
(these are candidates for the unnumbered slots, all with evidence):

| # | what | where / why it could not fail |
|---|---|---|
| i | Falsification script rebuilt only the Go binary, not the frontend → the embedded bundle was the **previous** one, so the deliberately broken token was never in the artifact → the gate passed for the wrong reason | `db9fd0a` body: "只重建了 Go 二进制、没重建前端，于是嵌进去的是**上一个** bundle，坏 token 根本没进产物，门禁当然通过" |
| ii | The same script's artifact check used `grep bbbbbb`, but Tailwind compresses the hex to `#bbb` → **that check always failed** (a false alarm) | `db9fd0a` body |
| iii | Polling cadence was invisible to the a11y gate: "把它改成常量，174 项可访问性断言全绿，行为却已经错了" | `f292474` (adds `web/e2e/polling.spec.ts`) |
| iv | `::after` "phantom hit area" — enlarged the visual hit area without enlarging the element, so neither Playwright nor axe could see it: "一句门禁无法检验的声明" | issue-72 §"我自己写错并被工具抓到的两处" |
| v | `@media (pointer: coarse)` block placed **before** the component rules that overrode it → the 24px checkbox never took effect on touch | issue-72 same section; comment at `web/src/index.css:391` |
| vi | `@tanstack/react-query` "从未被任何源码 import（0 处 useQuery/QueryClient，产物里 0 痕迹）", while ADR-0018 + ticket 30's reversal + ticket 72 all recorded it as adopted | `d374b7b`; `f870812`; caught by `internal/licencecheck/npm_attest_test.go` |
| vii | A test named `partial` but asserting `success`: the scrape test double dropped `summary.Missing`, so the pipeline could never reach `partial` under test | `fd7c4c8` (`feat(scrape,organize,store): 元数据合并收口——超时冻结为 partial（票 27）`): "这正是那条名字说 partial、断言却写 success 的测试的成因" |
| viii | Library-refresh success criterion: any 2xx counted as success, but Jellyfin/Emby answer **204 for a path that does not exist and for an empty body** → the "path refresh failed → full-library refresh" fallback could essentially never trigger | ticket 40 `.scratch/open-mdcng/issues/40-path-refresh-success-criterion.md` (measured matrix), found by ticket 33 on a real server |
| ix | The declarative-source golden tests passed for `avbase`/`airav` whose selectors matched **nothing** on the real DOM — "因为票 19 的 golden 测试没有真实夹具约束它，一个错的定义能带着绿灯通过" | `.scratch/open-mdcng/issues/39-fixtures-must-come-from-real-captures.md` |
| x | Diagnostic tool used `.badge`, a class from the deleted mockup: "工具正确地报了 NOT FOUND，错的是「类名能跨重写存活」这个假设" | `b1b9b31` body |
| xi | `reuseExistingServer: !process.env.CI` made the new CI falsification step fail in CI while passing locally ("**又一次「本机绿、CI 红」**") | `02a9a75` body |
| xii | CI job coupling: the a11y step was killed with "**没有结论、没有日志**", and every later step (Go suite, licence attestation, binary smoke test) silently never ran | `493fcb1` body |

Related-but-different: the **reverse-engineering** work (`origin/main`, i.e. `git show 8369f4d:HANDOFF.md`
§ 重要陷阱) keeps its own trap list of measurements that lied — trap 12 (SVG path de-whitespacing produced a
false match and inflated the corpus 172→183), trap 13/18 (string adjacency ≠ ownership; `strings` doesn't
print non-ASCII), trap 14 (`archify deliver` exits 0 but may be `ok:false` and keeps the *previous* artifact),
trap 15 (`archify visual-check` defaults to `skipped`, not pass), trap 17 (`xref_scan.py` covers ~54 % and
silently drops the rest — "**不能用「dump 里 grep 不到」断言「字符串/逻辑不存在」**"), trap 19 (over-correction:
"I couldn't reproduce it" was treated as "it isn't reproducible"). These are not Go-repo test defects.

---

## 2. Old-repo test layout

### Go tests

- 106 `_test.go` files, 787 `func Test…`, 92 `t.Run(` calls inside them, spread over 18 directories:
  `scrape` 18, `store` 15, `api` 15, `fetch` 13, `config` 10, `organize` 9, `plugins` 7, `parser` 7,
  `internal/licencecheck` 2, `ingest` 2, then one each in `retention`, `poll`, `internal/web`,
  `internal/pgtest`, `internal/clock`, `internal/apierr`, `cmd/mdcng`, `acceptance`.
- The acceptance package is behind a build tag: `acceptance/acceptance_test.go:1` `//go:build acceptance`
  and `acceptance/doc.go` "It is build-tagged and never part of `make test`. It needs Docker, a …".
  Therefore `go test ./...` (what CI runs) **excludes it**.
- Postgres leg: `MDCNG_REQUIRE_POSTGRES=1` is set in CI so a missing server is a failure, not a skip
  (`.github/workflows/ci.yml:39-45`), plus an explicit "Record that the Postgres leg ran" step that greps
  the `-v` output (`ci.yml:96-106`).

### `/root/clone/open-mdcng/acceptance` (read in full: `acceptance/README.md`, 1 775-line test)

Contents: `acceptance_test.go`, `README.md`, `doc.go`, `config.toml.tmpl`, `docker-compose.yml`,
`sources-fixture.toml`, `sources-real.toml`, `names-all.txt`, `names-seeded.tsv`, `names-late.tsv`,
`names-library-primer.tsv`, `capture/`, `fixtureserver/`, `fixtures/html/*.html` (21 pages) +
`fixtures/numbers.tsv`, `internal/seed/`, `parseprobe/`, `rules/`, `seed/`, `setupserver/`.

What it exercises (22 subtests, `acceptance/acceptance_test.go:96` onward) — mdcng side:
batch counts are the fold of per-file outcomes; residue/empty dirs produce nothing; no release reaches the
isolation list; the fixture source is asked for exactly the expected numbers, once per task; unserved numbers
end `partial` with the 404 recorded; a javbus scrape ends `partial` with exactly the 7 fields javbus lacks;
scraped fields reach `movie.nfo`; companions placed beside their primary; cover lands as `fanart.jpg` and no
`poster.jpg`; three discs land in one directory; a retry changes nothing. Jellyfin side:
the library is indexed without the test asking for a full refresh; a path-scoped refresh really triggers a
scan; `jellyfin reads movie.nfo` (`acceptance/acceptance_test.go:626`) asserts `item.Name == nfo.Title` and
`item.PremiereDate` has the nfo's `Premiered`; two versions aggregate into one entry with two media sources;
a hand-typed number on a planted isolation task reaches the queue and the library; a foreign video in the
directory stops `movie.nfo` from being read. Triggers: `POST /api/scan/create`, `POST /api/hooks/qbittorrent`,
and the directory poller all run, with the dedup gate asserted.

How it is run (`Makefile:44-70`): `make accept` = `accept-reset` + `accept-up` +
`go test -tags acceptance -count=1 -v -timeout 30m ./acceptance/`. `make accept` "takes about two and a
half minutes" (`acceptance/README.md`). Requires Go, ffmpeg, Docker. Also `make accept-real`
(same env, `ACCEPT_SOURCES=real`, real javdb+javbus, corpus re-seeded after the poller's watermark moves),
`make accept-fixtures` (network, once), `make accept-emby`, `make accept-logs`, `make accept-down`.

**Was it gated in CI? No.** `grep -n accept .github/workflows/ci.yml` returns nothing; no `-tags acceptance`
and no `make accept` anywhere in `.github/workflows/`. The Makefile states the intent explicitly:
"Nothing here runs under `make test`: it needs Docker and a gigabyte of image, and a test suite that takes
minutes is one that stops being run." (`Makefile:36-39`).

### Makefile targets

`all`, `build`, `web`, `build-check`, `typecheck`, `test`, `test-go`, `licence`, `test-web`, `test-a11y`,
`vet`, `fmt`, `tidy`, `run`, `clean`, `help`, plus the acceptance family above. `make test` = `test-web
typecheck test-go`. `test-a11y` is deliberately **not** part of `make test` ("Not part of `make test`: it
needs a browser (a ~150 MB download on first run) and it builds and starts the binary").

### CI config

One file: `.github/workflows/ci.yml`, two jobs.

- `test` (`.github/workflows/ci.yml:6`): Postgres 16 service container with healthcheck; env
  `MDCNG_TEST_POSTGRES_DSN` + `MDCNG_REQUIRE_POSTGRES=1`; steps = build web interface (`npm ci && npm run
  build`), verify `internal/web/dist/index.html` exists, `go vet ./...`, `make licence`, `go test ./...`,
  record that the Postgres leg ran, build the single binary, smoke-test the binary with `PATH=/usr/bin:/bin`
  (no Node) and curl `/api/health` + `/tasks | grep -q 'id="root"'`.
- `a11y` (`ci.yml:139`): own job, `timeout-minutes: 20`; install frontend deps, cache `~/.cache/ms-playwright`,
  `npx playwright install --with-deps chromium` (15 min timeout), `npm run build`, `npm run test:a11y`
  (15 min timeout), upload the HTML report on failure, then `Prove the gates can fail` = run
  `web/e2e-tools/verify-gate-fails.sh` and `web/e2e-tools/verify-polling-gate-fails.sh` with
  `MDCNG_E2E_PORT: '18083'`.
  The separate job is itself a lesson: `ci.yml:126-138` — "when it shared the `test` job, the job was killed
  while running the gate -- no step conclusion, no log line -- and every later step … silently never ran."

**Did CI run against real sites?** No. The default Go suite runs against committed fixtures; the spec fixes
this as a rule — `.scratch/open-mdcng/spec.md:732`: "**自动化的边界**：真实目标站（javbus/avbase/dmm）
**不进自动化测试**——它们会变、会限流、会封 IP。源的正确性靠「单源测试」这个手动动作
（`POST /api/config/sources/{id}/test`）在用户环境里验证，测试里用固化的 HTML 夹具。"
`docs/agents/fixtures.md` § *Drift sentinel: the decision is no* records the explicit decision **not** to add a
CI drift sentinel ("CI is a datacenter IP that the sites already treat differently … a check that is usually
red is a check nobody reads"). The only CI thing that talks to a network is `docker pull`/`npm ci`.
The a11y job starts a **real mdcng binary** but with no external fetches: seeding goes through
`POST /api/scan/create` with a client-supplied path list (`web/e2e/global-setup.ts:27-38`).

---

## 3. Fixtures

Three separate fixture sets, all with provenance discipline:

1. **`scrape/testdata/`** — 22 entries: `javbus_detail.html`, `airav_*` (4), `avbase_*` (3), `fc2_detail.html`
   / `fc2_miss.html`, `dmm_search.json` / `dmm_content.json`, `javdb_*` (6), plus `README.md` and
   **`fixtures.yaml`** (21 rows, machine-readable manifest). Capture rows carry
   `url / status / bytes / captured / sha256`; constructed rows carry `reason / note`.
2. **`fetch/testdata/`** — the challenge-detector's evidence (`airav_search_ssis001.html`, `avbase_404.html`,
   `cloudflare_interstitial.html`) with its own README stating "Two are real captures, kept verbatim (no
   trimming, no rewriting); the third is constructed and says so in the file", plus URL/date/bytes/sha256.
3. **`acceptance/fixtures/html/`** — 21 committed javbus detail pages + `fixtures/numbers.tsv`; images go to
   `acceptance/.cache/pics/` (gitignored).

Capture mechanism:

- Acceptance: `acceptance/capture/main.go` (`make accept-fixtures` → `go run ./acceptance/capture`).
  Header comment: "Command capture fills acceptance/fixtures/html from the live site, once. It is the one
  step of the acceptance environment that touches the network. … which is what makes the run reproducible and
  what keeps the sharpest rule in this repository — **fixtures come from real captures, never from imagination
  (ticket 39)** — true for the environment as well as for the unit tests."
  It deliberately does not rewrite the HTML ("A rewritten page would be a weaker claim: the bytes under test
  would no longer be the bytes the site sent") and does not commit images. Pacing `pace = 5 * time.Second`,
  3 retries, javbus age cookie `"age=verified; existmag=all; dv=1"`, and a `Referer` because
  "javbus refuses an image request without one with 403, and a 403 in a fixture is a poster that will be
  missing three steps later with nothing to say why" — "The capture therefore treats a non-200 image as fatal".
- Unit fixtures: refreshed **by hand**, per `docs/agents/fixtures.md` § *Adding or refreshing a capture*:
  "**Fetch it through this program's own Fetcher.** A plain `curl` is refused by some of these sites' edges;
  the TLS fingerprint is part of what gets the page"; write the body verbatim, update the source's golden
  expectations in the same change ("If the new page changes what a definition extracts, that difference is
  the finding — do not adjust the capture to preserve the old expectations"), update the manifest row, run
  `go test ./scrape`.

The rule, quoted:

- `docs/agents/fixtures.md`: "**A capture is verbatim.** No trimming, no rewriting, no injected comment — a
  comment is an edit to the evidence." / "**Every built-in declarative source has at least one real capture.**
  A declarative source's whole input surface is the page, so a page written to match its selectors agrees with
  it by construction and proves nothing. This is an assertion, not advice:
  `TestFixtures_BuiltinDeclarativeSourcesHaveARealCapture` fails if a declarative source has no capture, and
  it refuses a constructed fixture beside one unless the reason is `defect-reproduction`."
- Ticket 39 `.scratch/open-mdcng/issues/39-fixtures-must-come-from-real-captures.md`:
  "**固化 HTML 夹具必须来自真实抓取，且源定义要对着真实页面写**"; the two allowed constructed reasons are
  `defect-reproduction` and native-source `parser-shape`, "The reasons are a closed set in
  `scrape/fixtures_test.go`".
- Enforcement tests: `scrape/fixtures_test.go` — manifest↔directory coverage, capture bytes/sha256 match
  (`TestFixtures_CapturesMatchTheirBytes`), constructed fixtures need a closed-set reason + note, and
  `TestFixtures_BuiltinDeclarativeSourcesHaveARealCapture`.
- Cadence: `docs/agents/fixtures.md` lists when to re-run the single-source action (after any `scrape/`/`fetch/`
  change; after changing a source's `[[sources]]` entry; before tagging a release; when a task in the wild
  fails; when the site moves) and where the record lives: `docs/source-notes.md` § *Last single-source
  verification* (filled with the 2026-09-25 run of five sources, including the failing one).
- Caveat recorded in the manifest itself: "a capture is a point-in-time artifact, not a reproducible one:
  javbus serves rotating advertising, so re-capturing the same URL yields the same size and a different hash."

The acceptance fixture source is javbus's **own definition** with `http://{domain}` pointed at the local
fixture server, "so the real scrape engine, the real DSL and the real selectors run — against real javbus
pages, captured once" (`acceptance/README.md`).

---

## 4. Coverage

**Not found.** No coverage measurement, no coverage target, no coverage gate anywhere:

- `grep -rn "coverage\|coverpkg\|coverprofile\|-cover" Makefile .github/ web/package.json docs/ CONTEXT.md`
  → only one hit, unrelated prose: `docs/agents/fixtures.md:45` ("a typo in a row would silently exempt that
  file from the coverage check" — about the manifest, not code coverage).
- No `codecov.yml`, no `--cover` in the Makefile or CI.
- The only occurrences of "覆盖率/coverage" in git history are denials of the practice:
  `store/backend_external_test.go:16` "The point is not coverage of Postgres for its own sake. It is that
  'the schema …'" and a ticket-31 commit body "票 31 的意义不是「给 Postgres 加覆盖率」，而是把「schema 是纯
  标准 SQL、两个后端…".
- No evidence found that tests were written for coverage's sake. The nearest thing to a stated testing
  philosophy is `.scratch/open-mdcng/spec.md:690-729`, which assigns *per-package* test kinds
  (table-driven+golden for the declarative engine, parameterized dual-backend integration for `store`,
  `httptest` integration for `api`, fake-`Source` orchestration for the scheduler) and names the single most
  important test: the fake-source assertion of the exact **request sequence** for the minimum-request promise
  ("这是最少请求承诺的**唯一**可测断言点").

---

## 5. Frontend testing

- Framework: **Playwright** (`@playwright/test ^1.63.0`) + **`@axe-core/playwright` ^4.13.0**. No vitest, no
  jest, no cypress anywhere in `web/package.json` or `web/package-lock.json`.
- Scripts (`web/package.json`): `"test:a11y": "playwright test --reporter=list"`,
  `"test:a11y:install": "playwright install --with-deps chromium"`; `"build": "tsc --noEmit && vite build"`,
  `"typecheck": "tsc --noEmit"`. There is no unit-test runner for the frontend at all — the only frontend
  gate is the browser suite.
- Specs: `web/e2e/accessibility.spec.ts` (axe with tags `wcag2a/wcag2aa/wcag21a/wcag21aa/wcag22aa`,
  touch/click target size `MIN_TARGET = 24` / `MIN_TARGET_TOUCH = 44`, and "every interactive element has an
  accessible name"; plus 3 URL-state behaviour tests) and `web/e2e/polling.spec.ts` (3 tests × 2 themes ×
  3 widths: idle ~5s and really refreshing, busy ~1s, interval follows the selection). Config:
  `web/playwright.config.ts` — 3 projects `desktop` 1440, `tablet` 768, `phone` (Pixel 5); `retries: CI ? 1 : 0`;
  `reuseExistingServer: MDCNG_REUSE_SERVER === '1' ? true : !process.env.CI`; `webServer` builds and starts
  the real Go binary against a throwaway `/tmp/mdcng-e2e-data`; `globalSetup: './e2e/global-setup.ts'` seeds
  through the public API.
- CI wiring: the `a11y` job (see §2), with browser caching, a 15-minute install timeout, a 15-minute gate
  timeout, report upload on failure, and the two falsification scripts in the same job.
  `web/e2e-tools/` holds those scripts plus three measurement tools (`measure-polling.mjs`,
  `report-colors.mjs`, `scan-layout.mjs`) explicitly "not gates".
- Gate history in numbers (from `493fcb1`/`db9fd0a` bodies and issue-72's table): pre-rewrite baseline
  2/84 axe-clean combos, 120 contrast violations, 5–98 sub-44px targets per page; after the rewrite 84/84;
  gate coverage 86 → 153 assertions; final local CI-semantics run 174 passed.

---

## 6. Real end-to-end acceptance against a running Jellyfin/Emby

Yes, and it is a first-class artefact (ticket 51 `b2db8b1`, extended by tickets 54/57/66/68/69).

- Container file: `acceptance/docker-compose.yml` — `jellyfin/jellyfin:10.10.7` (pinned, overridable via
  `MDCNG_ACCEPT_JELLYFIN_IMAGE`), healthcheck on `/System/Info/Public`, host uid/gid, and the scratch root
  bind-mounted **at the identical absolute path** inside and outside the container ("mdcng writes absolute
  paths into its database and sends one in its refresh call, and Jellyfin answers `204` for a path it has
  never heard of — so a different mount point would look like a working refresh while nothing was scanned").
  `emby/embyserver:4.10.0.40` under `profiles: ["emby"]`.
- Launcher + setup: `Makefile` `accept-up`/`accept-prepare`/`accept-real`; `acceptance/setupserver/main.go`
  drives the wizard, creates one `movies` library, and writes the access token (it fixes ticket 33's script
  bug: `GET /Startup/User` must precede the POST, else 500 `Sequence contains no elements` on a fresh DB);
  `acceptance/seed`, `acceptance/fixtureserver`, `acceptance/internal/seed`.
- The NFO check exists and is explicit: `acceptance/acceptance_test.go:626` `t.Run("jellyfin reads movie.nfo", …)`
  compares Jellyfin's `item.Name` with the nfo's `Title` ("the nfo was not read") and its `PremiereDate` with
  the nfo's `Premiered` ("a title could be derived from the directory, a premiere date could not").
  Also `t.Run("jellyfin indexes two of the three discs, and not the third", …)`,
  `t.Run("the library is indexed without the test asking for a full refresh", …)`,
  `t.Run("the path-scoped refresh really triggers a scan", …)`.
- Emby: available, **not asserted** — `acceptance/README.md` § Known gaps: "**Emby is not asserted, only
  available.** `make accept-emby` brings up 4.10.0.40 and `setupserver -server emby` points it at the same
  library, so the README's second column can be re-measured by hand. Nothing in the test suite reads it."
- Real sites: `make accept-real` runs the same environment with `acceptance/sources-real.toml` (javdb first,
  javbus as fallback, fixture source unscheduled) and re-seeds the corpus after the poller's watermark moves.
  Its own README is explicit that the fixture-mode assertions **do not apply** there ("This mode is for
  watching real metadata land and for trying the manual paths").
- The original answers came from a manual run: ticket 33 `.scratch/open-mdcng/issues/33-real-server-acceptance.md`
  ("只有真实服务器和真实站点能定的六件事", six items answered by hand in containers, producing the README
  comparison table). `acceptance/README.md` opens by saying the environment exists because "Nothing could
  re-run them" — the scratch tooling "proved the answers once and then stopped being true the moment either
  side moved".

**"日常可用" (daily-usable): the phrase is not found in the old repo** — `git log --all --format='%s%n%b' |
grep 日常` and a full-tree grep return only incidental Japanese/Chinese page text inside fixtures. The nearest
things the old project used to judge readiness:

- `README.md:11` — "**Status: the main chain runs end to end, with the post-processing plugins in place.**
  Trigger a scan, and the tasks it creates are scraped by the field-priority scheduler, placed into your
  library with NFO and images, and the media server is told."
- `README.md` § Roadmap (`README.md:897`) — a table of ~22 areas, every row marked `done`, from "Single
  binary, embedded interface" to "Batch retention".
- The **Known gaps** section of `acceptance/README.md`, which is where the old project actually recorded what
  was *not* daily-usable: the image plugin's crop never runs on the default source set; every javbus-scraped
  task is `partial`, so `success` is never exercised; nothing renders the interface in the acceptance run;
  `-uncensored` uncovered; the family serial-width table has one corpus case; Emby not asserted; the
  zero-request skip not exercised; `348NTR-082`'s layout undefined.
- The user's own verdict exists only in the **new** repo's session logs (mdc-lite), e.g. the option text
  "范围失控，主链没到日常可用 … 番号只覆盖半个语料（票 55）、轮询把成对文件拆散（票 68）、dmm/avbase 定义反复重写"
  (`/root/.pi/agent/sessions/--root-clone-mdc-lite--/2026-10-26T08-07-06-755Z_01a1773f-….jsonl`). That is a
  user judgement, not an artefact of the old repo.

---

## Appendix — commands used (reproducible)

```sh
git -C /root/clone/open-mdcng log --oneline | wc -l                     # 122
ls /root/clone/open-mdcng/docs/adr/*.md | wc -l                         # 19
ls /root/clone/open-mdcng/.scratch/open-mdcng/issues/*.md | wc -l       # 68
grep -rhE '^func Test' --include='*_test.go' /root/clone/open-mdcng | wc -l   # 787
find /root/clone/open-mdcng -name '*_test.go' | wc -l                   # 106
grep -c 't.Run(' /root/clone/open-mdcng/acceptance/acceptance_test.go   # 22
git -C /root/clone/open-mdcng show 74dacdc -- web/e2e/accessibility.spec.ts
git -C /root/clone/open-mdcng show db9fd0a -- web/e2e/accessibility.spec.ts
git -C /root/clone/open-mdcng show 493fcb1 -- web/src/index.css
git -C /root/clone/open-mdcng show 5221290:web/src/index.css | grep -n 'touch-target'   # the dead rule's origin
grep -n accept /root/clone/open-mdcng/.github/workflows/ci.yml          # (no output: acceptance not in CI)
git -C /root/clone/open-mdcng show 8369f4d:HANDOFF.md                   # reverse work's trap list (traps 11-22)
```
