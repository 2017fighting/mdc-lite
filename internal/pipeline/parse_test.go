package pipeline

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2017fighting/mdc-lite/internal/config"
)

// parseWith is the rule table most tests use: the built-in set, as a pipeline
// file spells it.
func parseWith(mods ...func(*config.ParseWith)) config.ParseWith {
	w := config.ParseWith{Rules: []config.ParseRule{{Builtin: "default"}}}
	for _, mod := range mods {
		mod(&w)
	}
	return w
}

// parseNames runs the real parse step over a set of names in one directory. It
// passes Candidates directly: under ADR-0019 parse runs before group, so there is
// no stem to borrow and no sibling to agree a conditional marker.
func parseNames(t *testing.T, pw config.ParseWith, names ...string) (*Parsed, *ParseSummary) {
	t.Helper()
	st := &State{Candidates: &Candidates{}}
	for _, name := range names {
		path := name
		if !filepath.IsAbs(path) {
			path = filepath.Join("/media/a", name)
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
	step, err := newParse(pw)
	if err != nil {
		t.Fatalf("newParse: %v", err)
	}
	if err := step.Run(context.Background(), st); err != nil {
		t.Fatalf("parse: %v", err)
	}
	return st.Parsed, st.ParseRun
}

// parseOne parses one file and fails unless it resolved to exactly one candidate.
func parseOne(t *testing.T, pw config.ParseWith, name string) ParsedCandidate {
	t.Helper()
	parsed, _ := parseNames(t, pw, name)
	if len(parsed.Items) != 1 {
		t.Fatalf("%s: items = %+v, unresolved = %+v, want one resolved candidate",
			name, parsed.Items, parsed.Unresolved)
	}
	return parsed.Items[0]
}

// TestParse_APlainNameYieldsItsNumberAndNoVersion: the baseline — one file, one
// number, no part marker, no flags.
func TestParse_APlainNameYieldsItsNumberAndNoVersion(t *testing.T) {
	item := parseOne(t, parseWith(), "ABP-123.mp4")
	if item.Number.Normalized != "ABP-123" {
		t.Errorf("number = %q, want ABP-123", item.Number.Normalized)
	}
	if item.Number.Raw != "ABP-123" || item.Number.Family != "generic" {
		t.Errorf("number = %+v, want the raw text and family of the generic reading", item.Number)
	}
	if item.Part.Index != 0 || item.Part.Text != "" {
		t.Errorf("part = %+v, want no part marker", item.Part)
	}
	if item.Flags != (VersionFlags{}) {
		t.Errorf("flags = %+v, want none", item.Flags)
	}
	if item.Stem != "ABP-123" {
		t.Errorf("stem = %q, want ABP-123", item.Stem)
	}
}

// TestParse_ConfidentPartMarkers: the markers that say "part" out loud are
// evidence on their own, so parse takes them off and numbers the part.
func TestParse_ConfidentPartMarkers(t *testing.T) {
	for _, tc := range []struct {
		name string
		part int
		text string
	}{
		{"ABC-123-cd1.mp4", 1, "cd1"},
		{"ABC-123.cd2.mp4", 2, "cd2"},
		{"ABC-123.part2.mp4", 2, "part2"},
		{"ABC-123_disc3.mkv", 3, "disc3"},
		{"ABC-123-vol4.mp4", 4, "vol4"},
		{"ABC-123-B.mp4", 2, "B"},
	} {
		item := parseOne(t, parseWith(), tc.name)
		if item.Number.Normalized != "ABC-123" {
			t.Errorf("%s: number = %q, want ABC-123", tc.name, item.Number.Normalized)
		}
		if item.Part.Index != tc.part || item.Part.Text != tc.text || !item.Part.Confident {
			t.Errorf("%s: part = %+v, want confident %q (%d)", tc.name, item.Part, tc.text, tc.part)
		}
		if item.Stem != "ABC-123" {
			t.Errorf("%s: stem = %q, want the marker taken off", tc.name, item.Stem)
		}
	}
}

// TestParse_HeterogeneousVRName: the ticket's own counterexample. The name pads
// the serial, writes the part marker as `part1`, and carries an `8K` token, and
// still has to land on HNVR-152 part 1.
func TestParse_HeterogeneousVRName(t *testing.T) {
	item := parseOne(t, parseWith(), "hnvr00152.part1_8K.mp4")
	if item.Number.Normalized != "HNVR-152" {
		t.Errorf("number = %q, want HNVR-152 (the VR width table trims the pad)", item.Number.Normalized)
	}
	if item.Part.Index != 1 || item.Part.Text != "part1" {
		t.Errorf("part = %+v, want part1", item.Part)
	}
	if item.Stem != "hnvr00152" {
		t.Errorf("stem = %q, want hnvr00152", item.Stem)
	}

	// The other spelling of the same work has to normalize to the same number.
	other := parseOne(t, parseWith(), "HNVR-152.part3_8K.mp4")
	if other.Number.Normalized != item.Number.Normalized {
		t.Errorf("%s = %q, want the same number as %q",
			other.File.Path, other.Number.Normalized, item.Number.Normalized)
	}
}

// TestParse_ChineseFlag: every spelling in the ticket marks a Chinese version,
// and a `cd1` part marker is not one of them.
func TestParse_ChineseFlag(t *testing.T) {
	for _, name := range []string{
		"ABC-123-C.mp4",
		"ABC-123_C.mp4",
		"ABC-123-ch.mp4",
		"ABC-123-chs.mp4",
		"ABC-123_中文.mp4",
		"ABC-123-中文字幕.mp4",
		"ABC-123-中字.mp4",
	} {
		item := parseOne(t, parseWith(), name)
		if !item.Flags.IsChinese || item.Flags.IsUncensored {
			t.Errorf("%s: flags = %+v, want chinese only", name, item.Flags)
		}
		if item.Number.Normalized != "ABC-123" {
			t.Errorf("%s: number = %q, want ABC-123", name, item.Number.Normalized)
		}
		if item.Stem != "ABC-123" {
			t.Errorf("%s: stem = %q, want the flag taken off", name, item.Stem)
		}
	}

	// `cd1` starts with a c, but it is a part marker, not a Chinese flag.
	if item := parseOne(t, parseWith(), "ABC-123-cd1.mp4"); item.Flags.IsChinese {
		t.Errorf("cd1 read as a Chinese flag: %+v", item.Flags)
	}
}

// TestParse_UncensoredFlag: the ticket's spellings for the uncensored version.
func TestParse_UncensoredFlag(t *testing.T) {
	for _, name := range []string{
		"ABC-123-U.mp4",
		"ABC-123_U.mp4",
		"ABC-123-无码.mp4",
		"ABC-123-無碼.mp4",
		"ABC-123-破解.mp4",
		"ABC-123-流出.mp4",
		"ABC-123-uncensored.mp4",
		"ABC-123-leaked.mp4",
	} {
		item := parseOne(t, parseWith(), name)
		if !item.Flags.IsUncensored || item.Flags.IsChinese {
			t.Errorf("%s: flags = %+v, want uncensored only", name, item.Flags)
		}
		if item.Number.Normalized != "ABC-123" {
			t.Errorf("%s: number = %q, want ABC-123", name, item.Number.Normalized)
		}
		if item.Stem != "ABC-123" {
			t.Errorf("%s: stem = %q, want the flag taken off", name, item.Stem)
		}
	}
}

// TestParse_BothFlags: the combined tokens set both flags, in either order, and
// never invent a second number.
func TestParse_BothFlags(t *testing.T) {
	for _, name := range []string{
		"ABC-123-UC.mp4",
		"ABC-123-uc.mp4",
		"ABC-123-CU.mp4",
		"ABC-123-U-C.mp4",
		"ABC-123-中文-破解.mp4",
	} {
		item := parseOne(t, parseWith(), name)
		if !item.Flags.IsChinese || !item.Flags.IsUncensored {
			t.Errorf("%s: flags = %+v, want chinese and uncensored", name, item.Flags)
		}
		if item.Number.Normalized != "ABC-123" {
			t.Errorf("%s: number = %q, want ABC-123", name, item.Number.Normalized)
		}
		if item.Stem != "ABC-123" {
			t.Errorf("%s: stem = %q, want the flags taken off", name, item.Stem)
		}
	}
}

// TestParse_AFlagSpellingAtTheStartKeepsThePrefix: `CH-123` is a number whose
// prefix happens to look like a flag, not a file with a flag in front. The noise
// table only takes flag tokens after a separator, so the prefix survives.
func TestParse_AFlagSpellingAtTheStartKeepsThePrefix(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"CH-123.mp4", "CH-123"},
		{"UC-123.mp4", "UC-123"},
	} {
		item := parseOne(t, parseWith(), tc.name)
		if item.Number.Normalized != tc.want {
			t.Errorf("%s: number = %q, want %q", tc.name, item.Number.Normalized, tc.want)
		}
		if item.Flags != (VersionFlags{}) {
			t.Errorf("%s: flags = %+v, want none: the token is the prefix", tc.name, item.Flags)
		}
	}
}

// TestParse_FlagsBeforeAndAfterPartMarker: a version flag can sit on either side
// of the part marker; it must not hide the part or change the number.
func TestParse_FlagsBeforeAndAfterPartMarker(t *testing.T) {
	for _, tc := range []struct {
		name string
		part int
	}{
		{"ABC-123-cd1-C.mp4", 1},
		{"ABC-123-C-cd2.mp4", 2},
		{"ABC-123-UC-cd2.mp4", 2},
	} {
		item := parseOne(t, parseWith(), tc.name)
		if item.Part.Index != tc.part {
			t.Errorf("%s: part = %+v, want %d", tc.name, item.Part, tc.part)
		}
		if item.Number.Normalized != "ABC-123" {
			t.Errorf("%s: number = %q", tc.name, item.Number.Normalized)
		}
		if item.Stem != "ABC-123" {
			t.Errorf("%s: stem = %q, want ABC-123", tc.name, item.Stem)
		}
	}
}

// TestParse_AmbiguousGoesToAHumanWithTheCandidates: several numbers and none the
// less specific reading of another means ask, and show what to choose from.
func TestParse_AmbiguousGoesToAHumanWithTheCandidates(t *testing.T) {
	parsed, summary := parseNames(t, parseWith(), "ABC-123_XYZ-456.mp4")

	if len(parsed.Items) != 0 {
		t.Fatalf("items = %d, want nothing resolved", len(parsed.Items))
	}
	if len(parsed.Unresolved) != 1 {
		t.Fatalf("unresolved = %v, want the one file", parsed.Unresolved)
	}
	u := parsed.Unresolved[0]
	if u.Reason != ReasonAmbiguous {
		t.Errorf("reason = %q, want %q", u.Reason, ReasonAmbiguous)
	}
	if got := strings.Join(u.Candidates, ", "); got != "ABC-123, XYZ-456" {
		t.Errorf("candidates = %q, want both readings", got)
	}
	if len(u.Files) != 1 || u.Files[0].Path != "/media/a/ABC-123_XYZ-456.mp4" {
		t.Errorf("files = %v, want the video a human must number", u.Files)
	}
	if summary.Unresolved[ReasonAmbiguous] != 1 {
		t.Errorf("summary = %+v, want one ambiguous file", summary)
	}
	if got := strings.Join(summary.CandidateNumbers[u.Files[0].Path], ", "); got != "ABC-123, XYZ-456" {
		t.Errorf("summary candidates = %q, want both readings", got)
	}
}

// TestParse_NoCandidateIsItsOwnReason: a file with no number at all is not the
// same thing as an ambiguous one, and no candidate list is invented for it.
func TestParse_NoCandidateIsItsOwnReason(t *testing.T) {
	parsed, summary := parseNames(t, parseWith(), "hello.mp4")

	if len(parsed.Unresolved) != 1 {
		t.Fatalf("unresolved = %v", parsed.Unresolved)
	}
	u := parsed.Unresolved[0]
	if u.Reason != ReasonNoCandidate {
		t.Errorf("reason = %q, want %q", u.Reason, ReasonNoCandidate)
	}
	if len(u.Candidates) != 0 {
		t.Errorf("candidates = %v, want none", u.Candidates)
	}
	if _, ok := summary.CandidateNumbers[u.Files[0].Path]; ok {
		t.Errorf("summary invented candidates for a no_candidate file: %v", summary.CandidateNumbers)
	}
	if summary.Unresolved[ReasonNoCandidate] != 1 {
		t.Errorf("summary = %+v", summary)
	}
}

// TestParse_ALoneTrailingTokenStaysOutOfTheNumber: a conditional marker needs a
// sibling's agreement, and parse has not seen the siblings. It stays in the stem
// and out of the number until `group` can settle it.
func TestParse_ALoneTrailingTokenStaysOutOfTheNumber(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"ABC-123-1.mp4", "ABC-123"},  // lone trailing digit: stays in the stem
		{"ABC-123-12.mp4", "ABC-123"}, // two lone digits are the same ruling
		{"ABC-123A.mp4", "ABC-123"},   // a glued letter is a release suffix
	} {
		item := parseOne(t, parseWith(), tc.name)
		if item.Number.Normalized != tc.want {
			t.Errorf("%s: number = %q, want %q: a lone trailing token is neither a serial nor the number's tail",
				tc.name, item.Number.Normalized, tc.want)
		}
		if item.Part.Index != 0 {
			t.Errorf("%s: part = %+v, want no part until a sibling agrees", tc.name, item.Part)
		}
	}
}

// TestParse_AnAudioCodecInTheNameIsNotASecondNumber: the generic rule would read
// `AAC2.0` and `H.264` as numbers; the noise table takes them out first, so a real
// scene name is one number, not ambiguous.
func TestParse_AnAudioCodecInTheNameIsNotASecondNumber(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"MIDV-804.2025.1080p.DMM.WEB-DL.AAC2.0.HFR.H.264-MTeam.mkv", "MIDV-804"},
		{"SONE-239.2025.2160p.DMM.WEB-DL.AAC2.0.HFR.H.264-MTeam.mkv", "SONE-239"},
	} {
		item := parseOne(t, parseWith(), tc.name)
		if item.Number.Normalized != tc.want {
			t.Errorf("%s: number = %q, want %q", tc.name, item.Number.Normalized, tc.want)
		}
	}
}

// TestParse_FullWidthNameResolvesThroughTheStep: folding has to happen before the
// rules match, not only inside normalization, so a name typed on a CJK keyboard
// reaches the same rule as its half-width spelling.
func TestParse_FullWidthNameResolvesThroughTheStep(t *testing.T) {
	item := parseOne(t, parseWith(), "ＡＢＣ－１２３.mp4")
	if item.Number.Normalized != "ABC-123" {
		t.Errorf("number = %q, want ABC-123", item.Number.Normalized)
	}
}

// TestParse_SanitizeTableIsStripped: the user's sanitize table is parse's now, and
// a literal or a regex entry both keep the noise out of the number.
func TestParse_SanitizeTableIsStripped(t *testing.T) {
	withSanitize := parseWith(func(w *config.ParseWith) {
		w.Sanitize = []string{"-sample", `r:-web$`}
	})
	for _, name := range []string{"ABC-123-sample.mp4", "ABC-123-web.mp4"} {
		item := parseOne(t, withSanitize, name)
		if item.Number.Normalized != "ABC-123" {
			t.Errorf("%s: number = %q, want ABC-123", name, item.Number.Normalized)
		}
		if item.Stem != "ABC-123" {
			t.Errorf("%s: stem = %q, want ABC-123", name, item.Stem)
		}
	}
}

// TestParse_PartPatternsAreConfidentMarkers: a user part pattern is a confident
// marker whose first capture group is the part number.
func TestParse_PartPatternsAreConfidentMarkers(t *testing.T) {
	withParts := parseWith(func(w *config.ParseWith) {
		w.PartPatterns = []string{`(?i)(?:^|[-_. ])slice(\d)$`}
	})
	item := parseOne(t, withParts, "ABC-123-slice2.mp4")
	if item.Part.Index != 2 || item.Part.Text != "slice2" || !item.Part.Confident {
		t.Errorf("part = %+v, want confident slice2 (2)", item.Part)
	}
	if item.Number.Normalized != "ABC-123" || item.Stem != "ABC-123" {
		t.Errorf("number/stem = %q/%q, want ABC-123/ABC-123", item.Number.Normalized, item.Stem)
	}
}

// TestParse_CompanionsAreReadToo: parse does not know which extensions `group`
// calls companions, so a subtitle is parsed like any other candidate; group will
// sort it out by extension and the number it shares with its video.
func TestParse_CompanionsAreReadToo(t *testing.T) {
	item := parseOne(t, parseWith(), "ABC-123-cd2.srt")
	if item.Number.Normalized != "ABC-123" || item.Part.Index != 2 {
		t.Errorf("candidate = %+v, want ABC-123 part 2", item)
	}
	if item.File.Ext != ".srt" {
		t.Errorf("ext = %q, want .srt", item.File.Ext)
	}
}

// TestParse_SummaryCountsEveryOutcome: one line has to be enough to see what the
// rules did, so the counters are part of the contract.
func TestParse_SummaryCountsEveryOutcome(t *testing.T) {
	parsed, summary := parseNames(t, parseWith(),
		"ABC-123.mp4",
		"ABC-124-cd1.mp4",
		"ABC-125-C.mp4",
		"ABC-126-U.mp4",
		"hello.mp4",
		"ABC-127_XYZ-456.mp4",
	)
	if got := len(parsed.Items); got != 4 {
		t.Errorf("items = %d, want 4 resolved", got)
	}
	if summary.Files != 6 || summary.Resolved != 4 {
		t.Errorf("summary files/resolved = %d/%d, want 6/4", summary.Files, summary.Resolved)
	}
	if summary.Parts != 1 || summary.Chinese != 1 || summary.Uncensored != 1 {
		t.Errorf("summary parts/chinese/uncensored = %d/%d/%d, want 1/1/1",
			summary.Parts, summary.Chinese, summary.Uncensored)
	}
	if summary.Unresolved[ReasonNoCandidate] != 1 || summary.Unresolved[ReasonAmbiguous] != 1 {
		t.Errorf("summary unresolved = %v, want one of each", summary.Unresolved)
	}
	want := "6 files, 1 part, 4 resolved, 1 chinese, 1 uncensored, 1 needs a number (no_candidate), 1 needs a number (ambiguous)"
	if got := summary.String(); got != want {
		t.Errorf("summary.String() = %q, want %q", got, want)
	}
}

// TestParse_NoCandidatesIsAnError: parse's input is the filter's artifact; without
// it there is nothing to read, and saying so beats an empty result.
func TestParse_NoCandidatesIsAnError(t *testing.T) {
	step, err := newParse(parseWith())
	if err != nil {
		t.Fatalf("newParse: %v", err)
	}
	err = step.Run(context.Background(), &State{})
	if err == nil || !strings.Contains(err.Error(), "Candidates") {
		t.Fatalf("got %v, want an error naming Candidates", err)
	}
}
