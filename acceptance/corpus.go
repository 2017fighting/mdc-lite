// Package acceptance is the one-command acceptance environment: a real Jellyfin in
// Docker, the scrape sources replayed from committed captures, the real binary built
// from this checkout and run on the host, and assertions that land on what the media
// server read.
//
// The package is split in two so the cheap half runs in the pull-request gate: the
// corpus table and its provenance are plain Go (this file), and everything that
// needs Docker, a thousand megabytes of image or a running server is behind the
// `acceptance` build tag.
package acceptance

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Provenance is where a corpus directory name came from. It is a closed set: a name
// is either copied from the owner's library or written by hand as a counterexample,
// and the difference is what keeps "this is what real files look like" honest.
type Provenance string

const (
	// FromLibrary is a name copied verbatim from the owner's media library.
	FromLibrary Provenance = "library"
	// FromBoundary is a name written by hand for one rule's counterexample.
	FromBoundary Provenance = "boundary"
	// FromSynthetic is a directory written by hand for one scenario of the
	// environment itself — a source that answers in a specific way, an output root on
	// another filesystem, a write that is interrupted. The number may be a real one
	// (a captured page's) or invented; what the provenance says is that the directory
	// is not evidence of what a library looks like, so nobody reads it as that.
	FromSynthetic Provenance = "synthetic"
)

// CorpusRoot names one of the two traffic sets the acceptance run drives. They are
// separate on purpose: the library root is the story "a real release lands in
// Jellyfin", and the counterexample root is the story "these files must not become
// one release". Mixing them would make one batch answer two questions.
type CorpusRoot string

const (
	// RootLibrary is the corpus the main chain and the media server read.
	RootLibrary CorpusRoot = "library-in"
	// RootCounterexamples is the corpus of grouping counterexamples.
	RootCounterexamples CorpusRoot = "counterexamples"
	// RootSuccess is the release the narrowed pipeline runs over. It answers the
	// question "did the task really end `succeeded`" and, by ending `partial` anyway,
	// "is the required-fields gate what decides that" (it is not, and the README says
	// so).
	RootSuccess CorpusRoot = "success-in"
	// RootSources holds one directory per source answer the run has to classify: the
	// site's own not-found markers and the six reason codes.
	RootSources CorpusRoot = "sources-in"
	// RootExdev is the release whose output root sits on the environment's second
	// filesystem, so a hardlink really is EXDEV.
	RootExdev CorpusRoot = "exdev-in"
	// RootSlow is the release whose video is large enough that a `copy` takes long
	// enough to be caught in the middle. Nothing reads it but the copy.
	RootSlow CorpusRoot = "slow-in"
)

// CorpusFile is one file the acceptance run puts on disk. The bytes are generated,
// not committed: a one-second clip per file, made in the same pinned Jellyfin image
// the assertions read afterwards, because a zero-byte file would make Jellyfin fail
// the probe and silently turn off the paths this environment exists to exercise.
type CorpusFile struct {
	Root CorpusRoot
	// Dir is the directory name inside the corpus root. It is what the provenance
	// table is keyed by, so it has to be a real name or a declared counterexample.
	Dir string
	// Name is the file name. It carries the noise, the part marker or the flag the
	// case is about.
	Name string
	// Note says what the case is for, in the words of the rule it pins down.
	Note string
	// Size is the file's size in bytes; zero means the standard one-second clip. The
	// only non-zero case is the interrupted-write release: its video has to take long
	// enough to copy that the run can be killed in the middle of it, which no
	// one-second clip does. It is generated (sparse), not committed, and no media
	// server ever reads it.
	Size int64
}

// largeCopyBytes is the interrupted-write release's size: big enough that the copy is
// still in flight when the run kills the process (the arm polls for the temp file every
// millisecond), small enough that a run does not spend seconds on it.
const largeCopyBytes = 256 << 20

// corpus is the acceptance corpus, as a table.
//
// The library half is four releases a real library holds, each named the way the
// owner's files are named, and each with a captured page behind it, so the chain
// runs for real: group → parse → scrape → images → nfo → output → Jellyfin.
//
// The counterexample half is the spec's own do-not-merge and must-merge cases; the
// numbers there are not captured, so those tasks end at the `output` step's required
// fields (no title, no cover), which is fine: the assertion is about how the files were
// grouped into tasks, and a task that found no metadata does not blur that.
// abpDir is the one library directory name that carries brackets and full-width
// characters as well as the number. It is the neighbour-pair case's directory: a real
// name that the segment sanitiser and the number parser both have to survive.
const abpDir = "ABP-647 绝对的铁板濑名光莉[高清中文字幕]"

var corpus = []CorpusFile{
	// --- the main chain -----------------------------------------------------------
	{Root: RootLibrary, Dir: "CAWD-279", Name: "CAWD-279.mp4",
		Note: "the simplest case there is: one file, one number"},
	{Root: RootLibrary, Dir: "MIDA-557-C", Name: "MIDA-557-C.mp4",
		Note: "a real `-C` directory: the flag is not part of the number"},
	{Root: RootLibrary, Dir: "MIDV-804.2025.1080p.DMM.WEB-DL.AAC2.0.HFR.H.264-MTeam",
		Name: "MIDV-804.2025.1080p.DMM.WEB-DL.AAC2.0.HFR.H.264-MTeam.mkv",
		Note: "a real scene-named directory: the number is inside the noise"},
	{Root: RootLibrary, Dir: "SSIS-001", Name: "SSIS-001-cd1.mp4",
		Note: "the first disc of a two-disc release"},
	{Root: RootLibrary, Dir: "SSIS-001", Name: "SSIS-001-cd2.mp4",
		Note: "the second disc, which must aggregate into one entry"},

	// --- grouping counterexamples -------------------------------------------------
	{Root: RootCounterexamples, Dir: abpDir, Name: "ABP-647.mp4",
		Note: "a neighbour pair: ABP-647 must not absorb ABP-648"},
	{Root: RootCounterexamples, Dir: abpDir, Name: "ABP-648.mp4",
		Note: "the other neighbour, with no marker to justify merging"},
	{Root: RootCounterexamples, Dir: "ABC-123", Name: "ABC-123.mp4",
		Note: "a duplicate stem, first copy"},
	{Root: RootCounterexamples, Dir: "ABC-123", Name: "ABC-123-U.mp4",
		Note: "the leaked copy of the same stem: duplicate_stem, not a merge"},
	{Root: RootCounterexamples, Dir: "SSVR-122-parts", Name: "SSVR-122-cd1.mp4",
		Note: "two discs whose stems differ by one digit, both explicitly marked"},
	{Root: RootCounterexamples, Dir: "SSVR-122-parts", Name: "SSVR-123-cd2.mp4",
		Note: "…and the second one, which must merge anyway"},
	{Root: RootCounterexamples, Dir: "ABC-456", Name: "ABC-123-cd1.mp4",
		Note: "explicitly marked but three digits away from its sibling"},
	{Root: RootCounterexamples, Dir: "ABC-456", Name: "ABC-456-cd2.mp4",
		Note: "…the sibling it must not merge with, only warn about"},
	{Root: RootCounterexamples, Dir: "ABC-124", Name: "ABC-124-1.mp4",
		Note: "a lone trailing digit is not a part marker: one file, one task"},

	// --- the narrowed pipeline ----------------------------------------------------
	// IPX-535 is a captured javbus page and nothing else: title and cover arrive,
	// plot, rating, extrafanarts and poster do not. Its task ends `partial` even
	// though the pipeline it runs requires only `title`, which is the point — the
	// required-fields gate decides failed-or-not, and `Missing` (all 14 fields)
	// decides partial-or-succeeded.
	{Root: RootSuccess, Dir: "IPX-535", Name: "IPX-535.mp4",
		Note: "a release only javbus covers: enough for a narrowed gate, not for `succeeded`"},

	// --- the three states and the six reason codes --------------------------------
	// One directory per answer the run has to classify. The numbers are invented (no
	// site is asked about them through anything but the replayed answer); what each
	// directory pins down is the product's reading of that answer.
	{Root: RootSources, Dir: "ZZZZ-9999", Name: "ZZZZ-9999.mp4",
		Note: "javbus answered 404: the captured page is the site's own \"no such work\" marker, so the source is `notfound`, not `failed`"},
	{Root: RootSources, Dir: "FC2-PPV-1234567", Name: "FC2-PPV-1234567.mp4",
		Note: "fc2 answered 200 with `div.items_notfound_wp`: the miss page is a positive marker, so the source is `notfound` on a 200"},
	{Root: RootSources, Dir: "FAIL-500", Name: "FAIL-500.mp4",
		Note: "the site answered 500: reason `http_5xx`, after the transport's two retries"},
	{Root: RootSources, Dir: "FAIL-501", Name: "FAIL-501.mp4",
		Note: "the site did not answer inside the per-request timeout: reason `timeout`"},
	{Root: RootSources, Dir: "FAIL-409", Name: "FAIL-409.mp4",
		Note: "the site refused the request outright (410): reason `blocked`"},
	{Root: RootSources, Dir: "FAIL-503", Name: "FAIL-503.mp4",
		Note: "the site punished the client (503): reason `cooldown`"},
	{Root: RootSources, Dir: "FAIL-200", Name: "FAIL-200.mp4",
		Note: "the site answered 200 with something the parser cannot read: reason `parse`"},
	{Root: RootSources, Dir: "FAIL-902", Name: "FAIL-902.mp4",
		Note: "the connection died before an answer: reason `egress_unavailable`"},

	// --- the other filesystem ------------------------------------------------------
	// The same captured release as the main chain, in its own directory: the output
	// root of `acceptance-exdev` lives on the environment's second mount, so the
	// hardlink the pipeline wants is a real EXDEV and the task must fail naming the fix.
	{Root: RootExdev, Dir: "CAWD-279", Name: "CAWD-279.mp4",
		Note: "a release with metadata, so the run reaches the write instead of failing at the gate"},

	// --- the interrupted write -----------------------------------------------------
	// Also a captured release, and deliberately large: the copy has to last long
	// enough that the run can be killed inside it.
	{Root: RootSlow, Dir: "CAWD-279", Name: "CAWD-279.mp4",
		Note: "a large release, so a `copy` is catchable mid-write", Size: largeCopyBytes},
}

// loadProvenance reads the corpus's name ledger.
func loadProvenance(path string) (map[string]Provenance, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	out := map[string]Provenance{}
	scanner := bufio.NewScanner(f)
	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		if text == "" || strings.HasPrefix(text, "#") || !strings.Contains(text, "\t") {
			continue
		}
		parts := strings.SplitN(text, "\t", 2)
		name, provenance := parts[0], Provenance(strings.TrimSpace(parts[1]))
		switch provenance {
		case FromLibrary, FromBoundary, FromSynthetic:
		default:
			return nil, fmt.Errorf("%s:%d: %q is not a provenance (library / boundary / synthetic)", path, line, provenance)
		}
		if name == "" {
			return nil, fmt.Errorf("%s:%d: a row has no name", path, line)
		}
		out[name] = provenance
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// provenancePath is where the ledger lives, relative to this package.
func provenancePath() string { return filepath.Join("corpus", "names.tsv") }
