# Acceptance

`make accept` is the whole environment, from nothing, in one command: a real
**Jellyfin** in Docker (pinned version, overridable), the scrape sources **replayed
from the committed captures**, the **real binary built from this checkout** and run on
the host, and assertions that land on what the media server read — not on whether the
pipeline thinks it wrote something.

It takes **about 2 minutes** here (100–110 s for the assertions; the first run also
downloads the Jellyfin image and the Chromium the interface arm drives, and takes about
15 s of container start). The self-check runs the gate twice, green then red:
**about 3.5 minutes**.

```sh
make accept          # the gate
make accept-falsify  # prove the gate can fail, and for the right reason
make accept-down     # stop the container and keep the scratch root for inspection
make accept-clean    # stop it and drop the scratch root
```

Requirements: **Go**, **Docker with the compose v2 plugin**, **Node/npm** (the run
builds the frontend the binary embeds), **python3** for the self-check (it matches one
exact snippet of the product's source, so a reformat of that block fails loudly instead
of silently breaking nothing), and **the ability to mount a tmpfs** — as root or with
passwordless sudo for `mount`/`umount`, which the cross-device assertion needs, and
which a GitHub `ubuntu-latest` runner has. The first run also downloads Chromium (about
120 MiB into `~/.cache/ms-playwright`), because the interface arm drives a real browser.
Nothing talks to a real site.

## What it asserts

The nine arms of the spec, plus the hard requirements the old repository learned the
hard way. Every assertion waits for **the value it expects** (an item with the NFO's
title and date, a task that left `queued`/`running`, a temp file that appeared) and
never for a duration: the one exception is the killed-write arm's 1 ms poll, which is
waiting *faster*, not slower. Nothing counts entries in the library either: the
environment plants a primer of its own, and a server that indexed something else is not
a failure.

| | the claim |
|---|---|
| ① | the main chain runs (corpus → group → parse → scrape → images → nfo → output) and the media server reads the NFO: each release's item name and première date are the NFO's, and its path is under the output root — the same absolute path inside and outside the container |
| ② | a two-disc release is **one entry** in the library, carrying the second disc as an additional part; its `extrafanart/` directory is beside the `movie.nfo`, `IsInMixedFolder` is false, and each still became a backdrop |
| ③ | a hardlink from the corpus filesystem to the **environment's own tmpfs** (two real mount points, two device ids) fails the task with the message that names `copy|move` — and the failed write leaves no product behind |
| ④ | a rerun of an unchanged release **skips every write and keeps every mtime**; a rerun that gets one still fewer **deletes exactly that still** from the previous run's ledger, and the deletion is on the record; a copy **killed mid-write** leaves no file under the product's name, and the process's own crash recovery finishes it on restart — no `.tmp` left behind |
| ⑤ | the multi-disc basenames are `{number}-cd{part}`, and no bare `{number}.mp4` was written beside them |
| ⑥ | the grouping counterexamples stay counterexamples: `ABP-647`/`ABP-648` do not merge, `SSVR-122-cd1`+`SSVR-123-cd2` do, a pair three digits apart does not, a duplicate stem goes to a human with `duplicate_stem`, and a lone trailing digit is not a part |
| ⑦ | the three source states: javbus's captured **404** and fc2's captured **200 miss page** are both recorded `notfound` with no reason code and no fields; and the field-level fallback gives `title` to javdb and `publisher` (which javdb does not have) to javbus on the same release |
| ⑧ | `step_runs` is assertable (types in order, positions in the pipeline file, summaries) and `source_runs` carries **all six reason codes** — `http_5xx`, `timeout`, `blocked`, `cooldown`, `parse`, `egress_unavailable` — each produced by the replay answering that way, never by a simulated branch in the product |
| ⑨ | a real Chromium renders the **scan view** (directory deck over this run's own filesystem), the **task list** (the run's own rows with a status), the **detail's four tabs**, and the **needs-number panel** fills a number through the UI, submits, and the task really leaves the queue — no axe, no visual regression |
| — | **both terminal states are really reached**: `SSIS-001` ends `succeeded` (every source-level field arrived) and the javbus-only releases end `partial`, asserted against the metadata they report |
| — | the media server is **the pinned version**, and the run refuses a server that is not — an upgrade is an explicit edit to `compose.yaml`, and assertion ② re-measures the layout on every run |
| — | every image request the run made answered **200** (a non-200 image is fatal, not a quiet fallback); the one exception is the still an arm deliberately withdraws |

A run leaves a **positive marker** (`acceptance/scratch/ran.json` plus an
`ACCEPTANCE RAN: …` line) only when every arm passed — deliberately, because "the marker
is there" has to mean "the gate passed", not "the gate started". A red run says
`ACCEPTANCE FAILED` and writes no marker, and the self-check asserts it does not.

## The two questions this run had to settle

**Why the narrowed `required_fields` does not manufacture a `succeeded` task.** The
parent ticket expected it to; in this product it cannot, and the acceptance run pins
that down rather than pretending otherwise. `required_fields` decides **failed or not**
at the output gate, while `Missing` — all 14 source-level fields — decides **partial or
succeeded** (`Partial = len(Missing) > 0`, spec §5). The `acceptance-lenient` pipeline
narrows the gate to `["title"]` and runs `IPX-535`, a release only javbus has a capture
for: the gate passes, the release is written, and the task is still `partial`, because
plot/rating/extrafanarts/poster genuinely did not arrive. The `succeeded` terminal state
is reached the real way instead — `SSIS-001`, where javdb's thirteen fields and javbus's
`publisher` add up to all fourteen.

**Why the reason codes are scripted routes and not product branches.** A reason code is
the product's *reading of an answer*, so the answer has to be real. The replay gains a
handful of one-shot routes (see `acceptance/replay.go`): `FAIL-500` gets a 500, `FAIL-501`
an answer held back past the source's own 1s request timeout, `FAIL-409` an outright
refusal, `FAIL-503` a punishment (with javdb's cooldown dialed down to 1 ms so the limiter
does not punish the other numbers for what one arm did), `FAIL-200` the bytes of a real
captured HTML page where javdb expects JSON (`parse`), and `FAIL-902` a connection torn
down before any answer (`egress_unavailable`). The numbers are invented and the corpus
ledger says so (`synthetic` provenance); nothing but the replayed answer ever reaches
the product.

## How the replay works

A captured page names its cover with an absolute `https://` address, and the product
resolves that address off the page it just read — so the replay cannot be a rewritten
base URL, and the page's bytes cannot be edited to point somewhere else (a fixture that
has been edited is no longer evidence). The replay therefore sits where the network is:

```
product ── proxy ──▶ TLS terminated with a CA minted per run ──▶ committed capture / generated JPEG
```

`acceptance/config/config.yaml` sets `proxy:` to the replay's address, and the server
under test trusts the run's CA through `SSL_CERT_FILE`. The product is unchanged: it
runs its real selectors, its real age-gate cookie, its real image `Referer`.

The captures are the **same files the unit tests read** —
`internal/source/<id>/testdata/captures/` — so the two fixture sets cannot drift apart.
A request with no capture answers 404, which is how a missing fixture becomes visible
instead of being papered over with an invented answer. Images are generated rather than
captured (a 16:9 JPEG derived from the address), because an image is not a page a parser
reads; what matters is that the bytes arrive and what the product and the server do with
them.

## The corpus

`acceptance/corpus/names.tsv` is the ledger: every directory the run materialises is
listed there as **library** (copied verbatim from the owner's media library — the listing
is the old repository's `acceptance/names-all.txt`), **boundary** (written by hand for a
rule's counterexample), or **synthetic** (written by hand for one scenario of the
environment itself — a source answer to classify, another filesystem to write across, a
write to interrupt). `go test ./acceptance/` checks both directions, so an invented name
cannot pass as a real one and a ledger entry that nothing uses is caught.
`acceptance/corpus.go` is the table of files.

Two settings in `acceptance/config/pipelines/acceptance.yaml` exist because of the corpus
and would be wrong for a real library: `min_size: 0` (the corpus is one-second generated
clips; a size floor would throw the whole corpus away and report "no tasks, everything
fine") and a raised per-source `rate` (the replay answers instantly, so the 1 req/s
default would only add waiting). Three more pipelines sit beside it, one per scenario:
`acceptance-lenient` (the narrowed gate), `acceptance-exdev` (the output root on the
second filesystem), `acceptance-copy` (the interrupted write).

## The scratch root

Everything the run produces lives in `acceptance/scratch/` (gitignored, and overridable
with `MDC_ACCEPT_ROOT`). It must not start with a dot: a dot-leading path is exactly the
shape the segment sanitiser and the scan's filter rules treat specially, and an
environment that leans on a special case proves nothing. The test refuses one.

The scratch root is bind-mounted into the Jellyfin container **at the identical absolute
path**, because the library path is handed to the server verbatim and the server answers
`204` for a path it has never seen — a different mount point would look like a working
scan while nothing was scanned. The container runs as the host's uid/gid
(`MDC_ACCEPT_UID`/`MDC_ACCEPT_GID`, resolved by the test) so the next reset can delete
what it wrote. The library holds a **primer** directory before the first scan, because an
empty library registered and scanned is the case where a refresh is silently dropped.

`acceptance/scratch/exdev-fs/` is the one thing under the scratch root that is not on
the scratch root's filesystem: a tmpfs the run mounts itself (root or passwordless sudo)
and unmounts on teardown, including one a killed run left behind. The cross-device arm
asserts the two device ids differ before it asserts anything about the product.

## Refresh a capture (the only path to a real site)

Never inside this environment, and never in CI. A capture is refreshed by hand with the
program's own Fetcher, because a `curl` is refused by some of these sites' edges:

```sh
go run ./cmd/mdc source test --config examples/config.yaml --pipelines-dir examples/pipelines \
  --id javbus --number CAWD-279 --dump
```

That writes the bytes verbatim into `internal/source/javbus/testdata/captures/` and adds
the manifest row (`url / status / bytes / captured / sha256`). Then run
`go test ./internal/source/... ./internal/fixtures/` — the manifest is what proves the
bytes are still the bytes the site sent, and `internal/fixtures` is what refuses a
constructed fixture that does not say why it exists.

## The interface arm, specifically

`web/e2e/interface.spec.ts` runs under Playwright's Chromium against the running product
(not jsdom, not a mock): the pull-request gate keeps the frontend's vitest suite, and the
browser is spent only here, once. The run installs Chromium itself the first time
(`npx playwright install chromium`, cached in `~/.cache/ms-playwright`); if the system is
missing libraries, Playwright's own error names the command that installs them
(`npx playwright install-deps chromium`). The spec is driven by the acceptance test
through `MDC_E2E_*` variables — the ids, numbers and paths the arms already found over
the API — so the browser never guesses which row is which, and the arm asserts *after*
the browser's submission that the server really took the numbers and the task really
left the queue.

## Known gaps

- **No library refresh in the product.** The acceptance environment triggers the media
  server's own scan; the product's `refresh` step is batch two.
- **The needs-number arm needs the counterexample batch.** The interface spec's one
  write path fills the duplicate-stem task the counterexample batch leaves behind; the
  arm would need its own corpus entry to fill a `no_candidate` task instead.
- **Emby is not asserted.** One server is enough to prove the layout reads; a second one
  would be a second truth to keep current. An optional compose profile (`--profile emby`,
  `emby/embyserver` pinned) exists for a manual re-check, and the gate never starts it.
- **`-cd{n}` aggregates as additional parts, not as several media sources.** Measured on
  10.10.7 (`/Videos/{id}/AdditionalParts`): the release is one entry, its `MediaSources`
  holds the primary disc, and the second disc is an `AdditionalPart` of the same entry.
  That is the aggregation claim the environment asserts; "two media sources" would be a
  different layout (versions separated by ` - `, which is what Jellyfin's own naming
  tests use).
- **The self-check breaks one thing.** It proves the gate can go red by breaking the
  part numbering; it does not try to break every arm.
- **The corpus's counterexample tasks end without output.** Their numbers have no
  capture, so they stop at the required-fields gate — which the gate assertions then
  read, so the failure is asserted rather than merely tolerated.
- **The interrupted-write corpus file is 256 MiB.** It is the one corpus file that is not
  a one-second generated clip (sparse on disk, never read by a media server); the copy
  has to last long enough that a 1 ms poll can catch it. If a machine finishes the copy
  faster than the poll can see the temp file, the arm says so instead of passing quietly.
- **A killed product's sockets are not asserted.** The killed-write arm kills the server
  with SIGKILL and restarts it; it does not assert what happens to a request that was in
  flight — the browser arm runs before it, and nothing runs after it.
