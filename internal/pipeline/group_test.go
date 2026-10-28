package pipeline

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/2017fighting/mdc-lite/internal/config"
)

// groupFiles runs the group step over a set of file names in one directory. The
// files never touch the disk: grouping is a decision about names and directories,
// so making it need real files would only make the corpus slower to write.
func groupFiles(t *testing.T, w config.GroupWith, names ...string) *Groups {
	t.Helper()
	return groupFilesIn(t, w, "/media/a", names...)
}

func groupFilesIn(t *testing.T, w config.GroupWith, dir string, names ...string) *Groups {
	t.Helper()
	st := &State{Candidates: &Candidates{}}
	for _, name := range names {
		path := name
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, name)
		}
		st.Candidates.Files = append(st.Candidates.Files, Candidate{
			File: FileRef{
				Path:      path,
				Size:      1,
				SizeKnown: true,
				Ext:       strings.ToLower(filepath.Ext(path)),
				Kind:      KindUnknown,
			},
			Rule: "videos",
		})
	}
	step, err := newGroup(w)
	if err != nil {
		t.Fatalf("newGroup: %v", err)
	}
	if err := step.Run(context.Background(), st); err != nil {
		t.Fatalf("group: %v", err)
	}
	return st.Groups
}

func groupWith(mods ...func(*config.GroupWith)) config.GroupWith {
	w := config.GroupDefaults()
	for _, mod := range mods {
		mod(&w)
	}
	return w
}

// basenames renders a group's parts in order, which is what most assertions read.
func basenames(parts []Part) []string {
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = filepath.Base(p.File.Path)
	}
	return out
}

func wantParts(t *testing.T, g Group, want ...string) {
	t.Helper()
	got := basenames(g.Parts)
	if strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Fatalf("parts = %v, want %v", got, want)
	}
}

// TestGroup_FullWidthAndHalfWidthAreOneStem: the stem is where full-width folding
// happens, so two spellings of one work reach the duplicate rule as one stem
// instead of becoming two tasks with two output directories.
func TestGroup_FullWidthAndHalfWidthAreOneStem(t *testing.T) {
	g := groupFiles(t, groupWith(), "ABC-123.mp4", "ＡＢＣ－１２３.mp4")
	if len(g.Groups) != 0 || len(g.Duplicates) != 1 {
		t.Fatalf("groups = %d, duplicates = %d, want one duplicate stem", len(g.Groups), len(g.Duplicates))
	}
	if got := g.Duplicates[0].Stem; got != "ABC-123" {
		t.Errorf("stem = %q, want the folded ABC-123", got)
	}
}

func TestGroup_AdjacentNumbersStayApart(t *testing.T) {
	g := groupFiles(t, groupWith(), "ABP-123.mp4", "ABP-124.mp4")
	if len(g.Groups) != 2 {
		t.Fatalf("groups = %d, want 2 (ABP-123 and ABP-124 are different works)", len(g.Groups))
	}
	if len(g.Warnings) != 0 {
		t.Errorf("warnings = %d, want none: neither file claims to be a part", len(g.Warnings))
	}
	if len(g.Duplicates) != 0 {
		t.Errorf("duplicates = %d, want none: the stems differ", len(g.Duplicates))
	}
}

func TestGroup_MarkedStemsWithinDistanceMerge(t *testing.T) {
	g := groupFiles(t, groupWith(), "SSVR-122-cd1.mp4", "SSVR-123-cd2.mp4")
	if len(g.Groups) != 1 {
		t.Fatalf("groups = %d, want 1: both sides carry a marker and the stems are 1 apart", len(g.Groups))
	}
	grp := g.Groups[0]
	wantParts(t, grp, "SSVR-122-cd1.mp4", "SSVR-123-cd2.mp4")
	if filepath.Base(grp.Primary.Path) != "SSVR-122-cd1.mp4" {
		t.Errorf("primary = %s, want the lowest part number", grp.Primary.Path)
	}
	if grp.PrimaryStem != "SSVR-122" {
		t.Errorf("primary stem = %q, want the primary's own stem", grp.PrimaryStem)
	}
	if !grp.DistanceMerged {
		t.Error("the group exists because the stems are close, not because they are equal")
	}
}

func TestGroup_GluedLettersNeedASibling(t *testing.T) {
	g := groupFiles(t, groupWith(), "9ofje121A.mkv", "9ofje121B.mkv")
	if len(g.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(g.Groups))
	}
	grp := g.Groups[0]
	wantParts(t, grp, "9ofje121A.mkv", "9ofje121B.mkv")
	if grp.Primary != grp.Parts[0].File {
		t.Error("primary must be the first part")
	}
	for _, p := range grp.Parts {
		if p.Marker.Confident {
			t.Errorf("%s: a glued letter is conditional, not confident", p.File.Path)
		}
		if p.Marker.Index == 0 {
			t.Errorf("%s: the sibling's agreement gives it a part number", p.File.Path)
		}
	}
	if grp.Parts[0].Marker.Text != "A" || grp.Parts[1].Marker.Text != "B" {
		t.Errorf("markers = %q, %q", grp.Parts[0].Marker.Text, grp.Parts[1].Marker.Text)
	}
}

func TestGroup_ALoneTrailingNumberIsNotAPart(t *testing.T) {
	g := groupFiles(t, groupWith(), "ABC-123-1.mp4")
	if len(g.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(g.Groups))
	}
	grp := g.Groups[0]
	if grp.PrimaryStem != "ABC-123-1" {
		t.Errorf("stem = %q, want the whole name: a lone -1 belongs to the number", grp.PrimaryStem)
	}
	if grp.Parts[0].Marker.Text != "" {
		t.Errorf("marker = %+v, want none", grp.Parts[0].Marker)
	}
}

func TestGroup_LetterCIsNeverAPart(t *testing.T) {
	// A C copy is a duplicate version, not part 3, and a lone one is not a marker
	// at all.
	alone := groupFiles(t, groupWith(), "ABC-123-C.mp4")
	if len(alone.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(alone.Groups))
	}
	if m := alone.Groups[0].Parts[0].Marker; m.Text != "" {
		t.Errorf("marker = %+v, want none for a lone -C", m)
	}

	withParts := groupFiles(t, groupWith(), "ABC-123-A.mp4", "ABC-123-B.mp4", "ABC-123-C.mp4")
	if len(withParts.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(withParts.Groups))
	}
	for _, p := range withParts.Groups[0].Parts {
		if p.Marker.Index == 3 {
			t.Fatalf("%s is part 3; C is a chinese-subs copy, never part 3", p.File.Path)
		}
	}
}

func TestGroup_LettersAAndBAreParts(t *testing.T) {
	g := groupFiles(t, groupWith(), "ABC-123-A.mp4", "ABC-123-B.mp4")
	if len(g.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(g.Groups))
	}
	grp := g.Groups[0]
	wantParts(t, grp, "ABC-123-A.mp4", "ABC-123-B.mp4")
	if grp.Parts[0].Marker.Index != 1 || grp.Parts[1].Marker.Index != 2 {
		t.Errorf("indices = %d, %d, want 1, 2", grp.Parts[0].Marker.Index, grp.Parts[1].Marker.Index)
	}
	if !grp.Parts[0].Marker.Confident {
		t.Error("-A is in the confident table")
	}
}

func TestGroup_DuplicateStemNeedsAHuman(t *testing.T) {
	g := groupFiles(t, groupWith(), "ABC-123.mp4", "ABC-123-U.mp4")
	if len(g.Groups) != 0 {
		t.Fatalf("groups = %d, want 0: a copy is not a part", len(g.Groups))
	}
	if len(g.Duplicates) != 1 {
		t.Fatalf("duplicates = %d, want 1", len(g.Duplicates))
	}
	d := g.Duplicates[0]
	if d.Stem != "ABC-123" {
		t.Errorf("stem = %q", d.Stem)
	}
	if len(d.Files) != 2 {
		t.Errorf("files = %d, want both", len(d.Files))
	}
}

func TestGroup_WarnsAboutPartsThatDidNotMerge(t *testing.T) {
	// Both claim to be a part, but the stems are 3 apart: worth saying out loud,
	// because a typo in a number looks exactly like this.
	g := groupFiles(t, groupWith(), "ABC-123-cd1.mp4", "ABC-1999-cd2.mp4")
	if len(g.Groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(g.Groups))
	}
	if len(g.Warnings) != 1 {
		t.Fatalf("warnings = %v, want one", g.Warnings)
	}
	if !strings.HasSuffix(g.Warnings[0].A.Path, "ABC-123-cd1.mp4") {
		t.Errorf("warning = %+v", g.Warnings[0])
	}

	// Far apart and both marked is not a hint, it is two different works: the
	// advisory list is bounded so a directory of parts does not print a wall.
	far := groupFiles(t, groupWith(), "ABC-123-cd1.mp4", "ZZZZZZZZZZ-999-cd2.mp4")
	if len(far.Warnings) != 0 {
		t.Errorf("warnings = %v, want none for a pair nothing suggests is one work", far.Warnings)
	}
}

func TestGroup_UnmarkedFileJoinsAMarkedGroup(t *testing.T) {
	g := groupFiles(t, groupWith(), "ABC-123.mp4", "ABC-123-cd2.mp4")
	if len(g.Groups) != 1 {
		t.Fatalf("groups = %d, want 1: one side carries the marker", len(g.Groups))
	}
	grp := g.Groups[0]
	wantParts(t, grp, "ABC-123.mp4", "ABC-123-cd2.mp4")
	if filepath.Base(grp.Primary.Path) != "ABC-123.mp4" {
		t.Errorf("primary = %s: an unmarked file is part 1", grp.Primary.Path)
	}
}

func TestGroup_PartsSortByNumberNotByName(t *testing.T) {
	g := groupFiles(t, groupWith(), "ABC-123-cd10.mp4", "ABC-123-cd2.mp4")
	if len(g.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(g.Groups))
	}
	grp := g.Groups[0]
	wantParts(t, grp, "ABC-123-cd2.mp4", "ABC-123-cd10.mp4")
	if filepath.Base(grp.Primary.Path) != "ABC-123-cd2.mp4" {
		t.Errorf("primary = %s; a lexicographic sort would put cd10 first", grp.Primary.Path)
	}
}

func TestGroup_CompanionsFollowTheirPart(t *testing.T) {
	g := groupFiles(t, groupWith(),
		"SSVR-122-cd1.mp4", "SSVR-123-cd2.mp4",
		"SSVR-122-cd1.srt", "nothing.srt")
	if len(g.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(g.Groups))
	}
	grp := g.Groups[0]
	if len(grp.Companions) != 1 {
		t.Fatalf("companions = %v, want the matching subtitle", grp.Companions)
	}
	c := grp.Companions[0]
	if filepath.Base(c.File.Path) != "SSVR-122-cd1.srt" {
		t.Errorf("companion = %s", c.File.Path)
	}
	if c.FollowsPart != 1 {
		t.Errorf("follows part %d, want part 1 (the -cd1 video)", c.FollowsPart)
	}
	if c.File.Kind != KindCompanion {
		t.Errorf("kind = %q, want companion", c.File.Kind)
	}
	if len(g.Unmatched) != 1 || filepath.Base(g.Unmatched[0].File.Path) != "nothing.srt" {
		t.Fatalf("unmatched = %v, want the subtitle with no group", g.Unmatched)
	}
	if g.Unmatched[0].Reason != SkipNoGroup {
		t.Errorf("reason = %q", g.Unmatched[0].Reason)
	}
}

func TestGroup_CompanionOnThePrimaryStemFollowsThePrimary(t *testing.T) {
	g := groupFiles(t, groupWith(), "ABC-123.mp4", "ABC-123-cd2.mp4", "ABC-123.srt")
	if len(g.Groups) != 1 || len(g.Groups[0].Companions) != 1 {
		t.Fatalf("groups = %+v", g.Groups)
	}
	if got := g.Groups[0].Companions[0].FollowsPart; got != 0 {
		t.Errorf("follows part %d, want the primary", got)
	}
}

func TestGroup_CrossDirectoryNeverMerges(t *testing.T) {
	g := groupFilesIn(t, groupWith(), "/media/a", "ABC-123-cd1.mp4")
	other := groupFilesIn(t, groupWith(), "/media/b", "ABC-123-cd2.mp4")
	if len(g.Groups) != 1 || len(other.Groups) != 1 {
		t.Fatalf("each directory groups on its own: %d, %d", len(g.Groups), len(other.Groups))
	}
	if g.Groups[0].Parts[0].Marker.Index != 1 || other.Groups[0].Parts[0].Marker.Index != 2 {
		t.Error("each side keeps its own marker")
	}
}

func TestGroup_NoiseIsStrippedBeforeGrouping(t *testing.T) {
	g := groupFiles(t, groupWith(),
		"[javdb.com]ABC-123-cd1-1080p-x264.mp4",
		"ABC-123-cd2-720p.mp4")
	if len(g.Groups) != 1 {
		t.Fatalf("groups = %d, want 1: the noise is not part of the stem", len(g.Groups))
	}
	if stem := g.Groups[0].PrimaryStem; stem != "ABC-123" {
		t.Errorf("stem = %q, want ABC-123", stem)
	}
}

func TestGroup_SanitizeTableIsStrippedToo(t *testing.T) {
	w := groupWith(func(w *config.GroupWith) {
		w.Sanitize = []string{"-sample", `r:-web$`}
	})
	g := groupFiles(t, w, "ABC-123-cd1-sample.mp4", "ABC-123-cd2-web.mp4")
	if len(g.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(g.Groups))
	}
	if stem := g.Groups[0].PrimaryStem; stem != "ABC-123" {
		t.Errorf("stem = %q", stem)
	}
}

func TestGroup_PartPatternsAreConfidentMarkers(t *testing.T) {
	w := groupWith(func(w *config.GroupWith) {
		w.PartPatterns = []string{`(?i)(?:^|[-_. ])slice(\d)$`}
	})
	g := groupFiles(t, w, "ABC-123-slice1.mp4", "ABC-124-slice2.mp4")
	if len(g.Groups) != 1 {
		t.Fatalf("groups = %d, want 1: a user pattern is a confident marker, so distance may merge", len(g.Groups))
	}
	if idx := g.Groups[0].Parts[0].Marker.Index; idx != 1 {
		t.Errorf("index = %d, want 1 from the capture group", idx)
	}
}

func TestGroup_BKTreeAgreesWithThePairwisePass(t *testing.T) {
	// More videos than max_files_for_levenshtein in one directory switches the
	// near-miss search to a BK-tree. The grouping must not change: that switch is
	// about cost, not about the answer.
	var names []string
	for i := 1; i <= 30; i++ {
		work := strconv.Itoa(100 + i)
		names = append(names,
			"WORK-"+work+"-cd1.mp4",
			"WORK-"+work+"-cd2.mp4")
	}
	names = append(names, "ABP-123.mp4", "ABP-124.mp4")

	pairwise := groupFiles(t, groupWith(func(w *config.GroupWith) { w.MaxFilesForLevenshtein = 1000 }), names...)
	tree := groupFiles(t, groupWith(func(w *config.GroupWith) { w.MaxFilesForLevenshtein = 50 }), names...)

	if len(pairwise.Groups) != 32 || len(tree.Groups) != 32 {
		t.Fatalf("groups = %d (pairwise), %d (tree), want 32 each", len(pairwise.Groups), len(tree.Groups))
	}
	if got, want := render(tree), render(pairwise); got != want {
		t.Errorf("the two paths disagree:\ntree:\n%s\npairwise:\n%s", got, want)
	}
}

// render prints a Groups value in a form two runs can be compared by.
func render(g *Groups) string {
	var b strings.Builder
	for _, grp := range g.Groups {
		b.WriteString(grp.Primary.Path)
		b.WriteString(": ")
		b.WriteString(strings.Join(basenames(grp.Parts), " | "))
		b.WriteString("\n")
	}
	for _, d := range g.Duplicates {
		b.WriteString("dup " + d.Stem + "\n")
	}
	return b.String()
}
