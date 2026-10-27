# Metadata field model — FACTS ONLY

Gathered for the design ticket on the metadata field model of the mdc-lite rewrite.
No recommendations, no decisions.

## 0. What was read, and what "old repo" means here

| Label | Repo / revision | What it is |
|---|---|---|
| **NEW** | `/root/clone/mdc-lite` @ `main` | Near-empty Go repo; only `README.md` (the field draft being designed) |
| **OLD** | `/root/clone/open-mdcng` @ `b1b9b31` (branch `rewrite`), clean tree | The ~65k-line Go project being rewritten; 19 ADRs in `docs/adr/`, design docs in `.scratch/open-mdcng/` |
| **ORIG** | `8369f4d` inside `/root/clone/open-mdcng`'s history | The reverse-engineering record of the *original* tool (Rust/Actix backend + Next.js frontend) that OLD itself replaced. Files under `report/`, `recovered/`, `evidence/`, `notes/`, `adr/`, `scope.md`, `timeline.md`, `workitems.md` |

Citations are `path:line` for OLD/NEW, and `8369f4d:path:line` for ORIG.
Anything I could not verify is marked **UNVERIFIED**.

Counting checks: `find . -name '*.go' | wc -l` = 222 Go files; `ls docs/adr` = 19 ADRs
(`0001`…`0019`). The "72 tickets" are the `.scratch/open-mdcng/issues/*.md` set plus the
`(ticket N)` references in code comments.

---

## 1. The scraped-metadata type(s)

OLD has **three** shapes for the same field set, plus a descriptor table. There is no
`MovieMeta` / `FieldValue` / `ScrapedItem` type.

### 1.1 `scrape.Metadata` — the per-source / merged wire shape

`scrape/field.go:133-150`. Every field is a pointer or slice, so "absent" and "empty" are
distinguishable at the struct level. json tags are present; there are no yaml tags.

| # | Go field | Type | json tag | line |
|---|---|---|---|---|
| 1 | `Title` | `*string` | `title,omitempty` | `scrape/field.go:134` |
| 2 | `OriginalTitle` | `*string` | `original_title,omitempty` | `:135` |
| 3 | `Outline` | `*string` | `outline,omitempty` | `:136` |
| 4 | `Cover` | `*string` | `cover,omitempty` | `:137` |
| 5 | `Poster` | `*string` | `poster,omitempty` | `:138` |
| 6 | `ExtraFanart` | `[]string` | `extra_fanart,omitempty` | `:139` |
| 7 | `Tags` | `[]string` | `tags,omitempty` | `:140` |
| 8 | `UserRating` | `*float64` | `user_rating,omitempty` | `:141` |
| 9 | `Actors` | `[]Actor` | `actors,omitempty` | `:142` |
| 10 | `Studio` | `*string` | `studio,omitempty` | `:143` |
| 11 | `Series` | `*string` | `series,omitempty` | `:144` |
| 12 | `ReleaseDate` | `*string` | `release_date,omitempty` | `:145` |
| 13 | `Runtime` | `*int` | `runtime,omitempty` | `:146` |
| 14 | `Trailer` | `*string` | `trailer,omitempty` | `:147` |
| 15 | `Director` | `[]string` | `director,omitempty` | `:148` |
| 16 | `Publisher` | `*string` | `publisher,omitempty` | `:149` |

`Actor` is `{Name string \`json:"name"\`; Thumb string \`json:"thumb,omitempty"\`}` —
`scrape/field.go:113-116`. The plugins package has its own twin (`plugins/plugin.go:66-69`).

The field *vocabulary* is a separate `string` type with 16 constants
(`scrape/field.go:30-48`) and a canonical order in `Fields()` (`scrape/field.go:53-70`).
`ParseField` is exact-match for machine input (`scrape/field.go:82-91`); `ParseFieldName` trims and
lowercases for hand-edited `config.toml` (`:102-108`).

### 1.2 `plugins.Metadata` — the canonical in-memory shape after decode

`plugins/plugin.go:75-98`. It is the same 16 fields, but as **values not pointers**, plus
one derived field and one attachment:

| Go field | Type | json tag | line |
|---|---|---|---|
| `Streams` | `*Streams` | `streams,omitempty` | `plugins/plugin.go:80` |
| `Title` | `string` | `title` | `:82` |
| `OriginalTitle` | `string` | `original_title` | `:83` |
| `TranslatedTitle` | `string` | `translated_title` | `:84` |
| `Outline` | `string` | `outline` | `:85` |
| `Cover` | `string` | `cover` | `:86` |
| `Poster` | `string` | `poster` | `:87` |
| `ExtraFanart` | `[]string` | `extra_fanart` | `:88` |
| `Tags` | `[]string` | `tags` | `:89` |
| `UserRating` | `float64` | `user_rating` | `:90` |
| `Actors` | `[]Actor` | `actors` | `:91` |
| `Studio` | `string` | `studio` | `:92` |
| `Series` | `string` | `series` | `:93` |
| `ReleaseDate` | `string` | `release_date` | `:94` |
| `Runtime` | `int` | `runtime` | `:95` |
| `Trailer` | `string` | `trailer` | `:96` |
| `Director` | `[]string` | `director` | `:97` |
| `Publisher` | `string` | `publisher` | `:98` |

`TranslatedTitle` is documented as the 17th field, deliberately **not** a member of
`scrape.Fields()` (`scrape/field.go:24-28`, `docs/adr/0013-...:5-11`).
`organize.Metadata` and `organize.Actor` are type aliases of these
(`organize/organize.go:79-83`).

### 1.3 `nfoMovie` — the render shape

`organize/nfo.go:44-64` (plus `nfoSet` `:97-99`, `nfoActor` `:101-104`,
`nfoFileInfo`/`nfoStreamDetails`/`nfoVideoStream`/`nfoAudioStream`/`nfoSubtitleStream`
`:66-95`). This is a 1:1 XML mapping, not the field vocabulary — see §2.

### 1.4 The descriptor table (one row per field)

`fieldDescriptor` `scrape/field.go:311-339`, kinds `:294-300`
(`kindString/kindFloat/kindInt/kindStringList/kindActorList`), table
`fieldDescriptors` `scrape/field.go:341-386`. Each row carries `setString`/`setList`/
`setFloat`/`setInt`, `value`, `satisfied`, and `webURL`. `webURL` marks the four address
fields (Cover, Poster, Trailer, ExtraFanart) whose values the engine resolves against the
detail page (`scrape/engine.go:369-391`).

### 1.5 How multi-value fields are represented and deduplicated

| Field | Representation | Dedup | Citation |
|---|---|---|---|
| `tags` | `[]string` | Yes — order-preserving exact-match dedupe at extraction; **again** after tag-map rewriting | `scrape/engine.go:346`, `scrape/engine.go:623-637`; `plugins/tagmap.go:40-72` |
| `extra_fanart` | `[]string` of URLs | Yes — same `dedupe` at extraction | `scrape/engine.go:346` |
| `director` | `[]string` | Yes — same `dedupe` at extraction | `scrape/engine.go:346` |
| `actors` | `[]Actor{Name,Thumb}` | **No dedupe anywhere.** The engine appends every matched container (`scrape/engine.go:343-344`, `:420-434`); `actor_map` rewrites names in place and does not dedupe (`plugins/actormap.go:34-60`); `copyField` copies the slice verbatim (`scrape/scheduler.go:501-520`); the NFO render trims and skips blank names but does not dedupe (`organize/nfo.go:174-185`) | as cited |

Dedupe is exact byte match on the trimmed? — **no**: `dedupe` compares the raw string
before trimming (`scrape/engine.go:626-637`); trimming happens later at satisfaction
(`scrape/field.go:215`) and at NFO render (`organize/nfo.go:159-163`). **UNVERIFIED**
whether `" a"` and `"a"` are deduped — reading `dedupe` says no.

### 1.6 The ORIGINAL tool's metadata type

From the serde FIELDS recovery (`8369f4d:recovered/backend/api/DTO_FIELDS.md`,
raw dump `8369f4d:evidence/backend/recon/serde-fields-arrays.txt`):

- `0x2b4e838 [68]` (`serde-fields-arrays.txt:17`) is a **68-name run** whose first 5 entries
  are a category enum (`CN JP EA JAnime Unknown`) and whose remaining **63** are metadata
  field names — the largest single statement of ORIG's field vocabulary:

  ```
  Number PublishNumber PublishNumberDVD Actors OriginalActors ActorLinks ActorPhotos
  Cover Poster Director ExtraFanart Outline OriginalPlot Outline2 Release Runtime Series
  Source Studio Publisher Tags Title OriginalTitle Trailer UserRating UserVotes Website
  Year SourceFilename SourcePath JavdbId DMMCid SerialName SerialNumber Country
  TranslatedBy ActorDetails ThumbPath PosterPath FanartPath Subtitle Leak Hack Uncensored
  Censored NoSubtitlePrefix SubtitlePrefix LeakPrefix HackPrefix UncensoredPrefix
  CensoredPrefix Part ResolutionRaw ResolutionPrefix ResolutionSuffix Category
  _10musume _1Pondo TokyoHot Caribbean Heydouga Heyzo Unknown
  ```
  (`8369f4d:evidence/backend/recon/serde-fields-arrays.txt:17`)

- The task row's DTO is `0x2b409b0 [30]` (`DTO_FIELDS.md:16`) and includes
  `metadata`, `metadata_provider_info`, `metadata_all_sites` plus virtual columns
  `title`, `original_title`, `release`, `poster_provider`.
- `0x2b40208 [14]` (`DTO_FIELDS.md:13`) is a scraper/parse-result run:
  `poster thumb fanart extra_fanarts trailer id serial_name serial_number file_name
  file_path ext extra_meta category dmm_cid`.
- `0x2b41658 [5]` (`DTO_FIELDS.md:17`): `poster thumb fanart extra_fanarts trailer`
  (an image-set shape).
- The DTO file itself warns that adjacent arrays merge into one run and flags the
  attribution as suspect (`DTO_FIELDS.md:5`). So the exact struct boundary of the
  `[68]` run is **UNVERIFIED**, while the name list is verbatim.

---

## 2. NFO generation

### 2.1 OLD: a fixed struct marshal, **not** a switch surface and **not** a template

`RenderNFO(t Task, maxActorCount int) []byte` — `organize/nfo.go:108` — builds an
`nfoMovie` and calls `xml.MarshalIndent` (`organize/nfo.go:187-194`). There is no
per-field enable flag and no user-editable template. `nfoFileName = "movie.nfo"`
(`organize/nfo.go:282`), written atomically with a content-hash skip
(`organize/nfo.go:292-330`).

Full mapping (code + the spec's own table at `.scratch/open-mdcng/spec.md:468-491`):

| Metadata field | XML element | Notes | Citation |
|---|---|---|---|
| `Title` (or `TranslatedTitle` when non-blank) | `<title>` | number-prefixed | `organize/nfo.go:113-120`, `:123` |
| `OriginalTitle` (falls back to `Title`) | `<originaltitle>` | number-prefixed | `:117-120`, `:124` |
| — (derived) | `<sorttitle>` | **always equal to `<originaltitle>`** | `:130-134`; ADR `docs/adr/0017-...:5-11` |
| `Outline` | `<plot>` | the only plot slot | `:125` |
| `UserRating` (>0 only) | `<rating>` | 10-point value | `:137-139`; `docs/adr/0016-...` |
| `Runtime` (>0 only) | `<runtime>` | minutes, integer | `:140-142` |
| `Series` | `<set><name>` | omitted when blank | `:144-146` |
| `ReleaseDate` | `<premiered>` **and** `<releasedate>` | same value, double-written; `/`→`-` | `:151-154`, `:275-278` |
| `Tags` (trimmed, blanks dropped) | repeated `<tag>` | never `/`-joined | `:159-163` |
| `Publisher` | `<tag>发行: X</tag>` | degraded into a tag with a semantic prefix | `:164-167` |
| `Director` (trimmed, blanks dropped) | repeated `<director>` | | `:168-172` |
| `Actors` (trimmed, blank names skipped, capped) | repeated `<actor><name>` + optional `<thumb>` | cap = `paths.max_actor_count`, 0 = unlimited | `:174-185`; `config/schema.go:71-72` |
| `Streams` (plugin-produced) | `<fileinfo><streamdetails>` | absent unless the streamdetails plugin ran | `:185`, `:236-262`; ADR `docs/adr/0007-...`, `docs/adr/0010-...` |

Deliberately **never written** (`organize/nfo.go:17-28` (uniqueid `:22-23`, dateadded `:24`, locks `:25-26`, genre/country `:27-28`), restated at
`.scratch/open-mdcng/spec.md:532`):

| Not written | Reason given |
|---|---|
| `<uniqueid>` / `<id>` | "there is no external id to claim, and a guessed one would send the server's own scrapers down a wrong path" (`organize/nfo.go:22-23`) |
| `<dateadded>` | the server records its own ingestion time (`:24`) |
| `lockedfields` / `lockdata` / `watched` / `playcount` / `lastplayed` | NFO stays portable between servers (`:25-26`) |
| `<genre>` / `<country>` | Jellyfin splits both on `/`; not emitting them is how a tag containing a slash survives intact (`:27-28`) |
| `<thumb>` / `<fanart>` / `<logo>` / `<banner>` inside the NFO | images are found by file name, not by NFO path (`organize/images.go:14-20`) |

**Missing/empty handling.** Two mechanisms: (a) `omitempty` on every `nfoMovie` field
(`organize/nfo.go:44-64`), so a half-scraped task produces a half-full document
(comment `:41-43`); (b) explicit guards — `UserRating > 0` and `Runtime > 0`
(`:137-142`), `strings.TrimSpace(...) != ""` for Series/Publisher/each tag/each director
(`:144-172`), blank actor names skipped (`:174-185`). `numberPrefixed` returns `""` for an
empty title so a bare number is never emitted as a title (`:201-214`), and the number
boundary test is `leadsWithNumber` (`:216-232`).

**Empty-file behaviour:** `mixedFolderVideos` (`organize/nfo.go:337-360`) logs a warning
listing foreign videos in the target directory, because Jellyfin will not read `movie.nfo`
from a "mixed" folder; it blocks nothing (`organize/run.go:200-213`).

**Language / countrycode in OLD's NFO:** the only `<language>` elements are inside
`<fileinfo><streamdetails>` audio and subtitle streams, and they come from the ffprobe
plugin, not from any metadata field (`organize/nfo.go:86-95`, `:256-259`).
`<countrycode>` is never emitted.

**Image naming (OLD):**

| Field | File placed | Citation |
|---|---|---|
| `Poster` | `<dir>/poster.<ext>` | `organize/images.go:50-52` |
| `Cover` | `<dir>/fanart.<ext>` | `organize/images.go:53-55` |
| `ExtraFanart[i]` | `<dir>/extrafanart/fanart{i+1}.<ext>` (1-based) | `organize/images.go:56-65` |
| — | `thumb` / `logo` / `banner` **never placed**: "no scraped field supplies them in the sixteen-field list, so nothing writes them yet" | `organize/images.go:18-20` |

The extension is taken from the source URL's path, lowercased, `.jpg` as fallback
(`organize/images.go:107-123`). The layer does no format conversion or cropping
(`organize/images.go:12-16`; ADR `docs/adr/0007-organize-layer-no-image-processing.md`).
Stale images this program previously placed are removed, only inside the target dir
(`organize/images.go:88-104`).

### 2.2 ORIG: the "68 field switches" surface

The switch surface is a **config struct with 68 serde fields**, recovered verbatim:

`8369f4d:evidence/backend/recon/serde-fields-arrays.txt:27`

```
0x2b54848 [68] enabled enable_sorttitle enable_originaltitle add_part_to_title
enable_outline enable_originalplot enable_plot add_translated_by enable_release
enable_releasedate enable_premiered tagline_format enable_country enable_mpaa
enable_customrating enable_year enable_runtime enable_votes enable_score
enable_criticrating enable_actor enable_director enable_series enable_tag additional_tags
enable_genre enable_studio enable_maker enable_publisher enable_label enable_poster
enable_cover enable_trailer enable_website set_by_actress set_by_series
set_by_serial_name serial_name actors resolution mosaic has_subtitle subtitle_format
series series_format studio studio_format publisher publisher_format poster thumb fanart
extra_fanarts extra_fanarts_behind_scenes trailer thumb_download_strategy
hd_poster_amazon hd_poster_amazon_strict hd_poster_tenhow crop_type_censored
crop_type_uncensored crop_type_fc2 crop_type_amateur crop_type_cn crop_aspect crop_poster
crop_when_poster_low_res subtitles_folder
```

Composition (my count from the verbatim list): 37 fields of the NFO config
(`enabled` … `set_by_serial_name`), then **12 fields of the nested `additional_tags`
sub-struct** merged into the same run, then **19 more** (`poster` … `subtitles_folder`).
37 + 12 + 19 = 68. The merge of adjacent arrays is explicitly flagged as a hazard by the
DTO file (`DTO_FIELDS.md:5`), so treating all 68 as one struct is **UNVERIFIED**; the
name list is verbatim.

The serialized fixture shows the same keys with their shipped defaults —
`8369f4d:recovered/backend/config/fx_config_nfo.json` (one line):

| Switch | Default | UI label (from the recovered form) |
|---|---|---|
| `enabled` | `true` | 启用 NFO — "根据刮削数据生成 .nfo 文件" |
| `enable_sorttitle` | `true` | 类标题 |
| `enable_originaltitle` | `true` | 原标题 |
| `add_part_to_title` | `false` | 标题后添加分集信息 |
| `enable_outline` | `true` | 简介 (outline) |
| `enable_plot` | `true` | 简介 (plot) |
| `enable_originalplot` | `true` | 原简介 (originalplot) |
| `add_translated_by` | `false` | 简介后添加翻译来源信息（例如 "由 Google 提供翻译"） |
| `enable_release` | `true` | 发行日期 (release) |
| `enable_releasedate` | `true` | 发行日期 (releasedate) |
| `enable_premiered` | `true` | 发行日期 (premiered) |
| `tagline_format` | `"发行日期: {release}"` | Tagline 格式（"允许的字段请参考命名设置页面"） |
| `enable_country` | `true` | 国家 (country) |
| `enable_mpaa` | `true` | 分集信息 (mpaa) |
| `enable_customrating` | `true` | 自定义分集 (customrating) |
| `enable_year` | `true` | 年份 (year) |
| `enable_runtime` | `true` | 时长 (runtime) |
| `enable_votes` | `true` | 想看人数 (votes) |
| `enable_score` | `true` | 公众评分 (score) |
| `enable_criticrating` | `true` | 影评人评分 (criticrating) |
| `enable_actor` | `true` | 演员 (actor) |
| `enable_director` | `true` | 导演 (director) |
| `enable_series` | `true` | 系列 (series) |
| `enable_tag` | `true` | 标签 (tag) |
| `enable_genre` | `true` | 风格 (genre, 使用标签数据) |
| `enable_studio` / `enable_maker` | `true` | 片商 (studio) / 片商 (maker) |
| `enable_publisher` / `enable_label` | `true` | 发行商 (publisher) / 发行商 (label) |
| `enable_poster` | `true` | 海报 (poster) |
| `enable_cover` | `true` | 封面等 (cover/thumb/fanart) |
| `enable_trailer` | `true` | 预告片 (trailer) |
| `enable_website` | `true` | 网址 (website) |
| `set_by_actress` / `set_by_series` / `set_by_serial_name` | `false` / `true` / `false` | 合集：使用演员字段 / 使用系列字段 / 使用番号前缀 |
| `additional_tags.serial_name` | `true` | 附加标签：番号前缀 |
| `additional_tags.actors` | `true` | 演员 |
| `additional_tags.resolution` | `true` | 分辨率 |
| `additional_tags.mosaic` | `true` | 有码 / 无码 |
| `additional_tags.has_subtitle` | `true` | 中文字幕 |
| `additional_tags.subtitle_format` | `"中文字幕"` | 标签格式: 中文字幕 |
| `additional_tags.series` / `.series_format` | `true` / `"系列: {series}"` | 系列 / 标签格式: 系列 |
| `additional_tags.studio` / `.studio_format` | `true` / `"片商: {studio}"` | 片商 / 标签格式: 片商 |
| `additional_tags.publisher` / `.publisher_format` | `true` / `"发行: {publisher}"` | 发行商 / 标签格式: 发行商 |

Source for the defaults: `8369f4d:recovered/backend/config/fx_config_nfo.json`.
Source for the UI labels and the grouping (标题 / 简介 / 演员导演 / 发行日期 / 国家分级 /
年份时长想看人数 / 评分 / 系列标签风格 / 片商发行商 / 海报封面网址 / 合集):
`8369f4d:recovered/frontend/src/app/(app)/settings/nfo/form.tsx` (whole file; 161 lines).
That form exposes exactly **48 named inputs** — the 37 serde fields of the first group
(36 scalars plus all 12 `additional_tags` sub-keys, `additional_tags` itself being the
wrapper) and **nothing else**. In other words the UI covers the whole first group and
does **not** expose any of the 19-name tail
(`poster`/`thumb`/`fanart`/`extra_fanarts`/`extra_fanarts_behind_scenes`/`trailer`/
`thumb_download_strategy`/`hd_poster_*`/`crop_*`/`subtitles_folder`).

The 19 non-UI fields (`poster thumb fanart extra_fanarts extra_fanarts_behind_scenes
trailer thumb_download_strategy hd_poster_amazon hd_poster_amazon_strict
hd_poster_tenhow crop_type_censored crop_type_uncensored crop_type_fc2 crop_type_amateur
crop_type_cn crop_aspect crop_poster crop_when_poster_low_res subtitles_folder`):
the first six look like image **file names**, the rest like image-crop / HD-poster /
subtitle-folder settings. Their exact element names and semantics are **UNVERIFIED**
(the DTO file notes these are `#[serde(default)]` fields whose values equalled the
defaults and were skipped in serialization: `DTO_FIELDS.md:51`).

**Switch → XML element.** The switch *names* state their element for the
`enable_*` family: `enable_sorttitle`→`sorttitle`, `enable_originaltitle`→`originaltitle`,
`enable_outline`→`outline`, `enable_plot`→`plot`, `enable_originalplot`→`originalplot`,
`enable_releasedate`→`releasedate`, `enable_premiered`→`premiered`,
`enable_country`→`country`, `enable_mpaa`→`mpaa`,
`enable_customrating`→`customrating`, `enable_year`→`year`, `enable_runtime`→`runtime`,
`enable_votes`→`votes`, `enable_score`→`score`, `enable_criticrating`→`criticrating`,
`enable_actor`→`actor`, `enable_director`→`director`, `enable_series`→`series`,
`enable_tag`→`tag`, `enable_genre`→`genre`, `enable_studio`→`studio`,
`enable_maker`→`maker`, `enable_publisher`→`publisher`, `enable_label`→`label`,
`enable_trailer`→`trailer`, `enable_website`→`website`, `enable_poster`→`poster`,
`enable_cover`→`cover/thumb/fanart` (per its own UI label), `enable_release`→`release`.
The **actual writer code was never recovered** (the NFO writer is inside the
un-reversed `action::*` layer: `8369f4d:report/REPORT.md:210-214`, §6.2a). So
"how a field maps to an XML element" is inferred from switch names only, and the
following are **UNVERIFIED**: whether `<outline>` was emitted at all, whether the
`score`/`criticrating`/`votes` switches wrote `<rating>`/`<criticrating>`/`<votes>`,
whether `<uniqueid>`/`<id>`/`<language>`/`<countrycode>` were ever written, and how
empty fields were skipped.

**Missing/empty handling in ORIG:** **UNVERIFIED** (no writer code). The only related
evidence is `PUT /api/tasks/{id}/nfo` with `Json<UpdateNfoParam{metadata,
metadata_provider_info}>` (`8369f4d:recovered/backend/api/API_TABLE.md:73`), i.e. the
frontend could push an edited metadata blob back to be re-rendered.

---

## 3. Field priority

### 3.1 OLD: three layers, no category axis

Documented in `docs/adr/0002-field-cursor-scheduler.md` and implemented in
`scrape/schedule.go` + `scrape/scheduler.go`.

**Layer 1 — global source order.** `[[sources]]` is an ordered array of
`config.Source` (`config/schema.go:370-426`); `BuildPlan` resolves it once into
`globals []Step` (`scrape/schedule.go:116-160`). Every field not named in layer 2 gets
this list, filtered by layer 3 (`scrape/schedule.go:197-203`).

**Layer 2 — per-field override.** `scrape.field_priority` is
`map[string][]string` — "Ordered source list per field … This fully replaces the global
source order for that field and stops when the list is exhausted: it is not padded with
the global order" (`config/schema.go:166`). Keys are read with `ParseFieldName` (trim +
case-fold), unknown names are an error, and the same field under two spellings is an
error (`scrape/schedule.go:167-181`). An id that is not an *enabled* `[[sources]]` entry
is an error (typo); an id that is configured but unresolvable, or inapplicable to the
number family, is silently skipped (`scrape/schedule.go:218-248`).

**Layer 3 — blocking, two kinds:**
- `scrape.ignore_fields []string` — "Fields to never look up, whatever the source lists
  say." A blocked field gets an empty schedule and is not in `WantedFields()`
  (`config/schema.go:159-161`; `scrape/schedule.go:184-190`, `:250-262`). Unknown names
  are refused (`scrape/schedule.go:264-276`).
- per-source `ignore_fields []string` — "Fields this source's answer is discarded for"
  (`config/schema.go:423-425`); applied by `filterBlocked` / `blockedFor`
  (`scrape/schedule.go:250-262`).

**Evaluation order** (from the code, in order):
1. `[[sources]]` entries that are disabled → skipped (`SkipDisabled`);
   enabled but unresolvable → skipped (`SkipUnavailable`);
   `AppliesTo(ApplicablePatterns, prefix)` false → skipped (`SkipInapplicable`) —
   `scrape/schedule.go:126-159`. `SkipReason` enum `scrape/schedule.go:52-70`;
   `AppliesTo` `scrape/source.go:213-227`.
2. `scrape.ignore_fields` parsed and refused on typos (`scrape/schedule.go:162-165`).
3. `scrape.field_priority` parsed, refused on typos/duplicates
   (`scrape/schedule.go:167-181`).
4. Per field, in `Fields()` order: blocked → empty; configured override → `resolveList`;
   else → global order filtered by the source's own `ignore_fields`
   (`scrape/schedule.go:184-203`).

**The scheduler's runtime semantics** (`scrape/scheduler.go`):
- Invariant: "a source is asked ⟺ it is the next candidate for some field that is still
  unsatisfied, and it has not already been asked in this task" (`scrape/scheduler.go:11-14`).
- One request per source per task (`:316-357`, `asked` map).
- First satisfier wins, and "first" means *highest-priority source that has answered*, so
  harvest scans the schedule head up to and including the cursor
  (`scrape/scheduler.go:422-450`).
- A field waiting behind an in-flight source keeps waiting rather than moving on
  (`:360-380`).
- All wanted fields are launched concurrently at task start (`:316-357`).
- In-flight requests are never cancelled, even after every field is satisfied (`:250-256`).

### 3.2 ORIG: six category axes + `by_fields` + `ignore_fields`

Config shape, verbatim from the served fixture
`8369f4d:recovered/backend/config/fx_config_provider.json`:

```
priorities = {
  jav_censored, jav_uncensored, jav_amateur, jav_fc2, cn, ea,   // 6 ordered provider lists
  by_fields:      { <FieldName>: [provider...] },
  ignore_fields:  { <FieldName>: [provider...] }
}
priorities_user_modified: bool
series_type_keywords:          { <5 categories>: [keywords] }
series_type_keywords_in_path:  { <5 categories>: [keywords] }
configs: { <ProviderName>: {enabled, base_url, cookies, user_agent, cooldown_seconds,
           retry_times, retry_times_override, proxy, api_key, disable_hash_match} }
```

Observed values: 21 provider configs; the six category lists have 13/9/12/8/4/1 entries;
`by_fields` ships defaults for exactly **8 fields** —
`Outline:[airav_io]`, `Poster:[dmm,Mgstage,hbox_jp]`, `ExtraFanart:[Javbus,avbase,freejavbt]`,
`Tags:[Javbus,avbase,freejavbt]`, `UserRating:[dmm,jav321]`, `Cover:[dmm,Mgstage]`,
`OriginalTitle:[dmm,Mgstage]`, `Title:[airav_io]`; `ignore_fields` is `{}`;
`priorities_user_modified=false`; `series_type_keywords`/`..._in_path` have 5 keys each
(`jav_censored jav_uncensored jav_amateur cn ea` — **no `jav_fc2`**), all empty.
Source: `8369f4d:recovered/backend/config/fx_config_provider.json` (read in full).

Field vocabulary for `by_fields`/`ignore_fields` in the UI: `ALL_FIELDS` has **18** entries
(`Number PublishNumber Title OriginalTitle Outline OriginalPlot Actors Cover Poster
ExtraFanart Tags Release Runtime UserRating Director Series Studio Publisher UserVotes`)
— recovered from `8369f4d:recovered/frontend/src/lib/constants.ts`, quoted verbatim in the
OLD repo's own research note `.scratch/open-mdcng/research/06-re-artifacts.md:41-45`
and in `8369f4d:recovered/frontend/src/app/(app)/providers/components/provider-settings.tsx`.

Semantics, quoted from the recovered UI documentation strings
(`8369f4d:recovered/frontend/src/app/(app)/providers/components/provider-settings.tsx`,
lines ~564 and ~604; quoted in `.scratch/open-mdcng/research/06-re-artifacts.md:45`):
- `by_fields`: 「当字段优先级中的刮削源未获取到数据时, 会使用全局优先级中的刮削源进行补充」
  → field-level priority is a **supplement/fallback to** the category/global chain, not a
  replacement. (The opposite of OLD's layer 2 — see ADR-0002:9, which calls ORIG's model
  「字段级补充回填」.)
- `ignore_fields`: 「忽略配置的刮削源返回的相应字段结果」.

Category decision chain, as recorded by OLD's research:
`category_rules` (ordered, first match wins, case-insensitive) >
`series_type_keywords(_in_path)` > built-in mapping —
`.scratch/open-mdcng/research/06-re-artifacts.md:45`, `.scratch/open-mdcng/issues/06-re-artifact-audit.md:15`.
The config *shape* of `category_rules` is A-grade evidence
(`[{match_against: path|number, pattern, value}]`, default `[]`,
`8369f4d:recovered/frontend/src/app/(app)/settings/naming/rule-row.tsx` +
`8369f4d:recovered/frontend/src/app/(app)/settings/components/schemas.ts`, quoted at
`.scratch/open-mdcng/research/06-re-artifacts.md:13`); the **evaluation order is
inferred, not control-flow-verified** — the OLD research marks it A for the config and
leaves the merge control flow as a gap
(`.scratch/open-mdcng/research/06-re-artifacts.md:50`).

Naming has a *second*, differently-spelled category axis:
`naming.category_mapping` has **8** keys `jp_censored jp_uncensored jp_amateur jp_fc2 cn
ea janime unknown` (`8369f4d:recovered/backend/config/fx_config_naming.json`).
`series_type_keywords` has 5 (no fc2). `series_type_keywords_in_path` has 5.
OLD's ADR-0002:5 records that the whole category axis (per-category priority lists,
a source's category declaration, the `category_rules` chain) was **deleted** in OLD, and
that source applicability moved to the number's shape (`ApplicablePatterns`).

**Where the per-field source is recorded:**
- ORIG: `task.metadata_provider_info` (json) — a `{FieldName: ProviderName}` map
  (`8369f4d:recovered/backend/db/schema.sql:2`;
  `8369f4d:recovered/frontend/src/actions/resources/tasks/update-task-nfo.ts:15,37,43`).
  The generated column `poster_provider` is derived from it:
  `coalesce(json_extract(metadata_provider_info,'$.Poster'),
  json_extract(metadata_provider_info,'$.Cover'))`
  (`8369f4d:recovered/backend/db/schema.sql:2`).
- OLD: `Summary.Sources map[Field]string` (`scrape/scheduler.go:98-100`), persisted as
  `task.metadata_sources` (`scrape/pool.go:486`, `store/scrape.go:97`), exposed by the API
  (`api/tasks.go:477`, `:637`) and read back by the organize layer to pick the image
  download transport (`organize/organize.go:146-150`, `organize/images.go:29-38`).

---

## 4. Source tracing (`metadata_all_sites`)

### 4.1 ORIG

| Aspect | Fact | Citation |
|---|---|---|
| Column | `task.metadata_all_sites json_text` (next to `metadata`, `metadata_provider_info`) | `8369f4d:recovered/backend/db/schema.sql:2` |
| Shape (frontend) | `Record<site, Record<field, value>>` | `8369f4d:recovered/frontend/src/actions/resources/tasks/update-task-nfo.ts:14`; `.../components/sidebar/image-crop-modal.tsx:44`; `.../tasks/[id]/components/task-action-bar.tsx:49`; `.../tasks/components/provider-badge.tsx:82` |
| Who writes it | the scrape pipeline (writer code not recovered — `report/REPORT.md:210-214`) | — |
| `PUT /api/tasks/{id}/nfo` | body is `UpdateNfoParam{metadata, metadata_provider_info}` — **`metadata_all_sites` is not in this body** | `8369f4d:recovered/backend/api/API_TABLE.md:73` |
| Who reads it | (a) the task detail page: `8369f4d:recovered/frontend/src/app/(app)/tasks/[id]/page.tsx:117-122,241-242`; (b) `provider-badge.tsx:81-96` (badge colour + link to the site's `Website`); (c) `task-action-bar.tsx:220-247` (per-field "use this site's value" picker); (d) `image-crop-modal.tsx:108-115` (source of the cover to crop); (e) `update-task-nfo.ts:24-43` (builds the new `metadata` + `metadata_provider_info` from a chosen site) | as cited |
| Does the NFO read it? | No evidence of any NFO path reading it; the NFO writer was not recovered. **UNVERIFIED / no evidence** | — |

Note the OLD research note claims `PUT /api/tasks/{id}/nfo` updates "二者"
(`.scratch/open-mdcng/research/06-re-artifacts.md:47`); the API table says the body carries
two of the three columns. Both statements are consistent if "二者" = `metadata` +
`metadata_provider_info`.

### 4.2 OLD

| Aspect | Fact | Citation |
|---|---|---|
| Go shape | `Summary.AllSites map[string]Result` — "every raw answer, keyed by source id" | `scrape/scheduler.go:102-105` |
| `Result` | `{Found bool, URL string, Status int, Metadata Metadata}` | `scrape/source.go:186-208` |
| Stored JSON shape | `{ <sourceID>: { found, status?, url?, fields?: { <fieldName>: <value> } } }`; `fields` only carries fields that pass `Satisfied` | `scrape/pool.go:762-790` |
| Column | `task.metadata_all_sites` (opaque text; the store never parses it) | `store/tasks.go:29-30`, `store/scrape.go:16-38`, `:90-100` |
| Read back | `store.ScrapeResult` (`store/scrape.go:53-70`) — but **the store deliberately does not compute the missing set** | `store/scrape.go:57-66` |
| API | `GET /api/tasks/{id}` → `metadata_all_sites` raw JSON, alongside `metadata` and `metadata_sources` | `api/tasks.go:468-478`, `:636-638` |
| Frontend | rendered as a raw JSON block on the task detail page | `web/src/pages/TaskDetailPage.tsx:634` (label `copy.taskDetail.fields.allSites`), `web/src/api.ts:46` |
| Acceptance test shape | `AllSites map[string]map[string]any \`json:"metadata_all_sites"\`` | `acceptance/acceptance_test.go:1223` |
| Does the NFO read it? | **No.** `organize.TaskFromStore` decodes only `metadata` and `metadata_sources` | `organize/organize.go:124-152` |

The empty-blob rule is worth noting: `RecordScrape` never overwrites a column with an empty
string, "because 'the scheduler had no answer for this' must not wipe a raw-answer record
that a previous run left" (`store/scrape.go:74-99`).

---

## 5. The "required field" concept

### 5.1 The two named citations

`scrape/scheduler.go:87-93`:

```go
// Summary is the finished schedule: what the task learned, where each field
// came from, and what never arrived.
//
// A summary with a non-empty Missing set is a **partial** success, not a
// failure. There is no required field in V1: a missing cover means the organize
// stage runs without one and the task lands in the group the user acts on,
// rather than being an error they retry blindly.
```

`docs/adr/0002-field-cursor-scheduler.md:16`:

```
- 「不爬不相关源」依赖源作者的番号形态声明准确；partial（游标耗尽仍有缺失字段）是一等任务状态，无「必填字段」概念，缺失集与逐字段来源暴露给任务模型支撑定向重试。
```

Both confirmed verbatim. `grep -rn required --include=*.go` over OLD finds **no**
required-field logic anywhere (only unrelated config-required errors, e.g.
`config/config.go:266,376-382`).

### 5.2 Every place a task ends `partial` vs `failed` vs `success`

Terminal statuses: `success`, `partial`, `failed` (`store/transitions.go:12-21`,
`IsTerminalStatus` `:24-32`). `partial` and `failed` are both in the
"needs attention" group (`store/tasks.go:71-74`).

| Site | Verdict | Citation |
|---|---|---|
| Any wanted field unsatisfied after all cursors are exhausted, but files placed and artifacts written | **`partial`** (log level `warn`, message "organized with N field(s) missing: …", detail `{"missing": [...]}`) | `organize/run.go:226-238` |
| Scrape budget (`scrape.task_timeout_seconds`) expires | Run freezes with what it has, is still placed, ends `partial`; the log line gets the timeout reason plus `{"timeout": true}` | `config/schema.go:150-151`; `scrape/pool.go:443-490`; `scrape/pool.go:569-…` (`timeoutReason`) |
| Cursors exhausted with fields missing (not a cut-off) | Same `partial` outcome; the scrape log line is raised to `warn` | `scrape/pool.go:477-482` |
| Every wanted field answered | **`success`** | `organize/run.go:226` |
| A file is neither at its source, nor at its recorded target, nor already at its computed target | **`failed`** — `FileError{ErrMissingEverywhere}` | `organize/execute.go:61-63`, `:131-149` |
| Hardlink across filesystems (`EXDEV`) | **`failed`** — `CrossDeviceError`, never silently downgraded to copy | `organize/execute.go:49-58`, `:61-68`; `docs/adr/…` per `.scratch/open-mdcng/spec.md:507-508` |
| Any image download fails | **`failed`** — "a failed download must fail the run, not clobber a good image with a bad promise" | `organize/images.go:70-76`; surfaced at `organize/run.go:175-178` |
| `writeNFO` fails (temp file / chmod / rename) | **`failed`** | `organize/nfo.go:292-330`; `organize/run.go:195-198` |
| Malformed `flags` blob (only the program writes it) | **`failed`** — "a defect, not a state a retry can fix" | `organize/run.go:100-104` |
| Malformed `artifacts` blob | **`failed`** | `organize/run.go:168-171` |
| Malformed `metadata` / `metadata_sources` blob | **Not** a failure — decoded tolerantly; "the metadata is external input that a source half-wrote" | `organize/organize.go:133-151` |
| Template expansion / plan failure (e.g. file base name not starting with the dir base name) | **`failed`** | `organize/run.go:127-129`; `docs/adr/…` validator |
| Media-server refresh fails | **warning only** — never changes the terminal status | `organize/run.go:222-232` |
| A plugin hook fails | **warning only** (ADR-0010) | `plugins/plugin.go:1-9`; `organize/run.go:200-213`, `:218-221` |
| Scrape-stage error (plan build, store write, scheduler error) | **`failed`** | `scrape/pool.go:590-612` (`Pool.fail`) |
| Attempt budget spent after a crash/restart | **`failed`** with "it has already been tried N time(s) and the limit is M, so it needs a human" | `store/scrape.go:288-315`; `config/schema.go:154-157` |
| Process shutdown mid-task | **Neither** — nothing decided; the next start's reset rewinds the task | `scrape/pool.go:443-480`; ADR-0005 |
| A cut-off run always has ≥1 missing field (so "nothing missing" and "we were stopped" cannot both be true) | Guaranteed by the cursor rule; guard test named `TestScheduler_ACutOffRunAlwaysHasAMissingField` | `scrape/scheduler.go:262-290`; `organize/run.go:26-33` |
| `missing_fields` in the API | `null` = "not computed" (task never scraped), `[]` = "every wanted field answered" | `api/tasks.go:474-490`, `:644-731` |
| Retry semantics | Only one meaning: re-run the whole task under the current configuration; a "partial re-run" would need its own definition of what to keep | `store/retry.go:16-18`; `api/taskretry.go:69` |

---

## 6. Language

### 6.1 OLD

| Question | Fact | Citation |
|---|---|---|
| Any language field on the metadata? | **No.** The 16-field vocabulary has none. | `scrape/field.go:33-48` |
| Original-language marker? | `original_title` only — a *title* field, not a language tag. The NFO's `<sorttitle>` is documented as "the original-language title behind the number". | `scrape/field.go:34`; `organize/nfo.go:128-131` |
| Translation-related field? | `TranslatedTitle` — derived, written by the translate plugin, not in `Fields()` | `plugins/plugin.go:84`; `docs/adr/0013-…:5-11` |
| Translation target | `plugins.translate.target_lang`, enum `zh_cn,zh_tw` | `config/schema.go:328-329` |
| Language detection | Yes, a zero-dependency heuristic used to *skip* a paid call: kana ⇒ Japanese ⇒ translate; Han-dominated + length ≥4 ⇒ Chinese; small marker-rune sets decide simplified vs traditional | `plugins/translate.go:288-330` (`alreadyInTargetLanguage`, `looksChinese`, `containsKana`, `containsAnyRune`); marker sets `:337-338` |
| Where does `language` appear in output? | Only inside `<fileinfo><streamdetails>` audio/subtitle streams, from the ffprobe plugin | `organize/nfo.go:86-95`, `:256-259` |
| Subtitle file language token | Kept as its own name segment when renaming companion subtitle files | `organize/plan.go:250-262` |

### 6.2 ORIG

| Fact | Citation |
|---|---|
| Metadata field names include `Country` and `TranslatedBy` | `8369f4d:evidence/backend/recon/serde-fields-arrays.txt:17` |
| Config `metadata` section: `static_data_lang: "zh_cn"`, `auto_translate_title: false`, `auto_translate_outline: false`, `translate_engine: "OpenAI"`, `deepl_endpoint`, `deepl_api_key`, `openai_custom_prompt`, `use_title_from_sehuatang: true` | `8369f4d:recovered/backend/config/fx_config_metadata.json` |
| A language enum exists: `zh_cn zh_tw jp` | `8369f4d:evidence/backend/recon/serde-fields-arrays.txt:18` |
| Translate engines recovered: `DeepL Deeplx Google OpenAI` | `8369f4d:recovered/backend/api/DTO_FIELDS.md:48` |
| NFO config has an `enable_country` switch (国家 / country) and `add_translated_by` (简介后添加翻译来源信息) | `8369f4d:recovered/backend/config/fx_config_nfo.json`; `8369f4d:recovered/frontend/src/app/(app)/settings/nfo/form.tsx` |
| No `enable_language` / `enable_countrycode` switch exists in the 68 | `8369f4d:evidence/backend/recon/serde-fields-arrays.txt:27` |
| Whether ORIG detected a text's language at runtime | **UNVERIFIED** (translate behaviour is in the unrecovered `action::*` layer; `report/REPORT.md:210-214`) |

---

## 7. NFO templates

| Question | OLD | ORIG |
|---|---|---|
| Is the NFO templated? | **No.** `encoding/xml` struct marshal of a fixed shape (`organize/nfo.go:108`, `:187-194`). The OLD research states it explicitly: "NFO 生成在旧仓**不是模板**…所以旧仓**没有**「NFO 模板变量集」这个东西" (`/root/clone/mdc-lite` research artifact `research/nfo-templates:research/nfo-templates.md` §4.1, which cites `organize/nfo.go:44,108`) | **No.** A fixed switch surface (68 config fields, §2.2) plus two string formats: `tagline_format` and `additional_tags.{subtitle,series,studio,publisher}_format`, whose placeholders are naming-template variables ("允许的字段请参考命名设置页面" — `8369f4d:recovered/frontend/src/app/(app)/settings/nfo/form.tsx`). No NFO body template. |
| Is anything else templated? | Yes — **path templates**: `organize.dir_template` / `file_template` with a 25-variable set | `config/schema.go:177-179`; `config/naming.go:50-78` (`TemplateVariables()`); substitution at `organize/plan.go:330-365` |
| What are the 25 variables? | `number publish_number series_name serial_number first_letter series category actor first_actor title translated_title originaltitle year director studio publisher runtime release part source_filename filename source_path subtitle mosaic resolution` | `config/naming.go:51-78` |
| Where did 24 come from? | "the original tool's 24 plus `{translated_title}`"; the ticket list enumerates 23 while calling it 24, and `part` is the reconciling 24th | `config/naming.go:41-49`; the 23-name list at `.scratch/open-mdcng/issues/05-organize-metadata.md:25` |
| AI template endpoint in ORIG | `/api/ai/generate-template` (hyphenated, `Json<{prompt,…}>`), plus `POST /api/config/naming/validate-template` and `GET /api/config/naming/variables` | `8369f4d:recovered/backend/api/API_TABLE.md:33`, `:31`; `8369f4d:recovered/backend/api/ROUTES.md:17,95` |

---

## 8. The reverse-engineering record (ORIG) — field / NFO / model / priority / tracing

`git -C /root/clone/open-mdcng show 8369f4d:report/REPORT.md` (323 lines). Sibling report
files: `report/diagrams/*` (JSON + HTML + PNG), `notes/{README,01-retracted-claims,
02-dead-ends,03-tool-gotchas}.md`, `adr/0001-fresh-case-not-continuation.md`,
`adr/0002-frontend-runnable-backend-understanding-level.md`, `scope.md`, `timeline.md`,
`workitems.md`, and the whole `recovered/` + `evidence/` trees
(`git ls-tree -r --name-only 8369f4d`).

What it establishes, relevant to fields:

| Fact | Citation |
|---|---|
| Acceptance criterion is split: frontend = runnable-level, backend = **understanding-level, contract layer only** | `8369f4d:report/REPORT.md:14-38` |
| The backend's **execution layer was never reversed**: `action::*` = 40 method signatures (scrape 14 / reorganize 14 / manual 5 / actress 7) | `8369f4d:report/REPORT.md:210-214` (§6.2a) |
| Behaviour layer (parser 100 syms / config 204 / webhook 5) is field-table only, no behaviour | `8369f4d:report/REPORT.md:243-244` (§6.2c) |
| `parser` and `config` are the unreversed modules; the NFO writer lives in the unreversed action layer | same |
| DTO/enum recovery: 425 verbatim serde field strings, 65 functions / 504 constants for the scrapers | `8369f4d:report/REPORT.md:160-176` (§4) |
| 6 business tables; `task` has 4 GENERATED VIRTUAL columns (`title`/`original_title`/`release`/`poster_provider`) derived by `json_extract`, which is why the DTO has 4 fewer fields than the schema has columns | `8369f4d:report/REPORT.md:190-197` |
| `config` is one aggregate of 8 sections: `common watermark parser naming download metadata provider nfo`; `PUT /api/config/*` is a **whole-section replace** with `#[serde(default)]` everywhere, so `PUT {}` resets the section | `8369f4d:recovered/backend/api/DTO_FIELDS.md:13,22,34,40`; `8369f4d:recovered/backend/api/API_TABLE.md:87` |
| No `/api/config/parser` route exists even though a `parser` section does — parser rules are built-in, not API-editable | `.scratch/open-mdcng/research/06-re-artifacts.md:17` |
| Scraper universe: 10 native variants (`Carib Fc2 Javbus Javdb Javlibrary Madouqu Madou Mgstage Mmtv ThePornDB`), 11 config-driven variants, 13 embedded YAML files; `arzon`/`fc2ppvdb` exist in the YAML table but have no enum variant and are not in the default config | `8369f4d:report/REPORT.md:236-243`; `8369f4d:recovered/backend/api/DTO_FIELDS.md:49` |
| The 13 YAML sources' **text was never recovered** (`include_str!` into `.rodata`); only the schema field names are known | `8369f4d:recovered/backend/scraper/PROVIDERS.md:28` |
| Provider config is 11 runtime fields (`enabled base_url cookies user_agent cooldown_seconds retry_times retry_times_override proxy api_key disable_hash_match sanitize_actors_info`) | `8369f4d:recovered/backend/scraper/PROVIDERS.md:28`; `8369f4d:recovered/backend/api/DTO_FIELDS.md:50` |
| YAML `ScrapeRule` = `{field, xpath, value, processes, is_url, add_to_context, is_array, min_size, in_search_page}`; `Process` enum = `{String,Number,Regex,DurationToMinutes,LibXml,EncodingsString,Case}` | `8369f4d:recovered/backend/scraper/PROVIDERS.md:28` |
| Image/crop/watermark config that ORIG had and OLD pushed into plugins: `crop_type_censored/uncensored/fc2/amateur/cn`, `crop_aspect`, `crop_poster`, `crop_when_poster_low_res`, `hd_poster_amazon`, `hd_poster_amazon_strict`, `hd_poster_tenhow`, `thumb_download_strategy`, `extra_fanarts_behind_scenes`, `subtitles_folder`, plus a full `watermark` section | `8369f4d:evidence/backend/recon/serde-fields-arrays.txt:27,31,207`; `docs/adr/0007-organize-layer-no-image-processing.md` (table) |
| Whether the ORIG emitted `<outline>` is unverifiable from the record; the DTO proves an `Outline`, `OriginalPlot` and `Outline2` field existed | `8369f4d:evidence/backend/recon/serde-fields-arrays.txt:17` |

---

## 9. The two mdc-lite research artifacts (branches, not on `main`)

Found with `git -C /root/clone/mdc-lite ls-tree -r --name-only research/nfo-templates`
→ `README.md`, `research/nfo-templates.md` (644 lines);
`… research/source-landscape` → `README.md`, `research/source-landscape.md` (240 lines).
Both branches' `README.md` is byte-identical to the `main` README (the field draft), so
they add no constraints beyond the two research documents.

### 9.1 `research/nfo-templates:research/nfo-templates.md` — what constrains the field list

Evidence grades used in that file: **A** = source verbatim / official docs; **B** = official
issue reports; **C** = community inference. It states explicitly that **no real Jellyfin or
Emby instance was tested** (`§6.1`), so every server behaviour below is source-level, not
machine-verified.

**Which element names Jellyfin/Emby actually parse** (§3.2, from
`MediaBrowser.XbmcMetadata/Parsers/BaseNfoParser.cs` `switch (reader.Name)`):

- Jellyfin: `actor aired aspect aspectratio biography communityrating country countrycode
  credits criticrating customrating dateadded director displayorder durationinseconds
  enddate fanart fileinfo format3d formed genre height language lastplayed localtitle
  lockdata lockedfields mpaa name originaltitle playcount plot premiered rating ratings
  releasedate review runtime sortname sorttitle streamdetails studio style subtitle tag
  tagline thumb title trailer uniqueid value video watched width writer year`; plus
  `MovieNfoParser.cs`: `album artist id name set`.
- Emby: `actor aired aspectratio biography country countrycode credits criticrating
  customrating dateadded director displayorder enddate fileinfo format3d formed genre
  language localtitle lockdata lockedfields mpaa name originaltitle plot premiered rating
  releasedate review role runtime sortorder sorttitle streamdetails studio style tag
  tagline title trailer video writer year`; plus `album artist id name set`.
  (`role`/`sortorder` are **actor child nodes**, not top-level tags.)

**Dead elements / semantics traps** (§3.2-3.4):

| Element | Fact | Consequence stated |
|---|---|---|
| `<outline>` | **Not a case in either parser.** "Kodi 系的 `<outline>` 写进去没有任何效果，简介只能写 `<plot>`" | The ticket's `outline` field has no Jellyfin/Emby slot |
| `<rating>` | Jellyfin `BaseNfoParser.cs:534-542` → `item.CommunityRating`; Emby the same. **No NFO tag can set Jellyfin's user rating**; the only user-data tags are `watched`/`playcount`/`lastplayed` | "这一位要按「外部评分」而不是「我的评分」来用" |
| `<plot>` / `<review>` / `<biography>` | All three write `item.Overview`; **last one in the file wins** | — |
| `<tag>` / `<style>` | Same branch, `AddTag`, repeatable, **not** split on `/` | Multi-values must be repeated elements |
| `<genre>` / `<country>` | **Both split on `/`** in both servers | A tag containing `/` survives in `<tag>`, not in `<genre>` |
| `<premiered>` / `<releasedate>` / `<aired>` / `<formed>` | One branch writing `PremiereDate`; Emby only has `premiered`/`releasedate` | Double-writing is what covers the Kodi-lineage ambiguity |
| `<year>` | `ProductionYear` when `> 1850`; the `premiered` branch does `ProductionYear ??= releaseDate.Year` | — |
| `<runtime>` | `RunTimeTicks`, minutes, "取空格前那一段" | — |
| `<uniqueid>` | Jellyfin: yes (`type` attribute names the provider). **Emby: no `uniqueid` case at all** (it uses `<id>`'s `IMDB`/`TMDB`/`TVDB` attributes and `<imdbid>`/`<tmdbid>`-style tags) | — |
| `<thumb>` / `<fanart>` inside the NFO | Jellyfin reads them via `FetchThumbNode`; **Emby has neither case** (images are local-discovery only). Also: "Artwork defined in .nfo files with local paths or URLs have priority over remote image providers and images in the media folders" | Writing image paths into the NFO overrides local images |
| `ratings` (plural) | Jellyfin-only (multiple `rating` children + `name`) | — |
| `set` | Both accept `<name>` child **or** bare text | — |
| `countrycode` / `language` | Both parsers have the cases | (Not emitted by OLD.) |

**Which image file names work** (§3.6, from Jellyfin docs + `LocalImageProvider.cs` + Emby
docs):

| Name written | Jellyfin | Emby | Notes |
|---|---|---|---|
| `poster.jpg` | Primary ✅ | Primary ✅ | bare name works in both |
| `fanart.jpg` | Backdrop ✅ | Backdrop ✅ | bare name |
| `thumb.jpg` | Thumb ✅ | Thumb ✅ | bare name |
| `logo.png` / `banner.jpg` | Logo / Banner ✅ | ✅ | bare name |
| `extrafanart/fanart1.jpg`, `fanart2.jpg`… | ✅ | ✅ (`fanartX.ext`) | "**唯一两家都明示的多背景图写法**" |
| `<video base name>.jpg` | Would **win over** `poster.jpg` | — | Jellyfin tries the video's own base name first (`_videoImageFileNames`, lines 55-62; main logic 258-305) |
| `extrafanart/` file names | **Not matched** — every supported image in the folder becomes a Backdrop (lines 347-357) | docs require `fanartX.ext` | "Jellyfin 侧 `fanart1.jpg` 只是惯例" |
| root-level `backdrop-N.jpg` | ✅ | ✅ | deliberately not double-written |
| Image extensions | `.png .jpg .jpeg .webp .tbn .gif .svg` | `jpg jpeg png gif tbn` | Intersection: `jpg/jpeg/png/gif/tbn`; `webp`/`svg` Jellyfin-only |

**`movie.nfo` is the only correct file name** (§3.1): Jellyfin's
`GetMovieSavePaths` yields `VIDEO_TS/VIDEO_TS.nfo` → `movie.nfo` (guarded by
`!IsInMixedFolder && ItemType == Movie`) → `Path.ChangeExtension(item.Path,".nfo")`;
Emby's order is the reverse (video-basename first, then `movie.nfo`). A `<番号>.nfo` in a
multi-part directory is read by **neither**. Three upstream issues
(`jellyfin#10493`, `#15946`, `#17036`) all CLOSED NOT_PLANNED. Emby has **no NFO
documentation page**, and `MediaBrowser/Emby` last public commit 2024-03-27.

**`IsInMixedFolder`** (§3.7): `resolverResult.Count > 1 || parent?.IsTopParent == true`
(`MovieResolver.cs:287`), reset to `false` when a directory resolves exactly one item with
no photos and no other subfolders (478-496). For the "one number per directory" layout the
predicate degenerates to `Count > 1`, so `movie.nfo` is read. Also: `extrafanart` is **not**
in `AllExtrasTypesFolderNames` (`NamingOptions.cs:714-716`).

**Template engine facts** (§2): `gonja v2.9.1` is the only one of the three candidates that
is actually Jinja2; `pongo2 v6.1.0` is Django syntax and fails
`join(", ")`, `default("x")`, `trim`, conditional expressions, `{% for %}...{% else %}`,
`loop.*`, `range()`; `text/template` needs every construct rewritten and does not escape.
Autoescape must be **on** (gonja defaults off: `config/config.go:48`; pongo2 defaults on:
`context.go:11`) or a `&` in a plot makes the XML illegal and both servers silently apply
nothing. `StrictUndefined` defaults `false` (`config/config.go:49`), which suits "omit the
whole element when the field is missing". The README's output line
`{ for idx,extrafanart_i in extrafanarts }` is **not legal Jinja2** and
`{% for idx, f in fanarts %}` over a `[]string` unpacks to `(value, empty)`, not
`(index, value)` — `loop.index`/`loop.index0` is the working form.

**Cross-references into OLD** the artifact itself records (§4.1, §5, §6.9):
`organize/nfo.go:44,108` (no template, `RenderNFO`), `config/naming.go:50` (25 variables),
`docs/adr/0006-nfo-fixed-filename.md:23`, `docs/adr/0017-title-slots-carry-the-number.md`,
and the note that `open-mdcng/.scratch/open-mdcng/research/05-nfo-emby-jellyfin.md` is the
path (not `research/`). It also states that ORIG's `enable_outline` switch "在
Jellyfin/Emby 上是空操作" — i.e. copying the old config surface inherits a no-op switch.

### 9.2 `research/source-landscape:research/source-landscape.md` — what each first-batch scraper can supply

Measured 2026-10-26 (UTC+8) with `curl`, browser UA, egress `172.197.168.204` (Kuala
Lumpur, datacentre IP, **not** a Japanese exit), serial with 2-4 s sleeps. Per-source
reachability is in the doc's §1 table; per-source *field* facts are:

| Source | Fields it can actually supply | Evidence in the doc |
|---|---|---|
| **javdb** (APP JSON API, not the website) | search `GET /api/v2/search?q=` → `data.movies[0].id`; detail `GET /api/v4/movies/{id}?from_rankings=false` → `data.movie` with `origin_title` (Japanese original), `title` (Chinese title), `score` (5-point string, measured `"4.43"`), `duration`, `release_date`, `maker_name`, `director_name`, `series_name`, `tags[]`, `actors[]` (**with a `gender` field** — measured `SSIS-001` includes a male actor `平田司 gender:1`), `preview_images[]`, `thumb_url`/`cover_url`. Called "字段最广（15 项）" in §5. | §2 bullet 2, §3, §5 table |
| **javbus** (direct HTML, one request) | 标题, `div#avatar-waterfall a.avatar-box` (cast), `a.bigImage img` (cover), `span.genre label a` (tags), `div.info p` info lines (studio / series / release date / runtime / director / publisher). Sample images are **absolute dmm CDN URLs** — unreachable without a Japanese exit, so "没有日本出口时，javbus 只能拿到封面与头像，拿不到 extrafanart". Cover and portraits are **relative** and the image CDN requires `Referer` (403 without). | §1, §2 bullet 1, §3 |
| **fc2** (`adult.contents.fc2.com`) | direct HTML, `GET /article/{id}/`; id = trailing digit run. Cover/thumb are protocol-relative `//contents-thumbnail2.fc2.com/...`, plain JPEG, no Referer. Miss page is **HTTP 200** with `items_notfound_wp`, so "found" needs a positive marker. | §2 bullet 4, §3, §5 |
| **dmm** | "字段最全最准" but **every endpoint is unreachable from this egress** (main site, `api.video.dmm.co.jp/graphql`, and all image CDNs `pics.dmm.co.jp` / `awsimgsrc.dmm.co.jp` / `cc3001.dmm.co.jp` — all TLS-handshake EOF, reproduced twice). Its content-id has two zero-padding spellings (`ssis00001` / `ssis001NN`). **This section is entirely OLD-repo conclusion, not re-measured.** | §1, §2 bullet 3, §5, §6.1 |
| **avbase** | reachable page-level; data lives in `__NEXT_DATA__` (`pageProps.works[]` on `/works?q=`, `pageProps.work` on the detail). Detail address needs the prefix (`/works/idp:IPX-535`; `/works/IPX-535` → 404). **All its images point at dmm CDNs** (poster = `awsimgsrc.dmm.co.jp/...ps.jpg`, landscape = `...pl.jpg`, samples = `pics.dmm.co.jp`, trailer = `cc3001.dmm.co.jp`), so "接了页面、拿不到图"; only `pic.duga.jp` (200) is reachable. | §1, §2 bullet 5, §3 |
| **airav** (4th candidate) | search `GET /search_result?kw=` → `div.col.oneVideo` cards → `a[href^="/video?hid="]`; detail `GET /video?hid={id}`. **Its cover bytes are byte-identical to javbus's cover** (`cmp` on 188 849 B), so its unique value is only the Chinese title/description. | §3, §5 |

Cross-check against OLD's own capability declarations (`Fields()`), which are the OLD
repo's claim of what each source can serve:

| Source | OLD `Fields()` declaration | Citation |
|---|---|---|
| javdb | 15: title, original_title, outline, cover, poster, extra_fanart, tags, user_rating, actors, studio, series, release_date, runtime, director, publisher (no `trailer`) | `scrape/javdb.go` (`javdbFields()`) |
| dmm | 15: title, outline, cover, poster, extra_fanart, tags, user_rating, actors, studio, series, release_date, runtime, **trailer**, director, publisher (no `original_title`) | `scrape/dmm.go` (`dmmFields()`) |
| avbase | 14: title, outline, cover, poster, extra_fanart, tags, actors, studio, series, release_date, runtime, trailer, director, publisher | `scrape/avbase.go` (`avbaseFields()`) |
| fc2 | 8: title, outline, cover, extra_fanart, tags, studio, release_date, runtime | `scrape/fc2.go` (`fc2Fields()`) |
| javbus | 10: title, cover, tags, actors, studio, series, release_date, runtime, director, publisher | `scrape/sources_builtin/javbus.yaml` (`fields:` block) |
| airav | 6: title, outline, cover, tags, actors, release_date — with an in-file comment recording that runtime/studio/series/publisher/director were **removed** because "the detail page carries no runtime, studio, series, publisher or director at all" | `scrape/sources_builtin/airav.yaml` (`fields:` block + header comment) |

Related: rating scale. The javdb `score` (5-point) and dmm `reviewSummary.average`
(5-point) are both multiplied by 2 at the source via `ratingFromFivePoint`
(`scrape/field.go:279`; `docs/adr/0016-user-rating-is-a-ten-point-value.md`), and the
source-landscape doc repeats this in its §6.9 note. Also measured: javdb's site CDN
`c0.jdbstatic.com` took 19.06 s for one 157 KB cover, and the APP CDN's images are
`key ‖ (jpeg XOR key)` with a per-image key (decoding reproduces the site-CDN bytes
exactly, and the APP CDN returns **no** `Content-Type`).

---

## 10. Explicitly UNVERIFIED

1. The exact struct boundary of ORIG's `0x2b54848 [68]` and `0x2b4e838 [68]` runs —
   adjacent serde arrays merge in memory (`8369f4d:recovered/backend/api/DTO_FIELDS.md:5`).
   The name lists are verbatim; the grouping is my count.
2. ORIG's NFO **writer** behaviour: element names for the 19 non-UI switches, whether
   `<outline>`/`<uniqueid>`/`<id>`/`<language>`/`<countrycode>` were emitted at all, and how
   empty values were skipped. No writer code exists in the record.
3. ORIG's merge control flow (which provider's value wins, when `by_fields` runs relative to
   the category chain, what happens on an empty value) — `.scratch/open-mdcng/research/06-re-artifacts.md:50`
   lists this as a gap; the recorded chain order is an inference from UI/config, not from
   decompiled control flow.
4. ORIG's 13 embedded YAML source definitions (selectors, URL templates) — never recovered.
5. ORIG's parser behaviour (regex application order, multi-candidate arbitration).
6. Whether `metadata_all_sites` is read by any NFO path in ORIG.
7. Whether OLD's `dedupe` treats `" a"` and `"a"` as duplicates (it compares untrimmed).
8. All Jellyfin/Emby behaviour in the NEW project's `nfo-templates` artifact is
   **source-and-docs level, not machine-verified** (the artifact says so in §6.1); Emby
   conclusions are pinned to its last public commit (2024-03-27).
9. dmm's fields in the source-landscape artifact are inherited from OLD, not re-measured
   (`source-landscape.md` §6.1).
10. Rate limits / cooldowns for javbus, javdb API, avbase, airav — deliberately not probed
    (`source-landscape.md` §6.2). The OLD note "javdb bans search by IP" was neither
    reproduced nor falsified; the OLD note "javdb needs a UA or it 403s with error 1010" was
    **falsified** at this egress (no UA → 200) (`source-landscape.md` §3, §6.3).
11. The NEW README's field draft itself has no code; every statement about it here is a
    quotation of `/root/clone/mdc-lite/README.md` (the 「字段」 section), not of an
    implementation.
