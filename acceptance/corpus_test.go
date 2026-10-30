package acceptance

import (
	"strings"
	"testing"
)

// Every directory the acceptance run materialises has to be accounted for: either a
// name copied from the owner's library, or a declared counterexample. Without this,
// "the corpus is real names" is a claim in a comment, and the first invented name
// would be indistinguishable from a captured one.
func TestEveryCorpusDirectoryIsAccountedFor(t *testing.T) {
	ledger, err := loadProvenance(provenancePath())
	if err != nil {
		t.Fatalf("read the name ledger: %v", err)
	}

	seen := map[string]bool{}
	for _, file := range corpus {
		seen[file.Dir] = true
		if _, ok := ledger[file.Dir]; !ok {
			t.Errorf("%s/%s: the directory %q is not in corpus/names.tsv, so nothing says where the name came from",
				file.Root, file.Name, file.Dir)
		}
	}
	if len(seen) == 0 {
		t.Fatal("the acceptance corpus is empty")
	}
}

// Each root answers one question, so a case belongs to exactly one of them: the
// library root is the release the media server reads, the counterexample root is the
// grouping rules, and the rest each drive one scenario of their own. Planting a
// counterexample in the library root, for instance, would be a release the media
// server is asked about.
func TestTheCorpusUsesEveryRootAndOnlyThose(t *testing.T) {
	all := []CorpusRoot{
		RootLibrary, RootCounterexamples, RootSuccess, RootSources, RootExdev, RootSlow,
	}
	known := map[CorpusRoot]bool{}
	for _, root := range all {
		known[root] = true
	}
	roots := map[CorpusRoot]int{}
	for _, file := range corpus {
		if !known[file.Root] {
			t.Errorf("%s/%s: unknown corpus root %q", file.Root, file.Name, file.Root)
		}
		roots[file.Root]++
		if file.Name == "" || file.Dir == "" {
			t.Errorf("%+v: a corpus file needs a directory and a name", file)
		}
		if strings.TrimSpace(file.Note) == "" {
			t.Errorf("%s/%s: a corpus file has to say what case it is", file.Root, file.Name)
		}
	}
	for _, root := range all {
		if roots[root] == 0 {
			t.Errorf("corpus root %q has no files", root)
		}
	}
}

// The corpus is one-second clips because the media server probes every file it is
// asked to read. The one exception is the interrupted-write release, which exists to
// be copied for long enough to be caught: if a second large file appears, the
// exception has stopped being an exception.
func TestOnlyTheInterruptedWriteReleaseIsLarge(t *testing.T) {
	var large []string
	for _, file := range corpus {
		if file.Size <= 0 {
			continue
		}
		large = append(large, string(file.Root)+"/"+file.Name)
		if file.Root != RootSlow {
			t.Errorf("%s/%s: a large file belongs to the interrupted-write root, nothing else", file.Root, file.Name)
		}
	}
	if len(large) != 1 {
		t.Errorf("large corpus files = %v, want exactly the one the copy is caught on", large)
	}
}

// The multi-disc case is what pins `{number}-cd{part}` and the media server's
// aggregation assertion, so it has to be two files of one number in one directory;
// splitting it would quietly turn both assertions into single-file ones.
func TestTheMultiDiscCaseIsTwoDiscsOfOneNumber(t *testing.T) {
	var discs []string
	for _, file := range corpus {
		if file.Root == RootLibrary && file.Dir == "SSIS-001" {
			discs = append(discs, file.Name)
		}
	}
	if len(discs) != 2 {
		t.Fatalf("SSIS-001 carries %v, want exactly two discs", discs)
	}
	for _, want := range []string{"SSIS-001-cd1.mp4", "SSIS-001-cd2.mp4"} {
		found := false
		for _, name := range discs {
			if name == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is missing: the corpus must carry both discs", want)
		}
	}
}

// A name in the ledger that nothing materialises is dead weight in a file whose whole
// job is provenance; it is also how a sample drifts into being a wish list.
func TestTheLedgerHasNoNamesNothingMaterialises(t *testing.T) {
	ledger, err := loadProvenance(provenancePath())
	if err != nil {
		t.Fatalf("read the name ledger: %v", err)
	}
	used := map[string]bool{}
	for _, file := range corpus {
		used[file.Dir] = true
	}
	for name := range ledger {
		if !used[name] {
			t.Errorf("corpus/names.tsv records %q, which no corpus file uses (the ledger is not a sample gallery)", name)
		}
	}
}
