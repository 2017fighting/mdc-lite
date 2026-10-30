package pipeline

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2017fighting/mdc-lite/internal/config"
)

// groupFiles runs parse and then group over a set of file names in one directory.
// Grouping consumes Parsed (ADR-0019), so a group test has to parse first: a test
// that handed Candidates straight to group would be testing a step that no longer
// exists. The files never touch the disk — grouping is a decision about names and
// directories, so making it need real files would only make the corpus slower to
// write.
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
	parse, err := newParse(parseWith())
	if err != nil {
		t.Fatalf("newParse: %v", err)
	}
	if err := parse.Run(context.Background(), st); err != nil {
		t.Fatalf("parse: %v", err)
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

func wantOneGroup(t *testing.T, g *Groups, number string) Group {
	t.Helper()
	if len(g.Groups) != 1 {
		t.Fatalf("groups = %d, want 1 (%+v)", len(g.Groups), g.Groups)
	}
	grp := g.Groups[0]
	if grp.Number.Normalized != number {
		t.Fatalf("number = %q, want %q", grp.Number.Normalized, number)
	}
	return grp
}

// TestGroup_VersionsOfOneNumberAreOneWork: the original and its chinese and
// uncensored copies are the same work. They used to be a duplicate stem sent to a
// human; since ADR-0019 they are one group, and the original is its primary.
func TestGroup_VersionsOfOneNumberAreOneWork(t *testing.T) {
	g := groupFiles(t, groupWith(), "ABC-123-UC.mp4", "ABC-123.mp4", "ABC-123-C.mp4")
	grp := wantOneGroup(t, g, "ABC-123")
	wantParts(t, grp, "ABC-123.mp4", "ABC-123-C.mp4", "ABC-123-UC.mp4")
	if filepath.Base(grp.Primary.Path) != "ABC-123.mp4" {
		t.Errorf("primary = %s, want the original", grp.Primary.Path)
	}
	if got := grp.Parts[1].Flags; !got.IsChinese || got.IsUncensored {
		t.Errorf("flags of the -C copy = %+v, want chinese only", got)
	}
	if got := grp.Parts[2].Flags; !got.IsChinese || !got.IsUncensored {
		t.Errorf("flags of the -UC copy = %+v, want both", got)
	}
	if len(g.Unmatched) != 0 {
		t.Errorf("unmatched = %+v, want none", g.Unmatched)
	}
}

// TestGroup_HeterogeneousPartsAreOneWork: leading zeros and case are spelling, not
// identity. Normalization makes `hnvr00152` and `HNVR-152` one number, so the two
// discs are one work even though they sit in different directories (issue #33).
func TestGroup_HeterogeneousPartsAreOneWork(t *testing.T) {
	g := groupFiles(t, groupWith(),
		"/media/a/hnvr00152.part1.mp4",
		"/media/b/HNVR-152.part3.mp4")
	grp := wantOneGroup(t, g, "HNVR-152")
	wantParts(t, grp, "hnvr00152.part1.mp4", "HNVR-152.part3.mp4")
	if filepath.Base(grp.Primary.Path) != "hnvr00152.part1.mp4" {
		t.Errorf("primary = %s, want part 1", grp.Primary.Path)
	}
	// The primary speaks for the group, so the number's spelling is the primary's
	// reading and not whichever file happens to come first in the walk.
	if grp.Number.Raw != "hnvr00152" || grp.Number.Family != "generic" {
		t.Errorf("number = %+v, want the primary's reading", grp.Number)
	}
	if grp.Dir != "/media/a" {
		t.Errorf("dir = %q, want the primary's directory", grp.Dir)
	}
}

// TestGroup_PrimaryPrefersTheLowestPart: part order beats version order. A chinese
// copy of disc 1 is a better primary than the original of disc 2, because the
// primary's job is to be the first disc.
func TestGroup_PrimaryPrefersTheLowestPart(t *testing.T) {
	g := groupFiles(t, groupWith(), "ABC-123-cd2.mp4", "ABC-123-cd1-UC.mp4", "ABC-123-cd1-C.mp4")
	grp := wantOneGroup(t, g, "ABC-123")
	wantParts(t, grp, "ABC-123-cd1-C.mp4", "ABC-123-cd1-UC.mp4", "ABC-123-cd2.mp4")
	if filepath.Base(grp.Primary.Path) != "ABC-123-cd1-C.mp4" {
		t.Errorf("primary = %s, want the chinese copy of disc 1", grp.Primary.Path)
	}
}

// TestGroup_PrimaryVersionOrder pins the version ranking on its own: 原版 > 中文 >
// 破解.
func TestGroup_PrimaryVersionOrder(t *testing.T) {
	original := groupFiles(t, groupWith(), "ABC-123-U.mp4", "ABC-123-C.mp4", "ABC-123.mp4")
	if filepath.Base(original.Groups[0].Primary.Path) != "ABC-123.mp4" {
		t.Errorf("primary = %s, want the original", original.Groups[0].Primary.Path)
	}

	noOriginal := groupFiles(t, groupWith(), "ABC-123-U.mp4", "ABC-123-C.mp4")
	if filepath.Base(noOriginal.Groups[0].Primary.Path) != "ABC-123-C.mp4" {
		t.Errorf("primary = %s, want the chinese copy", noOriginal.Groups[0].Primary.Path)
	}

	uncensoredOnly := groupFiles(t, groupWith(), "ABC-123-U.mp4", "ABC-123-UC.mp4")
	if filepath.Base(uncensoredOnly.Groups[0].Primary.Path) != "ABC-123-U.mp4" {
		t.Errorf("primary = %s, want the plain uncensored copy over the uncensored chinese one",
			uncensoredOnly.Groups[0].Primary.Path)
	}
}

func TestGroup_AdjacentNumbersStayApart(t *testing.T) {
	g := groupFiles(t, groupWith(), "ABP-123.mp4", "ABP-124.mp4")
	if len(g.Groups) != 2 {
		t.Fatalf("groups = %d, want 2 (ABP-123 and ABP-124 are different works)", len(g.Groups))
	}
	if len(g.Unmatched) != 0 {
		t.Errorf("unmatched = %+v, want none", g.Unmatched)
	}
}

// TestGroup_SameNumberAcrossDirectoriesIsOneGroup: the number is the identity, so
// the directory a file arrived in does not split the work.
func TestGroup_SameNumberAcrossDirectoriesIsOneGroup(t *testing.T) {
	g := groupFiles(t, groupWith(),
		"/media/a/ABC-123-cd1.mp4",
		"/media/b/ABC-123-cd2.mp4")
	grp := wantOneGroup(t, g, "ABC-123")
	wantParts(t, grp, "ABC-123-cd1.mp4", "ABC-123-cd2.mp4")
	if filepath.Base(grp.Primary.Path) != "ABC-123-cd1.mp4" {
		t.Errorf("primary = %s", grp.Primary.Path)
	}
}

// TestGroup_AFullWidthNameIsTheSameNumber: full-width folding happens before the
// number is read, so two spellings of one work are one number and one group.
func TestGroup_AFullWidthNameIsTheSameNumber(t *testing.T) {
	g := groupFiles(t, groupWith(), "ABC-123.mp4", "ＡＢＣ－１２３.mp4")
	grp := wantOneGroup(t, g, "ABC-123")
	if len(grp.Parts) != 2 {
		t.Fatalf("parts = %v, want both spellings", basenames(grp.Parts))
	}
}

// TestGroup_NoiseIsNotPartOfTheNumber: what `parse` strips stays stripped. The
// resolution and the site prefix are noise, and a group built on the number does
// not see them at all.
func TestGroup_NoiseIsNotPartOfTheNumber(t *testing.T) {
	g := groupFiles(t, groupWith(),
		"[javdb.com]ABC-123-cd1-1080p-x264.mp4",
		"ABC-123-cd2-720p.mp4")
	grp := wantOneGroup(t, g, "ABC-123")
	wantParts(t, grp, "[javdb.com]ABC-123-cd1-1080p-x264.mp4", "ABC-123-cd2-720p.mp4")
	if grp.PrimaryStem != "ABC-123" {
		t.Errorf("primary stem = %q, want ABC-123", grp.PrimaryStem)
	}
}

// TestGroup_AnUnnumberedFileIsNotAWork: grouping never guesses a number. The file
// stays in Parsed.Unresolved and reaches a human as its own task.
func TestGroup_AnUnnumberedFileIsNotAWork(t *testing.T) {
	// hello.mp4 is unnamed as a work; nothing.srt matches no group either.
	g := groupFiles(t, groupWith(), "ABC-123.mp4", "hello.mp4", "nothing.srt")
	grp := wantOneGroup(t, g, "ABC-123")
	wantParts(t, grp, "ABC-123.mp4")
	if len(g.Unmatched) != 1 || filepath.Base(g.Unmatched[0].File.Path) != "nothing.srt" {
		t.Fatalf("unmatched = %+v, want only the orphan subtitle", g.Unmatched)
	}
	if g.Unmatched[0].Reason != SkipNoGroup {
		t.Errorf("reason = %q", g.Unmatched[0].Reason)
	}
}

func TestGroup_CompanionsFollowTheirPart(t *testing.T) {
	g := groupFiles(t, groupWith(),
		"ABC-123-cd1.mp4", "ABC-123-cd2.mp4",
		"ABC-123-cd1.srt", "nothing.srt")
	grp := wantOneGroup(t, g, "ABC-123")
	if len(grp.Companions) != 1 {
		t.Fatalf("companions = %v, want the matching subtitle", grp.Companions)
	}
	c := grp.Companions[0]
	if filepath.Base(c.File.Path) != "ABC-123-cd1.srt" {
		t.Errorf("companion = %s", c.File.Path)
	}
	if c.FollowsPart != 1 {
		t.Errorf("follows part %d, want part 1 (the -cd1 video)", c.FollowsPart)
	}
	if c.File.Kind != KindCompanion {
		t.Errorf("kind = %q, want companion", c.File.Kind)
	}
	if len(g.Unmatched) != 1 || filepath.Base(g.Unmatched[0].File.Path) != "nothing.srt" {
		t.Fatalf("unmatched = %+v, want the subtitle with no group", g.Unmatched)
	}
}

func TestGroup_CompanionOnTheWorkStemFollowsThePrimary(t *testing.T) {
	g := groupFiles(t, groupWith(), "ABC-123.mp4", "ABC-123-cd2.mp4", "ABC-123.srt", "ABC-123.jpg")
	grp := wantOneGroup(t, g, "ABC-123")
	if len(grp.Companions) != 2 {
		t.Fatalf("companions = %+v, want the subtitle and the image", grp.Companions)
	}
	for _, c := range grp.Companions {
		if c.FollowsPart != 0 {
			t.Errorf("%s follows part %d, want the primary", c.File.Path, c.FollowsPart)
		}
	}
}

// TestGroup_ACompanionKeepsItsLanguageToken: the subtitle names its work, not its
// language. The token is split off to find the group and kept for the rename.
func TestGroup_ACompanionKeepsItsLanguageToken(t *testing.T) {
	g := groupFiles(t, groupWith(), "ABC-123.mp4", "ABC-123.zh.srt")
	grp := wantOneGroup(t, g, "ABC-123")
	if len(grp.Companions) != 1 {
		t.Fatalf("companions = %+v", grp.Companions)
	}
	c := grp.Companions[0]
	if c.Lang != ".zh" || c.Stem != "ABC-123" {
		t.Errorf("companion = %+v, want lang .zh and stem ABC-123", c)
	}
}

func TestGroup_ACompanionFindsItsNumberAcrossDirectories(t *testing.T) {
	g := groupFiles(t, groupWith(),
		"/media/a/ABC-123.mp4",
		"/media/b/ABC-123.zh.srt")
	grp := wantOneGroup(t, g, "ABC-123")
	if len(grp.Companions) != 1 {
		t.Fatalf("companions = %+v, want the subtitle from the other directory", grp.Companions)
	}
	if filepath.Base(grp.Companions[0].File.Path) != "ABC-123.zh.srt" {
		t.Errorf("companion = %s", grp.Companions[0].File.Path)
	}
}

func TestGroup_CompanionExtensionsComeFromTheConfig(t *testing.T) {
	// The table says who counts as a companion; an extension outside it is parsed
	// and grouped as a video, which is what the config asks for.
	w := groupWith(func(w *config.GroupWith) { w.CompanionExtensions = []string{".ass"} })
	g := groupFiles(t, w, "ABC-123.mp4", "ABC-123.srt")
	grp := wantOneGroup(t, g, "ABC-123")
	if len(grp.Companions) != 0 {
		t.Errorf("companions = %+v, want none: .srt is not in this pipeline's table", grp.Companions)
	}
	if len(grp.Parts) != 2 {
		t.Errorf("parts = %v, want the .srt parsed as a video", basenames(grp.Parts))
	}
}

// TestGroup_PartsSortByPartNumberNotByName: cd2 stays in front of cd10 in the same
// way part 1 stays in front of part 2.
func TestGroup_PartsSortByPartNumberNotByName(t *testing.T) {
	g := groupFiles(t, groupWith(), "ABC-123-cd10.mp4", "ABC-123-cd2.mp4")
	grp := wantOneGroup(t, g, "ABC-123")
	wantParts(t, grp, "ABC-123-cd2.mp4", "ABC-123-cd10.mp4")
	if filepath.Base(grp.Primary.Path) != "ABC-123-cd2.mp4" {
		t.Errorf("primary = %s; a lexicographic sort would put cd10 first", grp.Primary.Path)
	}
}

// TestGroup_UnmarkedFileIsPartOne: a name with no marker is the whole work, and an
// explicit disc 2 follows it.
func TestGroup_UnmarkedFileIsPartOne(t *testing.T) {
	g := groupFiles(t, groupWith(), "ABC-123.mp4", "ABC-123-cd2.mp4")
	grp := wantOneGroup(t, g, "ABC-123")
	wantParts(t, grp, "ABC-123.mp4", "ABC-123-cd2.mp4")
	if filepath.Base(grp.Primary.Path) != "ABC-123.mp4" {
		t.Errorf("primary = %s: an unmarked file is part 1", grp.Primary.Path)
	}
}

func TestGroup_SummaryCountsTheWork(t *testing.T) {
	st := &State{Candidates: &Candidates{}}
	for _, name := range []string{"ABC-123.mp4", "ABC-123-C.mp4", "ABC-123.srt", "ABP-124-cd1.mp4", "hello.mp4"} {
		path := filepath.Join("/media/a", name)
		st.Candidates.Files = append(st.Candidates.Files, Candidate{
			File: FileRef{Path: path, Size: 1, SizeKnown: true, Ext: strings.ToLower(filepath.Ext(path))},
			Rule: "videos",
		})
	}
	parse, err := newParse(parseWith())
	if err != nil {
		t.Fatalf("newParse: %v", err)
	}
	if err := parse.Run(context.Background(), st); err != nil {
		t.Fatalf("parse: %v", err)
	}
	step, err := newGroup(groupWith())
	if err != nil {
		t.Fatalf("newGroup: %v", err)
	}
	if err := step.Run(context.Background(), st); err != nil {
		t.Fatalf("group: %v", err)
	}
	want := GroupSummary{Groups: 2, Videos: 3, Companions: 1}
	if *st.GroupRun != want {
		t.Errorf("summary = %+v, want %+v", *st.GroupRun, want)
	}
	if got := st.GroupRun.String(); !strings.Contains(got, "2 groups, 3 videos, 1 companion") {
		t.Errorf("summary line = %q", got)
	}
}

func TestGroup_RequiresParsed(t *testing.T) {
	step, err := newGroup(groupWith())
	if err != nil {
		t.Fatalf("newGroup: %v", err)
	}
	if err := step.Run(context.Background(), &State{}); err == nil {
		t.Fatal("group without Parsed must fail: there is nothing to fold by number")
	}
}

// TestGroup_ConditionalMarkersNeedASibling: `parse` leaves a trailing `-1` in the
// stem because one file cannot tell a part marker from a number. The sibling does:
// on its own the name is the whole work, next to `-2` it is part 1.
func TestGroup_ConditionalMarkersNeedASibling(t *testing.T) {
	alone := groupFiles(t, groupWith(), "ABC-123-1.mp4")
	grp := wantOneGroup(t, alone, "ABC-123")
	if grp.PrimaryStem != "ABC-123-1" {
		t.Errorf("stem = %q, want the whole name: a lone -1 is not a part marker", grp.PrimaryStem)
	}
	if grp.Parts[0].Marker.Text != "" {
		t.Errorf("marker = %+v, want none", grp.Parts[0].Marker)
	}

	withSibling := groupFiles(t, groupWith(), "ABC-123-2.mp4", "ABC-123-1.mp4")
	grp = wantOneGroup(t, withSibling, "ABC-123")
	wantParts(t, grp, "ABC-123-1.mp4", "ABC-123-2.mp4")
	for i, want := range []int{1, 2} {
		if got := grp.Parts[i].Marker.Index; got != want {
			t.Errorf("part %d index = %d, want %d", i, got, want)
		}
		if grp.Parts[i].Marker.Confident {
			t.Errorf("part %d: a sibling-agreed ending is conditional, not confident", i)
		}
	}
	if grp.PrimaryStem != "ABC-123" {
		t.Errorf("primary stem = %q, want the marker off the name", grp.PrimaryStem)
	}
}

// TestGroup_GluedLetterPartsAgree: the same sibling rule for a letter glued to the
// number (`9ofje121A`), whose part letter is not confident on its own.
func TestGroup_GluedLetterPartsAgree(t *testing.T) {
	g := groupFiles(t, groupWith(), "9ofje121B.mkv", "9ofje121A.mkv", "9ofje121A.srt")
	grp := wantOneGroup(t, g, "OFJE-121")
	wantParts(t, grp, "9ofje121A.mkv", "9ofje121B.mkv")
	if grp.Parts[0].Marker.Text != "A" || grp.Parts[0].Marker.Index != 1 {
		t.Errorf("first part = %+v, want A (1)", grp.Parts[0].Marker)
	}
	if grp.Parts[1].Marker.Text != "B" || grp.Parts[1].Marker.Index != 2 {
		t.Errorf("second part = %+v, want B (2)", grp.Parts[1].Marker)
	}
	if filepath.Base(grp.Primary.Path) != "9ofje121A.mkv" {
		t.Errorf("primary = %s, want part A", grp.Primary.Path)
	}
	// The subtitle carries the same conditional letter, so it follows that part.
	if len(grp.Companions) != 1 || grp.Companions[0].FollowsPart != 1 {
		t.Errorf("companions = %+v, want the subtitle on part 1", grp.Companions)
	}
}

// TestGroup_TheSameEndingIsNotTwoParts: two files with the *same* conditional
// ending are one part with two copies, not part 1 twice.
func TestGroup_TheSameEndingIsNotTwoParts(t *testing.T) {
	g := groupFiles(t, groupWith(), "ABC-123-1.mp4", "ABC-123-1.mkv")
	grp := wantOneGroup(t, g, "ABC-123")
	if len(grp.Parts) != 2 {
		t.Fatalf("parts = %v, want both copies of one work", basenames(grp.Parts))
	}
	for _, p := range grp.Parts {
		if p.Marker.Text != "" {
			t.Errorf("%s marker = %+v: identical endings do not make two parts", p.File.Path, p.Marker)
		}
	}
}
