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
}

// corpus is the acceptance corpus, as a table.
//
// The library half is four releases a real library holds, each named the way the
// owner's files are named, and each with a captured page behind it, so the chain
// runs for real: group → parse → scrape → images → nfo → output → Jellyfin.
//
// The counterexample half is the spec's own do-not-merge and must-merge cases; the
// numbers there are not captured, so those tasks end with a source failure, which
// is fine: the assertion is about how the files were grouped, and a scrape that
// found nothing does not blur that.
var corpus = []CorpusFile{
	// --- the main chain -----------------------------------------------------------
	{RootLibrary, "CAWD-279", "CAWD-279.mp4", "the simplest case there is: one file, one number"},
	{RootLibrary, "MIDA-557-C", "MIDA-557-C.mp4", "a real `-C` directory: the flag is not part of the number"},
	{RootLibrary, "MIDV-804.2025.1080p.DMM.WEB-DL.AAC2.0.HFR.H.264-MTeam",
		"MIDV-804.2025.1080p.DMM.WEB-DL.AAC2.0.HFR.H.264-MTeam.mkv",
		"a real scene-named directory: the number is inside the noise"},
	{RootLibrary, "SSIS-001", "SSIS-001-cd1.mp4", "the first disc of a two-disc release"},
	{RootLibrary, "SSIS-001", "SSIS-001-cd2.mp4", "the second disc, which must aggregate into one entry"},

	// --- grouping counterexamples -------------------------------------------------
	{RootCounterexamples, "ABP-647", "ABP-647.mp4", "a neighbour pair: ABP-647 must not absorb ABP-648"},
	{RootCounterexamples, "ABP-647", "ABP-648.mp4", "the other neighbour, with no marker to justify merging"},
	{RootCounterexamples, "ABC-123", "ABC-123.mp4", "a duplicate stem, first copy"},
	{RootCounterexamples, "ABC-123", "ABC-123-U.mp4", "the leaked copy of the same stem: duplicate_stem, not a merge"},
	{RootCounterexamples, "SSVR-122-parts", "SSVR-122-cd1.mp4", "two discs whose stems differ by one digit, both explicitly marked"},
	{RootCounterexamples, "SSVR-122-parts", "SSVR-123-cd2.mp4", "…and the second one, which must merge anyway"},
	{RootCounterexamples, "ABC-456", "ABC-123-cd1.mp4", "explicitly marked but three digits away from its sibling"},
	{RootCounterexamples, "ABC-456", "ABC-456-cd2.mp4", "…the sibling it must not merge with, only warn about"},
	{RootCounterexamples, "ABC-124", "ABC-124-1.mp4", "a lone trailing digit is not a part marker: one file, one task"},
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
		case FromLibrary, FromBoundary:
		default:
			return nil, fmt.Errorf("%s:%d: %q is not a provenance (library / boundary)", path, line, provenance)
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
