package acceptance

import "testing"

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

// The two roots answer two different questions, so a case belongs to exactly one:
// the library root's task count is the story being asserted against Jellyfin, and a
// counterexample planted there would be a release the media server is asked about.
func TestTheCorpusUsesBothRootsAndOnlyThose(t *testing.T) {
	roots := map[CorpusRoot]int{}
	for _, file := range corpus {
		switch file.Root {
		case RootLibrary, RootCounterexamples:
			roots[file.Root]++
		default:
			t.Errorf("%s/%s: unknown corpus root %q", file.Root, file.Name, file.Root)
		}
		if file.Name == "" || file.Dir == "" {
			t.Errorf("%+v: a corpus file needs a directory and a name", file)
		}
	}
	for _, root := range []CorpusRoot{RootLibrary, RootCounterexamples} {
		if roots[root] == 0 {
			t.Errorf("corpus root %q has no files", root)
		}
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
