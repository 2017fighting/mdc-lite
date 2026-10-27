# Actor gender across V1 scrapers — verified facts

Scope: mdc-lite Go rewrite, planning only. Question: which V1 scrapers can supply
`Actor{Name, Gender}` with `Gender ∈ {female, male, unknown}`, and what is javdb's
exact integer `gender` mapping. All evidence below was produced live in this
container on 2026-10-27 unless a file:line reference to the reference repo
`/root/clone/open-mdcng` is given.

## 0. Network

```
$ env | grep -i proxy
(no output — no proxy variables set)

$ curl -s -o /dev/null -w '%{http_code}\n' \
    -A 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) ... Chrome/120.0.0.0 Safari/537.36' \
    --max-time 20 https://jdforrepam.com/
404
```

`404` (not a TLS error) means the TCP+TLS handshake succeeded; the host is
reachable. `javdb.com` remains TLS-blocked; the APP API hosts are not.

---

## 1. javdb — CAN supply gender. `0 = female`, `1 = male`. CONFIRMED.

### 1.1 Request shape (reproducible)

Helper `/tmp/jd.sh` (salt/key from the prior ticket):

```bash
SALT="lpw6vgqzsp"
KEY="71cf27bb3c0bcdf207b64abecddc970098c7421ee7203b9cdae54478478a199e7d5a6e1a57691123c1a931c057842fb73ba3b3c83bcd69c17ccf174081e3d8aa"
UA="Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
DOMAIN="jdforrepam.com"
TS=$(date +%s); SIG=$(printf '%s' "${TS}${KEY}" | md5sum | cut -d' ' -f1)
curl -s --max-time 25 -A "$UA" -H "jdsignature: ${TS}.${SALT}.${SIG}" -H "Accept: application/json" \
  "https://${DOMAIN}${1}"
```

Search: `/api/v2/search?q={number}&from_recent=false&type=movie&movie_type=all&movie_sort_by=relevance&movie_filter_by=all&page=1&limit=24`
Detail: `/api/v4/movies/{id}?from_rankings=false`

Requests were spaced with `sleep 3`–`sleep 4`. All succeeded.

### 1.2 Where gender lives

`gender` occurs in the detail JSON **only** inside `data.movie.actors[]`. For
`IPX-535` (movie id `XzkY4`), `json.dumps(detail).count('"gender"') == 5` and a
recursive walk found exactly those 5 keys, all under `.data.movie.actors[N]`.
The search response carries no `actors` and no `gender`. There is **no** working
actor endpoint (`/api/v4/actors/{id}`, `/api/v3/actors/{id}`, `/api/v4/actor/{id}`
all returned `{"status":404,"error":"Not Found"}`).

### 1.3 Raw JSON — SSIS-001 (movie id `ZY5eq`)

```json
"actors":[
 {"id":"A5yq","name":"葵つかさ","gender":0,"avatar_url":"https://tp.spfcas.com/rhe951l4q/avatars/a5/A5yq.jpg"},
 {"id":"AGR0","name":"乙白さやか","gender":0,"avatar_url":"https://tp.spfcas.com/rhe951l4q/avatars/ag/AGR0.jpg"},
 {"id":"8VNb9","name":"平田司","gender":1,"avatar_url":"https://tp.spfcas.com/rhe951l4q/avatars/8v/8VNb9.jpg"}
]
```

Identical to the reference fixture `open-mdcng/scrape/testdata/javdb_detail_ssis001.json`.

### 1.4 Raw JSON — IPX-535 (movie id `XzkY4`)

```json
"actors":[
 {"id":"bvWB","name":"桜空もも","gender":0,"avatar_url":".../bv/bvWB.jpg"},
 {"id":"G955","name":"小田切ジュン","gender":1,"avatar_url":".../g9/G955.jpg"},
 {"id":"K4m3Q","name":"貞松大輔","gender":1,"avatar_url":".../k4/K4m3Q.jpg"},
 {"id":"65x3M","name":"野島誠","gender":1,"avatar_url":".../65/65x3M.jpg"},
 {"id":"MmXaX","name":"黒田悠斗","gender":1,"avatar_url":".../mm/MmXaX.jpg"}
]
```

### 1.5 Proof of the mapping — two independent confirmations per side

Cross-checked against Japanese Wikipedia (a source outside javdb and outside the
reference repo). Raw payloads saved under `/tmp/gender-evidence/`.

Male side (`gender == 1`):

| javdb name (id) | javdb gender | independent evidence | quote |
|---|---|---|---|
| 黒田悠斗 (`MmXaX`) | 1 | ja.wikipedia `黒田悠斗` (Q20644177) | `黒田 悠斗 は、日本のAV男優、AV監督。` — **AV男優 = male AV actor** |
| 小田切ジュン (`G955`) | 1 | ja.wikipedia search `小田切ジュン` | `…（おだぎり じゅん、1978年1月12日 - ）は、日本のAV男優。` — **AV男優 = male AV actor** |

Female side (`gender == 0`):

| javdb name (id) | javdb gender | independent evidence | quote |
|---|---|---|---|
| 桜空もも (`bvWB`) | 0 | ja.wikipedia `桜空もも` | `桜空 もも は、日本のAV女優。` — **AV女優 = female AV actress** |
| 葵つかさ (`A5yq`) | 0 | ja.wikipedia `葵つかさ` | `葵 つかさ は、日本の元AV女優…` — female |
| 乙白さやか (`AGR0`) | 0 | ja.wikipedia `乙白さやか` | `乙白 さやか は、日本の元AV女優。` — female |

Two further corroborations that `1` is not "female":
* javbus lists the `gender:0` actresses of SSIS-001 and IPX-535 but **omits every
  `gender:1` actor** from the same movies (see §2) — i.e. a different site treats
  the `1` set as non-actresses.
* The same actor keeps the same value across unrelated movies:
  `黒田悠斗 (MmXaX) = 1` in ABP-001, ABP-002, FSDSS-001, IPX-535, SONE-001,
  SONE-002; `貞松大輔 (K4m3Q) = 1` in IPX-535, PRED-001, SONE-002;
  `鮫島 (9gAX) = 1` in FSDSS-002, MIDV-001, PRED-001.

The reference repo independently asserts the same mapping
(`open-mdcng/scrape/javdb.go:297` — "Gender is 0 for a performer and 1 for a male
actor"; `open-mdcng/docs/source-notes.md:170-174`), but that is not the proof —
the Wikipedia quotes above are.

### 1.6 Distinct values observed and anomalies

35 unique movie numbers fetched (list below), **105 actor entries**:
`{0: 56, 1: 49}`. No value other than `0` and `1` was seen. No actor id changed
gender across movies.

Numbers: ABP-001/002, DASD-001, FSDSS-001/002, HND-001/002, IPX-535/536,
JUQ-001/002, MIAA-001/002, MIDE-001/002/003, MIDV-001/002, MIGD-001/002,
MIRD-001, NHDTA-001, PPPD-001, PRED-001, RCTD-001, SDDE-001, SONE-001/002,
SSIS-001/002, STARS-001/002, VRTM-001, WANZ-001/002.

* **No gender data error found.** Every `gender:1` name is an established male AV
  actor (平田司, 小田切ジュン, 貞松大輔, 野島誠, 黒田悠斗, 清水健, 鮫島, 吉村文孝,
  イェーイ高島, イセドン内村, マッスル澤野, トニー大木, 矢野慎二, 田淵正浩, 吉村卓,
  南佳也, ピエール剣, 森林原人, セツネヒデユキ, 戸川夏也, 一条真斗, 藍井優太,
  浅野あたる, 阿部智広, 大島丈, 奥村友真, 大沢真司, 横山大輔, 吉野篤史, 松山伸也,
  およよ中野, 杉浦ボッ樹, 忍野雅一, えりぐち); every `gender:0` name is a
  female actress. No counter-example appeared.
* **Name-quality anomaly (not gender):** the MIAA maker's male actors carry a
  leading full-width ideographic space U+3000 — `"\u3000大沢真司"` (MIAA-001) and
  `"\u3000横山大輔"` (MIAA-002). A rewriter must trim before comparing/merging
  names. This is a name bug, **not** a wrong gender value.
* Because the field is a plain integer, a future value (e.g. `2`) is possible; the
  rewrite should map only `0→female`, `1→male`, anything else→`unknown` rather
  than assuming a boolean.

**Conclusion:** javdb can supply gender, with a confirmed mapping
**`0 = female`, `1 = male`**; any other integer must be treated as `unknown`.

---

## 2. javbus — CANNOT supply gender.

Fetched `https://www.javbus.com/SSIS-001` and `https://www.javbus.com/IPX-535`
with header `Cookie: dv=1`, HTTP 200 each. Selector `div#star-div >
div#avatar-waterfall > a.avatar-box` holds the cast.

Raw fragment, SSIS-001 (verbatim from the live page):

```html
<div id="star-div">
	<h4 id="star-hide" style="cursor: pointer;">演員 <span class="glyphicon glyphicon-minus"></span></h4>
    <div id="avatar-waterfall">
            <a class="avatar-box" href="https://www.javbus.com/star/2xi">
            <div class="photo-frame">
                <img src="/pics/actress/2xi_a.jpg" title="葵つかさ">
            </div>
            <span>葵つかさ</span>
        </a>
            <a class="avatar-box" href="https://www.javbus.com/star/w5a">
            <div class="photo-frame">
                <img src="/pics/actress/w5a_a.jpg" title="乙白さやか">
            </div>
            <span>乙白さやか</span>
        </a>
    </div>
</div>
```

Findings:
* There is **no per-actor gender** anywhere: `gender` count 0, `男優` count 0,
  `male`/`female` count 0. `女優` appears only in the nav menu links
  (`有碼女優` / `無碼女優` = censored/uncensored actress categories), never next to
  an actor.
* The section heading is `演員` (actors/performers) — one flat list.
* The list is **female-only in practice**: SSIS-001 lists 葵つかさ, 乙白さやか but
  **not** 平田司 (javdb gender 1); IPX-535 lists only 桜空もも and none of the four
  javdb `gender:1` actors. So absence from javbus implies "not an actress", but the
  page gives no explicit marker to read.
* The actor's own star page (`/star/r62`) has no `性別`/gender field either
  (`性別` count 0); it only titles the page `… - 女優 - 影片`.
* `https://www.javbus.com/search/平田司` → HTTP 404, title `沒有您要的結果！`
  (no results): javbus does not track the male actor at all.

**Conclusion:** javbus can only ever assert `female` by presence in the actress
list; it has no markup that distinguishes male from female, and it never lists the
male actors. It cannot supply `Actor.Gender`.

---

## 3. fc2 — CANNOT supply gender (no actor list at all).

Fetched `https://adult.contents.fc2.com/article/4982870/` (HTTP 200, 107 066 B) and
`.../4997484/` (HTTP 200, 108 007 B). Both are valid product pages.

Counts on both pages (case-insensitive):
`actor = 0, 出演 = 0, 俳優 = 0, 出演者 = 0, 男優 = 0, 女優 = 0, gender = 0, cast = 0`.

The only structured "person" is the seller, not a performer:

```html
<a href="/search/?author_id=dSFCJvSF&limited=1" class="c-btn-201">限定商品一覧</a>
...
window.FC2ContentsObject.push(['T3Wnauthor', '販売者']);
```

The JSON-LD block is a `Product` with `name`/`description`/`sku` only — no
`actor`, `performer` or `cast`. Section headings are `商品タグ`, `対応デバイス`,
`サンプル画像`, `サンプル動画`, `商品説明`, `商品レビュー`, `販売者情報` — no cast
section. (A third id, `3336019`, answered the site's own "not found" page under
HTTP 200, consistent with the reference notes.)

**Conclusion:** fc2 exposes no actor list and no gender; it cannot supply
`Actor.Gender` (nor `Actor.Name`).

---

## 4. dmm — CANNOT supply gender (reference implementation + fixture).

dmm is unreachable from this container (Japan egress required), so this is read
from `/root/clone/open-mdcng`:

* `scrape/dmm.go:362-377` — the `ppvContent` GraphQL query requests only
  `actresses { name imageUrl }`. No `gender` is asked for.
* `scrape/dmm.go:145-148` — the response struct is
  `Actresses []struct { Name string \`json:"name"\`; ImageURL string \`json:"imageUrl"\` }`.
  There is no `Gender` field.
* `scrape/dmm.go:486-491` — extraction copies only `Name` and `ImageURL` into
  `Actor{Name, Thumb}`.
* `scrape/testdata/dmm_content.json` — the captured live response has
  `"actresses":[{"name":"女優 一","imageUrl":"…"}]` and contains the substring
  `gender` **0 times**; the full key list of `ppvContent` is
  `id,title,description,floor,makerContentId,deliveryStartDate,makerReleasedAt,duration,packageImage,sampleImages,sample2DMovie,sampleVRMovie,actresses,directors,series,maker,label,genres`.
* A repo-wide grep for `gender` finds it only in `scrape/javdb.go` and
  `docs/source-notes.md` (javdb section) — never in dmm code, tests or fixtures.

**Conclusion:** as consumed by the reference implementation, dmm's `ppvContent`
exposes no actor gender and none is parsed. (The live schema cannot be
introspected from here — dmm is TLS/region-blocked — so "the schema has no gender
field" is not proven; "the V1 scraper cannot supply it" is proven.)

---

## 5. Summary

| source | actor list? | gender field? | supplies `Actor.Gender`? | evidence |
|---|---|---|---|---|
| **javdb** | yes, `data.movie.actors[]` | **yes, integer** | **YES — `0=female`, `1=male`** | §1.3–1.5 |
| **javbus** | yes, actress-only | no | NO (presence ⇒ female, absence ⇒ unknown; never lists males) | §2 |
| **fc2** | no | no | NO | §3 |
| **dmm** | yes, `actresses[]` | no (not requested/parsed; absent from fixture) | NO | §4 |

For the Go rewrite's `Actor{Name, Gender}` with `Gender ∈ female|male|unknown`:

* **javdb is the only V1 source that can populate `Gender`.** Map `0→female`,
  `1→male`, any other/absent value→`unknown`. Trim the name (MIAA titles carry a
  leading U+3000). Note this contradicts the reference repo, which *drops* the
  `gender:1` actors; a `Gender` field lets the rewrite keep them instead.
* javbus, fc2 and dmm must fall back to `unknown` (or to the type's default) for
  every actor.
