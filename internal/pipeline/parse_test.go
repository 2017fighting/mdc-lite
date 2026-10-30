package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/2017fighting/mdc-lite/internal/config"
)

// parseWith is the rule table most tests use: the built-in set, as a pipeline
// file spells it.
func parseWith() config.ParseWith {
	return config.ParseWith{Rules: []config.ParseRule{{Builtin: "default"}}}
}

// parseNames runs the real group step and then the parse step over a set of names
// in one directory. Grouping is part of the setup because parse reads the stems
// group produced and never re-derives them.
func parseNames(t *testing.T, gw config.GroupWith, pw config.ParseWith, names ...string) (*Groups, *Parsed, *ParseSummary) {
	t.Helper()
	groups := groupFiles(t, gw, names...)
	step, err := newParse(pw)
	if err != nil {
		t.Fatalf("newParse: %v", err)
	}
	st := &State{Groups: groups}
	if err := step.Run(context.Background(), st); err != nil {
		t.Fatalf("parse: %v", err)
	}
	return groups, st.Parsed, st.ParseRun
}

// TestParse_PrimaryDecidesAndADivergenceIsRecorded: the primary's reading is the
// group's number, and a part that reads another number is recorded rather than
// treated as a failure. Two adjacent numbers that grouped by stem distance are
// exactly that case.
func TestParse_PrimaryDecidesAndADivergenceIsRecorded(t *testing.T) {
	_, parsed, summary := parseNames(t, groupWith(), parseWith(),
		"SSVR-122-cd1.mp4", "SSVR-123-cd2.mp4")

	if len(parsed.Items) != 1 {
		t.Fatalf("items = %d, want the one group resolved", len(parsed.Items))
	}
	item := parsed.Items[0]
	if item.Number.Normalized != "SSVR-122" {
		t.Errorf("number = %q, want the primary's SSVR-122", item.Number.Normalized)
	}
	if item.Number.Raw != "SSVR-122" || item.Number.Family != "generic" {
		t.Errorf("number = %+v, want the raw text and family of the reading it came from", item.Number)
	}
	if len(item.Parts) != 2 {
		t.Fatalf("parts = %v, want one entry per part", item.Parts)
	}
	if got := strings.Join(summary.Conflicts, ","); got != "SSVR-123" {
		t.Errorf("conflicts = %q, want SSVR-123", got)
	}
	if summary.Resolved != 1 || summary.Unresolved[ReasonAmbiguous] != 0 {
		t.Errorf("a divergence must not fail the group: %+v", summary)
	}
}

// TestParse_PrimaryWithNoNumberAdoptsTheAgreedOne: when the primary's own text
// offers nothing and the rest of the group agrees on one number, take it — and say
// that the primary is what failed.
func TestParse_PrimaryWithNoNumberAdoptsTheAgreedOne(t *testing.T) {
	_, parsed, summary := parseNames(t, groupWith(), parseWith(),
		"SECRET-cd1.mp4", "SECRET-1-cd2.mp4")

	if len(parsed.Items) != 1 {
		t.Fatalf("items = %d, want the group resolved from the other part", len(parsed.Items))
	}
	item := parsed.Items[0]
	if item.Number.Normalized != "SECRET-001" {
		t.Fatalf("number = %q, want SECRET-001", item.Number.Normalized)
	}
	if !item.PrimaryFailed {
		t.Error("the group must record that the primary produced no number")
	}
	if summary.PrimaryFailed != 1 {
		t.Errorf("summary.PrimaryFailed = %d, want 1", summary.PrimaryFailed)
	}
	if len(item.Parts[0]) != 0 {
		t.Errorf("the primary's own candidates = %v, want none", item.Parts[0])
	}
}

// TestParse_AnAudioCodecInTheNameIsNotASecondNumber: a real scene name carries
// `AAC2.0`, and the generic rule reads `AAC` + `2` as a second number — so a
// perfectly readable release would go to a human with two candidates. The channel
// count is part of the codec token, which the noise table strips.
func TestParse_AnAudioCodecInTheNameIsNotASecondNumber(t *testing.T) {
	_, parsed, _ := parseNames(t, groupWith(), parseWith(),
		"MIDV-804.2025.1080p.DMM.WEB-DL.AAC2.0.HFR.H.264-MTeam.mkv")

	if len(parsed.Items) != 1 {
		t.Fatalf("items = %v, unresolved = %v, want the release read as one number",
			parsed.Items, parsed.Unresolved)
	}
	if got := parsed.Items[0].Number.Normalized; got != "MIDV-804" {
		t.Errorf("number = %q, want MIDV-804", got)
	}
}

// TestParse_AmbiguousGoesToAHumanWithTheCandidates: several numbers and none the
// less specific reading of another means ask, and show what to choose from.
func TestParse_AmbiguousGoesToAHumanWithTheCandidates(t *testing.T) {
	_, parsed, summary := parseNames(t, groupWith(), parseWith(), "ABC-123_XYZ-456.mp4")

	if len(parsed.Items) != 0 {
		t.Fatalf("items = %d, want nothing resolved", len(parsed.Items))
	}
	if len(parsed.Unresolved) != 1 {
		t.Fatalf("unresolved = %v, want the one group", parsed.Unresolved)
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
		t.Errorf("summary = %+v, want one ambiguous group", summary)
	}
}

// TestParse_NoCandidateIsItsOwnReason: a file with no number at all is not the
// same thing as an ambiguous one, and no candidate list is invented for it.
func TestParse_NoCandidateIsItsOwnReason(t *testing.T) {
	_, parsed, summary := parseNames(t, groupWith(), parseWith(), "hello.mp4")

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
	if summary.Unresolved[ReasonNoCandidate] != 1 {
		t.Errorf("summary = %+v", summary)
	}
}

// TestParse_DuplicateStemGoesToAHuman: a copy is not a part, so the group step
// refused to make a group of it; parse has to hand both files over with the reason
// rather than number them.
func TestParse_DuplicateStemGoesToAHuman(t *testing.T) {
	groups, parsed, summary := parseNames(t, groupWith(), parseWith(),
		"ABC-123.mp4", "ABC-123-U.mp4")

	if len(groups.Duplicates) != 1 {
		t.Fatalf("duplicates = %v, want the pair", groups.Duplicates)
	}
	if len(parsed.Items) != 0 {
		t.Fatalf("items = %d, want nothing resolved", len(parsed.Items))
	}
	if len(parsed.Unresolved) != 1 {
		t.Fatalf("unresolved = %v", parsed.Unresolved)
	}
	u := parsed.Unresolved[0]
	if u.Reason != ReasonDuplicateStem {
		t.Errorf("reason = %q, want %q", u.Reason, ReasonDuplicateStem)
	}
	if len(u.Files) != 2 {
		t.Errorf("files = %v, want both versions for the human", u.Files)
	}
	if summary.Unresolved[ReasonDuplicateStem] != 1 {
		t.Errorf("summary = %+v", summary)
	}
}

// TestParse_CompanionsStayInTheGroup: companions enter the group, bind the part
// they follow, and pass through parse untouched — they are not number evidence.
func TestParse_CompanionsStayInTheGroup(t *testing.T) {
	_, parsed, _ := parseNames(t, groupWith(), parseWith(),
		"ABC-123-cd1.mp4", "ABC-123-cd2.mp4", "ABC-123-cd2.srt")

	if len(parsed.Items) != 1 {
		t.Fatalf("items = %d", len(parsed.Items))
	}
	item := parsed.Items[0]
	if item.Number.Normalized != "ABC-123" {
		t.Errorf("number = %q", item.Number.Normalized)
	}
	if len(item.Group.Companions) != 1 {
		t.Fatalf("companions = %v, want the subtitle", item.Group.Companions)
	}
	if got := item.Group.Companions[0].FollowsPart; got != 2 {
		t.Errorf("follows_part = %d, want part 2 (the -cd2 video)", got)
	}
	if len(item.Parts) != 2 {
		t.Errorf("parsed parts = %v, want the two videos and not the subtitle", item.Parts)
	}
}

// TestParse_FullWidthNameResolvesThroughTheStep: folding has to happen before the
// rules match, not only inside normalization, so a name typed on a CJK keyboard
// reaches the same rule as its half-width spelling.
func TestParse_FullWidthNameResolvesThroughTheStep(t *testing.T) {
	_, parsed, _ := parseNames(t, groupWith(), parseWith(), "ＡＢＣ－１２３.mp4")
	if len(parsed.Items) != 1 {
		t.Fatalf("items = %d, want the full-width name resolved", len(parsed.Items))
	}
	if got := parsed.Items[0].Number.Normalized; got != "ABC-123" {
		t.Errorf("number = %q, want ABC-123", got)
	}
}

// TestParse_AGroupWithNoPartsIsAnError: `group` cannot emit one, so a group that
// has none is broken state; saying so beats a panic or a group that vanishes from
// both halves of the result.
func TestParse_AGroupWithNoPartsIsAnError(t *testing.T) {
	step, err := newParse(parseWith())
	if err != nil {
		t.Fatalf("newParse: %v", err)
	}
	st := &State{Groups: &Groups{Groups: []Group{{Dir: "/media/a"}}}}
	err = step.Run(context.Background(), st)
	if err == nil || !strings.Contains(err.Error(), "/media/a") {
		t.Fatalf("got %v, want an error naming the group", err)
	}
}
