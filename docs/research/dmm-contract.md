# dmm scraper contract + javdb/javbus/fc2 sanity-check — facts extracted from `/root/clone/open-mdcng`

Reference-only reading. No code was copied. Every claim carries a `file:line` citation into
`/root/clone/open-mdcng`. Anything not determinable from the repo is marked **UNKNOWN**.

Vocabulary note: the old repo has a **16-field** vocabulary
(`scrape/field.go:26-43`, list at `scrape/field.go:45-62`):
`title, original_title, outline, cover, poster, extra_fanart, tags, user_rating, actors, studio,
series, release_date, runtime, trailer, director, publisher`.
The 14 names in the task map as: `title→title`, `plot→outline`, `fanart→cover`,
`extrafanarts→extra_fanart`, `tags→tags`, `releasedate→release_date`, `actors→actors`,
`poster→poster`, `director→director`, `runtime→runtime`, `rating→user_rating`, `series→series`,
`studio→studio`, `publisher→publisher`. (`original_title` and `trailer` are the two extra.)

---

## 1. dmm endpoints and protocol

### Endpoints

Resolved per call from the configured `[[sources]]` domain
(`scrape/dmm.go:284-288`, called from `scrape/dmm.go:243-250`):

```go
func dmmEndpoints(settings config.Source) (api, referer string) {
	host := strings.TrimPrefix(settings.Domain, "www.")
	return "https://api.video." + host + "/graphql", "https://video." + host + "/"
}
```

- GraphQL API: `https://api.video.<domain>/graphql` (both legs POST here).
- Referer: `https://video.<domain>/`.
- Shipped default domain is `dmm.co.jp` (`config/defaults.go:157`), so the effective addresses are
  `https://api.video.dmm.co.jp/graphql` and `https://video.dmm.co.jp/`.
- Both addresses are resolved once per call and cached on the session so a mid-call config save
  cannot split the two legs across hosts (`scrape/dmm.go:266-282`).

### Search operation — `legacySearchPPV`

Request body is `json.Marshal(map[string]string{"query": ...})`, i.e. `{"query":"..."}`
(`scrape/dmm.go:316-322`). The query text, with `%d` = `dmmSearchLeg` = 30
(`scrape/dmm.go:538`) and `%s` = a JSON-quoted search term (`scrape/dmm.go:59-66`):

```
query SearchPPV {
  legacySearchPPV(limit: 30, floor: AV, sort: SALES_RANK_SCORE, queryWord: "ssis00129") {
    result {
      contents { id }
      pageInfo { totalCount }
    }
  }
}
```

(Exact Go source at `scrape/dmm.go:317-325`.) Note `sort: SALES_RANK_SCORE` — sales rank, not
relevance — and the 30-wide window is deliberate (`scrape/dmm.go:532-538`).

### Content operation — `ppvContent`

`scrape/dmm.go:361-386`. Body is `{"query":"..."}` with the query text (both `%s` are the
JSON-quoted content id):

```
query ContentPageData {
  ppvContent(id: "ssis00129") {
    id
    floor
    title
    description
    deliveryStartDate
    makerReleasedAt
    duration
    packageImage { largeUrl mediumUrl }
    sampleImages { largeImageUrl }
    sample2DMovie { highestMovieUrl }
    sampleVRMovie { highestMovieUrl }
    actresses { name imageUrl }
    directors { name }
    series { name }
    maker { name }
    label { name }
    genres { name }
  }
  reviewSummary(contentId: "ssis00129") { average total }
}
```

`reviewSummary` is a **second top-level operation in the same POST** (one request, not two)
(`scrape/dmm.go:384`).

### Values are interpolated into the query text, not passed as GraphQL variables

Deliberate, documented at `scrape/dmm.go:38-49`: the endpoint is undocumented, so a variable's
declared type would be a guess that fails server-side with a schema error. Values are still
JSON-escaped by `jsonString` (`scrape/dmm.go:59-66`).

### Required headers

`scrape/dmm.go:427-448`:

```go
headers := map[string]string{
	"Referer":       c.referer,          // https://video.<domain>/
	"Content-Type":  "application/json",
	"Accept":        "application/json",
	"Cache-Control": "no-cache",
	"Fanza-Device":  "BROWSER",
}
```

- Configured `[[sources]].headers` are layered **over** these, so a user can correct one without a
  release (`scrape/dmm.go:444-447`).
- **`Origin` is never set by the source.** A repo-wide grep found no `Origin` header anywhere in
  the scrape/fetch code (only unrelated `OriginalTitle` matches). So: Origin is **not sent**, and
  whether the endpoint requires one is **UNKNOWN** from this repo.
- **User-Agent** is not set by the source either; it comes from the Fetcher's browser disguise
  (`fetch/disguise.go:203-215`, `applyDisguise` at `fetch/disguise.go:280-330`). Default profile
  `chrome_133` (`fetch/disguise.go:56`), UA of the form
  `Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36`
  (`fetch/disguise.go:158-161`). Header precedence, widest to narrowest: disguise → configured
  `[[sources]].headers` → the request's own headers (`fetch/disguise.go:295-317`).
- Header order is set explicitly through the fake `Header-Order:` key
  (`fetch/fetcher.go:49-54`, `fetch/disguise.go:325-327`); a test pins that Content-Type /
  Fanza-Device / Cache-Control land in the order table (`fetch/request_headers_test.go:25-73`).
- Confirmed by the doc: "dmm needs its `Content-Type`; the definition's `headers:` block is only
  real because ticket 34 fixed headers being dropped on the way out"
  (`docs/source-notes.md:94-96`).

### Response envelope

Search (`scrape/dmm.go:96-107`):

```
data.legacySearchPPV.result.contents[].id
data.legacySearchPPV.result.pageInfo.totalCount
```

Content (`scrape/dmm.go:109-169`):

```
data.ppvContent        -> object | null
data.reviewSummary     -> { average, total }
```

Errors: an `"errors"` key anywhere in the body is treated as an error
(`dmmGraphQLError`, `scrape/dmm.go:628-663`); the body is parsed for `errors[].message` and the
messages are joined. A region refusal (message containing `not-available-in-your-region` or
`not available in your region`) gets a dedicated error naming Japan and the per-source `proxy`
setting (`scrape/dmm.go:649-655`); anything else is surfaced as the site's own words
(`scrape/dmm.go:660-661`). A malformed `"errors"` document is itself an error
(`scrape/dmm.go:641-643`). An `"errors"` substring check is used rather than a JSON probe
(`scrape/dmm.go:629`).

`data.ppvContent == null` after a resolved id is treated as a normal miss, `Found: false`
(`scrape/dmm.go:404-409`). HTTP status is classified by the shared `pageOutcomes`
(`scrape/engine.go:548-559`): 2xx = ok, 404 = not-found, anything else = error. Note the doc says
a schema drift answered **422** (`docs/source-notes.md:91-93`), which under `pageOutcomes` is an
error, not a miss.

Fixtures: `scrape/testdata/dmm_search.json` and `scrape/testdata/dmm_content.json` — both
**constructed**, not captures (`scrape/testdata/fixtures.yaml:152-170`, reason `parser-shape`),
"a real capture would be better evidence for schema drift and is a known gap"
(`scrape/testdata/fixtures.yaml:147-151`).

---

## 2. dmm number → product id, and the zero-padding double spelling

### The id is not the number

The search result only carries `id` (`scrape/dmm.go:96-107`). The doc comment records that the
former `makerContentId` field (the display spelling the exact match used to read) was removed from
the schema (`scrape/dmm.go:293-300`). The product id is the label prefix plus the sequence, and the
sequence is spelled two ways: `ssis00001` for the first title and `ssis001NN` for later ones
(`scrape/dmm.go:543-549`).

### Which spellings are tried, in what order

`dmmSearchTerms` (`scrape/dmm.go:551-582`), "most specific first" — plain reduction first, then the
5-wide zero-padded spelling:

```go
func dmmSearchTerms(number string) []string {
	reduced := strings.ToLower(candidateKey(number))

	prefix, value, ok := dmmIdentity(number)
	if !ok {
		// A number whose tail is not digits has no padded spelling to try.
		return []string{reduced}
	}
	// The padded spelling is the value at the width the sites pad to, which is
	// the reduced form itself once the sequence is already that wide.
	count, err := strconv.Atoi(value)
	if err != nil {
		return []string{reduced}
	}
	if len(reduced)-len(prefix) >= dmmPaddedDigits {
		return []string{reduced}
	}
	padded := prefix + fmt.Sprintf("%0*d", dmmPaddedDigits, count)
	if padded == reduced {
		return []string{reduced}
	}
	return []string{reduced, padded}
}
```

- `dmmPaddedDigits = 5` (`scrape/dmm.go:541`).
- `candidateKey` (parser's canonicaliser, `scrape/source.go:224-241`) upper-cases, trims, and drops
  `-`, `_`, space, tab; then `strings.ToLower` is applied here.
- The loop is in `search` (`scrape/dmm.go:292-313`): it tries terms in order and **stops at the
  first term that errors or finds a hit**:
  ```go
  for _, term := range dmmSearchTerms(number) {
      id, res, found, err = c.searchTerm(ctx, number, term)
      if err != nil || found {
          return id, res, found, err
      }
  }
  return "", res, false, nil
  ```
- Measured behaviour pinned by tests: `SSIS-001 → ["ssis001", "ssis00001"]`,
  `IPX-535 → ["ipx535", "ipx00535"]`, `SSIS-00129 → ["ssis00129"]` (already 5 wide),
  `FC2-PPV-1234567 → ["fc2ppv1234567"]` (`scrape/dmm_test.go:10-27`).

### How the right hit is chosen

Never "the first candidate". Every candidate is compared by **identity** — family prefix plus the
sequence's numeric value with leading zeros stripped (`dmmIdentity`, `scrape/dmm.go:584-601`;
`dmmSameWork`, `scrape/dmm.go:665-673`):

```go
func dmmSameWork(id, number string) bool {
	idPrefix, idValue, idOK := dmmIdentity(id)
	numPrefix, numValue, numOK := dmmIdentity(number)
	if !idOK || !numOK {
		// Nothing numeric to compare — an exact spelling match is all there is.
		return strings.EqualFold(strings.TrimSpace(id), strings.TrimSpace(number))
	}
	return idPrefix == numPrefix && idValue == numValue
}
```

Candidate walk, `scrape/dmm.go:349-356`:

```go
for _, candidate := range payload.Data.LegacySearchPPV.Result.Contents {
	if candidate.ID != "" && dmmSameWork(candidate.ID, number) {
		return candidate.ID, Result{}, true, nil
	}
}
```

Rationale quote (`scrape/dmm.go:347-348`): "Exact match on the identity, never 'the first
candidate'. A search for SSIS-001 returns SSIS-0011 as a neighbour, and reading the wrong entry
would file someone else's title under this number."
Identity rationale (`scrape/dmm.go:586-593`): "`SSIS-001` and the id `ssis00001` are the same title,
and neither string is the other — the padding belongs to the site's id, the leading zero belongs to
the canonical number, and comparing the spellings is how a match is missed."

Test table: `{"ssis00129","SSIS-00129"}=true`, `{"ssis00001","SSIS-001"}=true`,
`{"ssis001","SSIS-0011"}=false`, `{"ssis00001","SSIS-002"}=false` (`scrape/dmm_test.go:30-48`).

### Request cost

- Zero candidates on a search → the content leg is **not** spent (`scrape/dmm.go:334-337`; test
  `scrape/native_test.go:220-246`).
- A 5-wide number asks once; a 3-wide number asks up to twice (test
  `scrape/native_test.go:248-287`, asserting `queryWord: "ssis001"` first then
  `queryWord: "ssis00001"`).
- dmm's applicability excludes FC2: one positive RE2 pattern
  `(?i)^(?:[^f].*|f(?:[^c].*|$)|fc(?:[^2].*|$))$` (`scrape/dmm.go:202-204`), so FC2/FC2-PPV/FC2PPV
  and the empty prefix all skip with zero requests (`scrape/native_test.go:485-514`).

---

## 3. dmm field mapping (ppvContent → the 14 names)

Mapping function `dmmMetadata` (`scrape/dmm.go:455-535`); response struct `dmmPPVContent`
(`scrape/dmm.go:117-165`); rating struct `dmmReviewSummary` (`scrape/dmm.go:167-169`).

| target field | source expression | notes |
|---|---|---|
| `title` | `ppvContent.title` | `scrape/dmm.go:457` |
| `plot` (`outline`) | `ppvContent.description` | `scrape/dmm.go:458` |
| `fanart` (`cover`) | first non-empty `sampleImages[].largeImageUrl`; fallback `packageImage.largeUrl` if there are no samples | `scrape/dmm.go:466-481` |
| `extrafanarts` (`extra_fanart`) | every non-empty `sampleImages[].largeImageUrl`, in order | `scrape/dmm.go:466-474` |
| `tags` | `genres[].name`, non-empty only | `scrape/dmm.go:483-487` |
| `releasedate` (`release_date`) | `deliveryStartDate`, falling back to `makerReleasedAt` | `scrape/dmm.go:504-506`, `dmmDate` at `scrape/dmm.go:603-619` |
| `actors` | `actresses[].name` + `actresses[].imageUrl` as the thumb | `scrape/dmm.go:488-493` |
| `poster` | `packageImage.largeUrl` | `scrape/dmm.go:461-465` |
| `director` | `directors[].name` (a list) | `scrape/dmm.go:494-498` |
| `runtime` | `duration` seconds / 60, integer division, only if > 0 | `scrape/dmm.go:509-512` |
| `rating` (`user_rating`) | `reviewSummary.average` × 2, only if > 0 | `scrape/dmm.go:520-526` |
| `series` | `series.name` | `scrape/dmm.go:500` |
| `studio` | `maker.name` | `scrape/dmm.go:499` |
| `publisher` | `label.name` | `scrape/dmm.go:501` |

Not in the 14 but present: `trailer` ← `sample2DMovie.highestMovieUrl`, falling back to
`sampleVRMovie.highestMovieUrl` (`scrape/dmm.go:514-518`). `ppvContent.floor` and `id` are
declared in the query but not mapped. `packageImage.mediumUrl` is declared but not used.
`dmmFields()` declares 15 fields including `trailer` (`scrape/dmm.go:70-86`).

### Rating scale and conversion

FANZA's `reviewSummary.average` is a **five-point** score; the field is **ten-point** (ADR-0016),
converted by `ratingFromFivePoint` = `score * 2` (`scrape/field.go:266-279`). Fixture `4.4` →
`8.8` (`scrape/testdata/dmm_content.json`, asserted at `scrape/native_test.go:194-198`). A
rating of exactly 0 is left unset (`scrape/dmm.go:522`, `> 0` guard). Doc:
`docs/source-notes.md:97-101`.

### Date format returned

The fixture returns RFC3339 UTC, e.g. `"deliveryStartDate": "2021-07-16T00:00:00Z"` and
`"makerReleasedAt": "2021-07-14T00:00:00Z"` (`scrape/testdata/dmm_content.json`). `dmmDate`
(`scrape/dmm.go:603-619`) tries `time.RFC3339` first and formats to `2006-01-02` in UTC; else if
the string is ≥10 chars and its first 10 parse as `2006-01-02`, it keeps that prefix; else empty.
Test asserts `release_date = "2021-07-16"` (delivery date wins over the maker date)
(`scrape/native_test.go:183`).

### Actor gender

**dmm does not supply actor gender.** `actresses` carries only `name` and `imageUrl`
(`scrape/dmm.go:143-146`); the query asks for nothing else (`scrape/dmm.go:378`). Contrast javdb,
which has `gender` 0/1 (`scrape/javdb.go:296-304`) and filters on it. dmm's mapping keeps every
actress with a non-empty name (`scrape/dmm.go:488-493`).

### Runtime unit

`duration` is **seconds** (`7080` in the fixture → 118 minutes;
`scrape/dmm.go:126-127`, `scrape/dmm.go:509-512`, test `scrape/native_test.go:187-190`).

---

## 4. dmm images

### Hosts and URL patterns (from `scrape/testdata/dmm_content.json`)

- Poster / package image: `https://pics.dmm.co.jp/digital/video/<id>/<id>pl.jpg` (`largeUrl`)
  and `...<id>ps.jpg` (`mediumUrl`).
- Sample / extrafanart / cover: `https://pics.dmm.co.jp/digital/video/<id>/<id>jp-1.jpg`,
  `...jp-2.jpg`, …
- Actor portrait: `https://pics.dmm.co.jp/mono/actjpgs/<slug>.jpg` (fixture uses
  `https://pics.dmm.co.jp/mono/actjpgs/one.jpg`).
- Trailer: `https://cc3001.dmm.co.jp/litevideo/freepv/s/ssi/<id>/<id>mhb.mp4`.

Independent corroboration that `pics.dmm.co.jp` is the real image CDN: javbus detail pages carry
sample images as absolute `https://pics.dmm.co.jp/digital/video/.../...jp-N.jpg` URLs and actor
portraits as `https://pics.dmm.co.jp/mono/actjpgs/...`
(`docs/source-notes.md:32-37`; real captures under
`/root/clone/open-mdcng/acceptance/fixtures/html/*.html`, e.g. `SSIS-001.html:319`). A ticket
transcript also records dmm poster/actor/trailer hosts `awsimgsrc.dmm.co.jp/pics_dig/...`,
`pics.dmm.co.jp/mono/actjpgs/...`, `cc3001.dmm.co.jp/pv/...`
(`.scratch/open-mdcng/issues/42-nextjs-json-data-unreadable.md:95-97`) — i.e. at least a third
image host exists.

### Referer / cookie / UA requirement

The source puts **no** headers on the image URLs. dmm implements neither `HeaderBoundSource` nor
`EncodedArtifactSource` — only `RequiresProxy`/`ProxyHint` (`scrape/dmm.go:229-241`); the registry
looks the optional interfaces up by type (`scrape/registry.go:372-435`). The artifact path only
sends a Referer when the source declares headers (`scrape/source.go:84-103`). So as far as this
repo records: **no Referer, no cookie, no special UA** is known to be needed for dmm images.
Whether the CDN actually demands one is **UNKNOWN**.

### Does the image fetch need the Japan egress?

**UNKNOWN.** Facts available:
- The blocked host named in this task's context is dmm's site/API. The API is on
  `api.video.dmm.co.jp`; images are on `pics.dmm.co.jp` and `cc3001.dmm.co.jp` (and possibly
  `awsimgsrc.dmm.co.jp`) — different hosts, no recorded reachability test.
- The old repo records the Japan-exit requirement only as a property of the source's own requests
  (`scrape/dmm.go:51-56`, `docs/source-notes.md:85-90`), never as a property of the image hosts.
- javbus pages embed `pics.dmm.co.jp` image URLs and the old pipeline treats them as ordinary
  address fields resolved against the page (`scrape/engine.go:237-268`,
  `docs/source-notes.md:32-37`); there is no note of a Japan exit being needed to download them, but
  there is also no note of a successful non-Japan download.
- The acceptance environment mirrors javbus images at `/pics/...` on a local fixture server
  (`acceptance/fixtureserver/main.go:71-90`), so it proves nothing about `pics.dmm.co.jp`.
- dmm's own live verification (`docs/source-notes.md:203`) used a machine whose exit reached the
  GraphQL endpoint; it says nothing about the images.

Treat "dmm images need Japan" as an open question to measure, not as a fact from this repo.

---

## 5. javdb

### Endpoints

- Search: `GET https://{domain}/api/v2/search?<params>` (`javdbSearchURL`, `scrape/javdb.go:185-199`).
  Query parameters exactly (`scrape/javdb.go:186-195`):
  `q={number}`, `from_recent=false`, `type=movie`, `movie_type=all`, `movie_sort_by=relevance`,
  `movie_filter_by=all`, `page=1`, `limit=24` (`javdbSearchLimit = 24`, `scrape/javdb.go:176`).
  Built with `url.Values.Encode()` so a number containing a space is escaped
  (`scrape/javdb.go:180-184`).
- Detail: `GET https://{domain}/api/v4/movies/{url.PathEscape(id)}?from_rankings=false`
  (`scrape/javdb.go:393-397`).
- Doc: `docs/source-notes.md:105-110`.

### Where the domain comes from

From the `[[sources]]` entry, read per call via `nativeSettings(q, s.id)`
(`scrape/javdb.go:370-376`, `scrape/native.go:40-54`). Shipped default `jdforrepam.com`
(`config/defaults.go:155`); the acceptance config uses the same (`acceptance/sources-real.toml:40-42`).
A missing domain is an error naming the setting (`scrape/native.go:47-50`; test
`scrape/javdb_test.go:772-782`). The domain is the API host only; `share_info`'s mirror
(`javdb580.com`) is never used (`docs/source-notes.md:108-110`).

### jdsignature recipe

`scrape/javdb.go:146-174`:

```go
const (
	javdbSignSalt = "lpw6vgqzsp"
	javdbSignKey  = "71cf27bb3c0bcdf207b64abecddc970098c7421ee7203b9cdae54478478a199e7d5a6e1a57691123c1a931c057842fb73ba3b3c83bcd69c17ccf174081e3d8aa"
)

func javdbSignature(unix int64) string {
	now := strconv.FormatInt(unix, 10)
	sum := md5.Sum([]byte(now + javdbSignKey))
	return now + "." + javdbSignSalt + "." + hex.EncodeToString(sum[:])
}

func javdbSignedHeaders() map[string]string {
	return map[string]string{"jdsignature": javdbSignature(time.Now().Unix())}
}
```

So: header name **`jdsignature`** (lowercase), value `{unix}.{salt}.{md5(unix + key)}`, computed per
request, never cached (`scrape/javdb.go:169-174`; test `scrape/javdb_test.go:151-198`). Measured
freshness: 300 s old accepted, 600 s old answers `ExpiredSignature`
(`scrape/javdb.go:147-150`, `docs/source-notes.md:111-118`). Both constants are the application's
own shared constants and are deliberately not configurable (`scrape/javdb.go:139-145`).

### FC2 number-rewrite regex

`scrape/javdb.go:199-222`:

```go
var javdbFC2Number = regexp.MustCompile(`(?i)^FC2[-_ ]?(?:PPV[-_ ]?)?(\d+)$`)

func javdbSearchNumber(number string) string {
	number = strings.TrimSpace(number)
	if parts := javdbFC2Number.FindStringSubmatch(number); parts != nil {
		return "FC2-" + parts[1]
	}
	return number
}
```

Anchored, digits required — so `FC2-PPV` alone is not reduced to `FC2` + "2"
(`scrape/javdb.go:199-203`). Evidence: `FC2-PPV-3336019` returns **zero** candidates
(`scrape/testdata/javdb_search_fc2_ppv.json`), `FC2-3336019` returns the exact entry
(`scrape/testdata/javdb_search_fc2.json`); `docs/source-notes.md:167-172`. The rewritten spelling is
used for **both** legs *and* the exact-match filter, because candidates carry the site's spelling
(`scrape/javdb.go:376-380`). Every other family is passed through untouched.

### How exact match is enforced

`javdbExactMatch` (`scrape/javdb.go:444-459`) compares `candidateKey(candidate.Number)` against
`candidateKey(number)`, walking **every** candidate and requiring `candidate.ID != ""` and
`candidate.Number != ""`; it never reads position. Measured near-misses: `ZZZZ-9999` answered with
`ZZZM-999` (`scrape/testdata/javdb_search_no_hits.json`), `SSIS-0011` answered with `SSIS-011`
first (`docs/source-notes.md:160-166`); two searches for one number returned a different *second*
candidate, so position is not evidence (`scrape/javdb.go:447-453`). `candidateKey` drops only
cosmetic separators (`-`, `_`, space, tab) after upper-casing, so `SSIS-0011` ≠ `SSIS-011`
(`scrape/source.go:224-241`). No candidates at all → `Found: false` costing one request
(`scrape/javdb.go:388-391`).

### Success/error envelope semantics

Envelope (`scrape/javdb.go:237-251`): `{ "success": int, "action": string, "message": string,
"data": T|null }`; `Success` zero is the safe default for a document that omitted it.

`javdbRead` (`scrape/javdb.go:343-368`) is the one place the three outcomes are separated:

1. **HTTP 404 → not-found**, `Found: false`, whatever the body says. The capture
   `scrape/testdata/javdb_detail_notfound.json` is
   `{"success":0,"action":"ResourceNotFound","message":"資源未找到","data":null}` served with 404
   (`scrape/testdata/fixtures.yaml:235-241`), and it is read as a miss, not an error
   (test `scrape/javdb_test.go:476-506`).
2. **Any other non-2xx → error**, and the body's own diagnosis is preferred over the status code:
   if the body parses as the envelope and `action != ""`, the error reads
   `source <id>: the API refused the request: HTTP <status>, action "<action>", message "<message>"`
   (`scrape/javdb.go:346-352`). This is what carries `ExpiredSignature` / `InvalidSignature` through
   on a 400 (`scrape/javdb.go:326-340`; test `scrape/javdb_test.go:508-528` uses
   `{"success":0,"action":"ExpiredSignature","message":"簽名已過期","data":null}` with HTTP 400).
   A non-JSON body on a non-2xx falls back to the plain status error.
3. **2xx with `success != 1` → error** (`scrape/javdb.go:362-365`), never an empty result.
   `ParameterInvalid` arrives this way: missing signature → `200` with `success: 0` and
   `ParameterInvalid` (`docs/source-notes.md:114-116`). A non-JSON 2xx body is an error rather than
   an empty answer (`scrape/javdb.go:354-361`, test `scrape/javdb_test.go:531-549`).
4. **2xx with `success == 1`** → `resp.Data` returned (`scrape/javdb.go:366`).

Also: no user agent at all → `403` with body `error code: 1010`
(`docs/source-notes.md:119-126`); no exit, no cookie needed (same lines); the artwork needs no
Referer (`docs/source-notes.md:127-130`).

### Image XOR decode

`DecodeArtifact` implements `EncodedArtifactSource` (`scrape/javdb.go:544-565`). Format is
`key || (jpeg XOR key)`: one key byte, then each subsequent byte XORed with it, fresh key per image
(measured keys `0x3c`, `0xbb`, `0xe6`) (`scrape/javdb.go:526-542`, `docs/source-notes.md:131-141`).

```go
func (s *javdbSource) DecodeArtifact(body []byte) ([]byte, error) {
	if len(body) == 0 { return nil, ... }
	if bytes.HasPrefix(body, javdbJPEGMagic) { return body, nil }   // 0xff 0xd8 0xff
	key := body[0]
	decoded := make([]byte, len(body)-1)
	for i := 1; i < len(body); i++ { decoded[i-1] = body[i] ^ key }
	if !bytes.HasPrefix(decoded, javdbJPEGMagic) { return nil, ... }
	return decoded, nil
}
```

`javdbJPEGMagic = []byte{0xff, 0xd8, 0xff}` (`scrape/javdb.go:525`). A plain JPEG passes through; a
body that decodes to something else is an error (`scrape/javdb.go:536-542`; tests
`scrape/javdb_test.go:595-651`).

**Which response fields are images:**

| field | JSON path | disposition |
|---|---|---|
| `cover` (fanart) | `movie.cover_url` | mapped to `md.Cover` (`scrape/javdb.go:574`); **decoded here**, address kept as the app CDN's |
| `poster` | `movie.thumb_url` | mapped to `md.Poster` (`scrape/javdb.go:575`); decoded |
| `extra_fanart` | `movie.preview_images[].large_url` | appended to `md.ExtraFanart` (`scrape/javdb.go:610-614`); decoded |
| actor thumb | `movie.actors[].avatar_url` | **re-addressed, not decoded** — turned into the site CDN URL (`scrape/javdb.go:601-605`, `javdbSiteImageURL`) because it goes into the NFO as a URL |

Covers are the one kind where the two CDNs disagree (site copy is a re-encode, 5–15 % smaller), so
the placed cover keeps the **application's** address and is decoded; `thumbs`, `samples` and
`avatars` were byte-identical between the two CDNs
(`docs/source-notes.md:143-152`).

**small_covers → thumbs mapping:** `javdbSiteImageURL` (`scrape/javdb.go:502-523`) rewrites
`https://{app-cdn}/{token}/{kind}/…` to `https://c0.jdbstatic.com/{kind}/…`, and when
`kind == "small_covers"` it substitutes `"thumbs"` (`scrape/javdb.go:515-518`). The site CDN is the
hard-coded constant `javdbSiteCDN = "c0.jdbstatic.com"` (`scrape/javdb.go:467`), and the recognised
asset kinds are `covers`, `small_covers`, `thumbs`, `samples`, `avatars`
(`scrape/javdb.go:472-479`). Test cases including the `small_covers`→`thumbs` rewrite:
`scrape/javdb_test.go:653-698`. The mapping keys on the path shape, not the host, because the app
CDN host moves (`tp-iu.cmastd.com` → `tp.spfcas.com`, same token) (`scrape/javdb.go:485-500`).

---

## 6. javbus

Definition: `scrape/sources_builtin/javbus.yaml` (declarative, direct — one request per number,
no search leg).

### Cookie requirement

The age gate is a **server redirect, not a JS challenge**: without a cookie the site answers
`302` to `/doc/driver-verify`, so FlareSolverr cannot help. The cookie is the whole answer and lives
in the `[[sources]]` entry (`docs/source-notes.md:19-26`):

```toml
cookies = { age = "verified", existmag = "all", dv = "1" }
```

Same values in `acceptance/sources-real.toml:48`. Cookies are a per-source config field
(`config/schema.go:388-391`), applied by the disguise layer (`fetch/disguise.go:312-317`).

### Domain

Shipped default `javbus.com` (`config/defaults.go:156`); `javbus.com` answers `301` to
`www.javbus.com` and the redirect is harmless here (unlike avbase's)
(`docs/source-notes.md:28-31`). Acceptance uses `www.javbus.com` (`acceptance/sources-real.toml:47`).

### HTTP 404 semantics

Via the shared `pageOutcomes` (`scrape/engine.go:548-559`): **404 = "no such entry"**, recorded as
`Found: false`, not an error; any other non-2xx is a failure. Measured: `DEVR-037` and `DEVR-37`
answer `200` with the release's title; `DEVR-0037`, `DEVR-00037`, `GASO-0061`, `ECR-0074` answer
`404` (`docs/source-notes.md:207-213`). Because the Fetcher does **not** penalise 404
(`fetch/challenge.go:93-101`), a miss never cools the domain down.

### Image Referer requirement

**Yes** — javbus answers `403` to an image without a Referer. The definition declares
`headers: Referer: 'https://{domain}/'` (`scrape/sources_builtin/javbus.yaml:30-31`), and that same
table is re-served to the artifact downloader through `HeaderBoundSource`
(`scrape/source.go:84-103`; `DeclarativeSource.RequestHeaders`, `scrape/engine.go:322-325`).
Registry lookup: `scrape/registry.go:394-420`. Doc/evidence: `scrape/source.go:94-95`
("javbus answers 403 to an image without a Referer"); the acceptance fixture server deliberately
reproduces the rule (`acceptance/fixtureserver/main.go:71-86`) and asserts it
(`acceptance/acceptance_test.go:463-480`). A real run that got this wrong booked the 403 against
the source and cooled the domain for five minutes (`fetch/fetcher.go:157-168`, `Request.Asset`).
Covers and actor portraits are **relative** (`/pics/cover/…`, `/pics/actress/…`) and are resolved
against the detail page; sample images are absolute on dmm's CDN
(`docs/source-notes.md:32-37`).

### Fields the detail page yields

Definition declares 10 fields (`scrape/sources_builtin/javbus.yaml:13-23`) and has 10 detail rules
(`:37-91`): `title, cover, tags, actors, studio, series, release_date, runtime, director,
publisher`.

Mapping onto the 14: **title, fanart(cover), tags, actors, studio, release_date, runtime, director,
publisher** are yielded; `series` has a rule but the real SSIS-001 capture has no 系列 line
(`grep` over `scrape/testdata/javbus_detail.html` finds only 發行日期 / 長度 / 導演 / 製作商 /
發行商), so in practice **9 of the 14**. Missing from a javbus page: **plot, extrafanarts, poster,
rating** (plus, in the 16-field vocabulary, `original_title` and `trailer`). The acceptance run
pins exactly this: missing =
`["extra_fanart", "original_title", "outline", "poster", "series", "trailer", "user_rating"]`
(`acceptance/acceptance_test.go:351-354`), and it notes "a javbus page carries nine of them, so
**every** javbus-scraped task is `partial`" (`acceptance/acceptance_test.go:301-302, 336-341`).
There is deliberately no outline rule (`acceptance/acceptance_test.go:303-306`).

Selectors worth citing: title `h3` (`scrape/sources_builtin/javbus.yaml:39-41`), cover `a.bigImage img[src]`
(`:43-45`), tags `span.genre label a` (`:47-49`), actors `div#avatar-waterfall a.avatar-box` with
name from `img[title]` and thumb from `img[src]` (`:58-69`), studio regex
`製作商:\s*(\S.*)$`, series `系列:\s*(\S.*)$`, release_date
`發行日期:\s*(\d{4}-\d{2}-\d{2})`, runtime `長度:\s*(\d+)分鐘`, director `導演:\s*(\S.*)$`,
publisher `發行商:\s*(\S.*)$` (`:72-91`). Applicability `'^[A-Z]+$'` (`:27-28`) — note the empty
prefix is rejected by this pattern (test `scrape/native_test.go:485-514`).

---

## 7. fc2

Source `scrape/fc2.go`; default domain `adult.contents.fc2.com` (`config/defaults.go:159`).

### Article-id derivation

`fc2ArticleID` (`scrape/fc2.go:98-112`) takes the **trailing** digit run, not every digit (so the
`2` of `FC2` cannot become a leading digit):

```go
func fc2ArticleID(number string) string {
	s := strings.TrimSpace(number)
	end := len(s)
	for end > 0 && !fc2Digit(s[end-1]) { end-- }
	start := end
	for start > 0 && fc2Digit(s[start-1]) { start-- }
	return s[start:end]
}
```

`FC2-PPV-1234567`, `FC2PPV-1234567` and `FC2-1234567` all → `1234567` (test
`scrape/native_test.go:452-476`). Address: `https://{domain}/article/{id}/`
(`scrape/fc2.go:125`); one GET, no search leg. Empty id is an error naming the number, not a request
for `/article//` (`scrape/fc2.go:120-123`). Applicability `(?i)^FC2` (`scrape/fc2.go:70-72`).

### Positive hit marker and positive miss marker

**Miss marker** (the positive one): `div.items_notfound_wp`
(`scrape/fc2Selectors.notFound`, `scrape/fc2.go:190`), checked right after the status
(`scrape/fc2.go:146-148`):

```go
if firstMatch(root, fc2Selectors.notFound) != nil {
	return Result{Found: false, URL: page.URL, Status: page.Status}, nil
}
```

Rationale (`scrape/fc2.go:142-148`): fc2 answers its own "not found" page with **HTTP 200**, so
status says nothing; keying on "the article container is absent" would read a changed page as "this
number does not exist". Doc: `docs/source-notes.md:184-188`. Fixture: `scrape/testdata/fc2_miss.html`
(constructed, `defect-reproduction`, `scrape/testdata/fixtures.yaml:137-147`); test
`scrape/native_test.go:424-450` (asserts `Found: false`, zero fields, status 200, exactly 1 request).

**Hit marker** is the positive container `div.items_article_headerInfo` (`scrape/fc2.go:186`): if it
is present and the miss container is absent, the page is read (`fc2Extract`, `scrape/fc2.go:200-262`).

### Fields the page yields

Capability declaration (`scrape/fc2.go:44-55`): `title, outline, cover, extra_fanart, tags, studio,
release_date, runtime` — **8 fields, and deliberately no actors** ("fc2 is an amateur marketplace
with no cast list, and a field that is declared but never returned is a capability that lies",
`scrape/fc2.go:39-43`).

Mapping onto the 14: **title, plot(outline), fanart(cover), extrafanarts, tags, releasedate,
runtime, studio** = 8. Missing: **actors, poster, director, rating, series, publisher**.

Selectors (`scrape/fc2.go:169-193`): header `div.items_article_headerInfo`, title `h3` (with hidden
ad spans stripped — `zoom:0.01`, `display:none`, `overflow:hidden`, `scrape/fc2.go:358-412`),
tags `section.items_article_TagArea a.tag`, seller/studio `li.items_article_writer a`,
date rows `div.items_article_softDevice p` (first row that parses as a date; printed as
`販売日 : 2026/09/25`), cover `div.items_article_MainitemThumb span img[src]` (protocol-relative,
resolved against the page), runtime `div.items_article_MainitemThumb p.items_article_info`
(`30:16` under an hour, `01:03:52` over it), gallery `section.items_article_SampleImages li a[href]`
(full-size link), outline `meta[name="description"]` with the leading number token stripped
(`fc2StripNumberPrefix`, `scrape/fc2.go:497-506`). Real capture:
`scrape/testdata/fc2_detail.html` (capture, `scrape/testdata/fixtures.yaml:123-133`); field assertions
`scrape/native_test.go:351-400`.

---

## 8. The old scheduler's fallback and tri-state handling

Implementation `scrape/scheduler.go`; plan `scrape/schedule.go`.

### The three outcomes

`Outcome` has exactly three values (`scrape/scheduler.go:34-52`):

- `OutcomeMissing` = "the source was asked and did not supply the field … a fact about the site
  rather than a failure of it, so it does not count against the source's health."
- `OutcomeFailed` = "the request did not complete — a transport error after the Fetcher's retries,
  or a refusal because the domain is cooling down … This is the source failing."
- `OutcomeHit` = "the source supplied at least one field that was still unsatisfied."

Classified in `absorb` (`scrape/scheduler.go:360-397`):

```go
outcome := OutcomeMissing
switch {
case a.err != nil && !selfInflicted:
	outcome = OutcomeFailed
case len(supplied) > 0:
	outcome = OutcomeHit
}
```

`selfInflicted` = `ctx.Err() != nil` (`scrape/scheduler.go:369-372`): a task-budget cancellation is
neither a failure nor a miss — "Recording the cancellation as a failure would blame a site for the
task's own deadline" (`scrape/scheduler.go:366-368`).

So the three-way distinction is: **error from `Fetch`** → failed; **`Found: false` / no satisfying
value** → missing; **≥1 newly satisfied wanted field** → hit. "Found but missing fields" is not a
separate outcome — a source that answered with a partial set is a `Hit` for the fields it supplied,
and the rest stay `Missing` (`scrape/scheduler.go:397-406`).

### What it recorded per source

`Summary` (`scrape/scheduler.go:94-140`):

- `Metadata` — the merged result.
- `Sources map[Field]string` — per-field provenance (which source supplied each field).
- `AllSites map[string]Result` — **every raw answer keyed by source id**, including sources that
  answered nothing, with `Found`, `Status`, `URL` and the satisfied fields
  (`encodeAllSites`, `scrape/pool.go:762-790`).
- `Missing []Field` — wanted fields no source supplied.
- `Unreachable []string` — sources whose requests did not complete, kept separate on purpose:
  "'the site refused me' and 'the site does not have this field' are different answers"
  (`scrape/scheduler.go:119-125`).
- `Events []Event` — every answer in order, each carrying `Source`, `Outcome`, `Fields`, `Missing`,
  `URL`, `Status`, `Err` (`scrape/scheduler.go:63-92`).

### Fallback behaviour

The cursor engine (`scrape/scheduler.go:14-25`): "a source is asked ⟺ it is the next candidate for
some field that is still unsatisfied, and it has not already been asked in this task."
- A source that answers without a field does **not** immediately advance to the next source; the
  field waits for any source already in flight that might answer it, then moves on
  (`absorb` advances only the cursors pointing at the answered source,
  `scrape/scheduler.go:374-385`).
- One request per source per task (`asked` map, `scrape/scheduler.go:214-215, 316-347`).
- Per-field `field_priority` lists *replace* the global order and are not padded with it
  (`scrape/schedule.go:239-249`, `config/schema.go:163-166`).
- Harvest scans from the head of a field's schedule up to the cursor, so the earliest
  highest-priority answered source wins; a race between two concurrent answers cannot decide
  precedence (`scrape/scheduler.go:422-449`).

### Did it retry?

- **Within a task: no.** One `Fetch` per source per task, and the scheduler never re-asks a source
  (`scrape/scheduler.go:214-215`, `launch` at `scrape/scheduler.go:316-347`).
- **At the transport level: yes.** `attempts = RetryTimes + 1` (default `RetryTimes = 2` → 3
  attempts) with no backoff sleep — the per-domain token bucket is the spacing
  (`fetch/fetcher.go:325-353`, `config/defaults.go:57`).
- **Across tasks: yes, bounded.** `scrape.max_attempts` (default 5) caps task retries
  (`config/defaults.go:64`, `config/schema.go:153-157`); used by the start-up reset
  (`cmd/mdcng/app.go:363`) and the retry endpoint (`api/taskretry.go:188-193`).
- A retry builds a **new** `Scheduler` ("It is single-use … A retry builds a new one",
  `scrape/scheduler.go:163-166`).
- Task budget expiry: the scrape is frozen and the task still proceeds to placement as a **partial
  success**, not a failure (`scrape/pool.go:441-470`; `timeoutReason`,
  `scrape/pool.go:569-600`). A shutdown is different: nothing is decided and the task is rewound
  (`scrape/pool.go:443-449`).

---

## 9. Per-source concurrency / timeout / retry worth knowing

### Three separable concurrency layers

`scrape/pool.go:21-33`: (1) within a task, the scheduler's cursor heads — bounded by the number of
sources, one goroutine per launched source (`scrape/scheduler.go:316-347`); (2) across tasks, the
pool — `scrape.concurrency`, default **3** (`config/defaults.go:62`); (3) per source across all of
it, the Fetcher's pacing and cooldown. "They do not share a limiter and they must not"
(`scrape/pool.go:29-31`). Pool size floor is 1 (`scrape/pool.go:625-635`).

### Timeouts

- Per request (one attempt, redirects + body read included): `[fetch].timeout_seconds`, default
  **30 s** (`config/defaults.go:55`, `fetch/fetcher.go:268-273`, `sendOnce` at
  `fetch/fetcher.go:483-490`). A **fresh deadline per attempt**, so a slow first try cannot consume
  the retries' budget (`fetch/fetcher.go:338-345`).
- Per task scrape: `[scrape].task_timeout_seconds`, default **600 s**
  (`config/defaults.go:63`, `scrape/pool.go:361-366`). Deliberately does **not** bound placement.
- Bookkeeping writes: 10 s, on a context detached from the task's budget
  (`writeTimeout`, `scrape/pool.go:538-552`).

### Rate / cooldown / retry

- Token bucket keyed by **registrable domain** (eTLD+1), burst **1**
  (`fetch/limiter.go:95-105, 130-180`); default `rate_per_second = 1`
  (`config/defaults.go:57`). A rate of 0 disables pacing (`fetch/limiter.go:135-139`). A rate change
  adjusts the existing bucket in place so an in-flight wait is not cancelled
  (`fetch/limiter.go:141-180`).
- Cooldown: a **refusal, not a wait** (`fetch/limiter.go:21-29`). Triggered by status **403, 429,
  503** or a challenge body (`fetch/challenge.go:93-101`, `fetch/fetcher.go:388-405`); default
  `cooldown_seconds = 300` (`config/defaults.go:58`). 404 is deliberately not penalised
  (`fetch/challenge.go:94-96`). Overlapping penalties keep the later end (`fetch/limiter.go:183-196`).
  The cooling check runs **before** the rate wait so a cooling domain costs nothing
  (`fetch/fetcher.go:295-301`).
- Retries: `retry_times` default **2** → 3 attempts, re-paced each time, no backoff sleep
  (`config/defaults.go:56`, `fetch/fetcher.go:325-353`).
- Browser fallback (FlareSolverr), attempted **once**, only on a refusal or after every retry
  failed, and only for GET (`fetch/fetcher.go:434-481`); disabled when the address is empty.
- Challenge markers: 8 strings including `"just a moment..."`, `"cf-chl"`, `"cf_chl_opt"`,
  `"chk_jschl"`, `"ddos protection by"` (`fetch/challenge.go:44-63`).
  `"cdn-cgi/challenge-platform"` is deliberately **excluded** — a healthy Cloudflare page carries it
  and matching it cooled healthy sites for 300 s (`fetch/challenge.go:65-80`).
- Artifact (`Request.Asset`) requests: no health record, no cooldown start, no fallback, judged by
  status alone — but still subject to a cooldown a scrape started
  (`fetch/fetcher.go:131-146, 356-368`).
- Health is in-memory only, one record per source, not persisted (`fetch/health.go:20-26, 67-100`).
- Per-source overrides for `proxy`, `tls_profile`, `timeout_seconds`, `retry_times`,
  `rate_per_second`, `cooldown_seconds`, `flaresolverr_url`, `headers`, `cookies`, `api_key`,
  `ignore_fields` all exist; 0 / "" means inherit (`config/schema.go:370-426`;
  `inherit` at `fetch/fetcher.go:527-539`).
- dmm is the only source that declares an exit requirement: `RequiresProxy() = true` and the hint
  "dmm serves Japan only: set `proxy` on the dmm entry in [[sources]] to a Japanese exit, or disable
  the source" (`scrape/dmm.go:229-241`). Proxy resolution: per-source override, else global
  (`fetch/fetcher.go:229-237`).

---

## Open items / UNKNOWN

1. **dmm `Origin` header** — not sent by the old source; whether the endpoint requires one is
   unknown from this repo.
2. **dmm image egress** — `pics.dmm.co.jp` / `cc3001.dmm.co.jp` (and possibly
   `awsimgsrc.dmm.co.jp`) reachability without a Japan exit is unmeasured. The blocked host named in
   the task context is the API/site (`api.video.dmm.co.jp`); the image hosts are different hosts and
   are also embedded in javbus pages.
3. **dmm real capture** — `dmm_search.json` / `dmm_content.json` are constructed
   (`parser-shape`), not captures; the repo calls a real capture "a known gap"
   (`scrape/testdata/fixtures.yaml:147-151`).
4. **dmm actor gender** — not supplied (only javdb has `gender`).
5. **javdb `preview_video_url`** — a signed, expiring address (`?sign=…&t=…`); deliberately not
   exposed as `trailer` (`scrape/javdb.go:87-91`, `docs/source-notes.md:180-183`).
6. **dmm applicability** — dmm serves everything except FC2 (and the empty prefix); javbus is
   `^[A-Z]+$` only; fc2 is `(?i)^FC2`; javdb has no patterns (serves every prefix,
   `scrape/javdb.go:136`).
