package pipeline

import (
	"strings"
	"testing"

	"github.com/2017fighting/mdc-lite/internal/config"
)

// numberFor runs the built-in rules over one stem the way `parse` does — folding
// full-width characters before matching, as the step does — and returns the
// decided number, its raw text and family, plus every number the stem offered. It
// is the pure seam: no group, no files.
func numberFor(t *testing.T, rules []config.ParseRule, stem string) (Number, []string) {
	t.Helper()
	step, err := newParse(config.ParseWith{Rules: rules})
	if err != nil {
		t.Fatalf("newParse: %v", err)
	}
	parse := step.(*parseStep)
	matched, refused := parse.candidates(toHalfWidth(stem))
	cands := adjudicate(matched, refused)
	numbers := numbersOf(cands)
	if len(numbers) != 1 {
		return Number{}, numbers
	}
	return numberFrom(cands, numbers[0]), numbers
}

func builtinRules() []config.ParseRule {
	return []config.ParseRule{{Builtin: "default"}}
}

// TestNormalizeNumber_IsTheOneFormBothPathsUse pins the normalization rules on
// their own: the same function a hand-typed number goes through.
func TestNormalizeNumber_IsTheOneFormBothPathsUse(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"ABC123", "ABC-123"},                  // glued letter/digit run
		{"abc_123", "ABC-123"},                 // "_" is a separator
		{"abc 123", "ABC-123"},                 // so is a space
		{"SSIS-1", "SSIS-001"},                 // a short serial is padded
		{"ssis-1", "SSIS-001"},                 // and case never matters
		{"MD-0123", "MD-0123"},                 // padding is never truncation
		{"[SSIS-123]", "SSIS-123"},             // site brackets are not the number
		{"ＡＢＣ－１２３", "ABC-123"},                 // full-width to half-width
		{"FC2-PPV-3336019", "FC2-PPV-3336019"}, // the canonical FC2 form survives
		{"snis00440", "SNIS-440"},              // the per-prefix width table trims the pad
		{"SNIS-044", "SNIS-044"},               // but a serial of the right width stays
		{"ssis-1 ", "SSIS-001"},                // edges are trimmed
		{"hnvr00152", "HNVR-152"},              // VR labels print a three-digit serial
		{"sivr00525", "SIVR-525"},              // the same width applies to its siblings
		{"dsvr00123", "DSVR-123"},
		{"mdvr00123", "MDVR-123"},
	} {
		if got := NormalizeNumber(c.in); got != c.want {
			t.Errorf("NormalizeNumber(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestNormalizeNumber_HandTypedAndFileNameAgree is the ticket's own example: a
// number typed into the panel and the same number read off a file name have to be
// the same number, or a rerun creates a second output directory.
func TestNormalizeNumber_HandTypedAndFileNameAgree(t *testing.T) {
	if got, want := NormalizeNumber("ssis-1"), "SSIS-001"; got != want {
		t.Fatalf("NormalizeNumber(\"ssis-1\") = %q, want %q", got, want)
	}
	if NormalizeNumber("ssis-1") != NormalizeNumber("SSIS-1") {
		t.Fatal("a hand-typed number must not depend on its case")
	}
}

// TestParse_TheBuiltinSetIsTheFourFamilies pins the ticket's scope from the
// outside: one witness name per family, and a family V1 left out reads nothing.
func TestParse_TheBuiltinSetIsTheFourFamilies(t *testing.T) {
	for _, c := range []struct{ stem, family string }{
		{"FC2-PPV-3336019", "fc2"},
		{"010124-001", "date-code"},
		{"h_295ecr00074", "dmm-content-id"},
		{"SSIS-001", "generic"},
	} {
		got, numbers := numberFor(t, builtinRules(), c.stem)
		if len(numbers) != 1 || got.Family != c.family {
			t.Errorf("%q = %v (family %q), want the %s family", c.stem, numbers, got.Family, c.family)
		}
	}
	// TokyoHot is one of the families V1 left out: `n1234` is a single letter and
	// four digits, which none of the four families reads.
	if _, numbers := numberFor(t, builtinRules(), "n1234"); len(numbers) != 0 {
		t.Errorf("TokyoHot's n1234 must not resolve in V1: %v", numbers)
	}
}

// TestParse_BuiltinCorpus is the four families against the spellings real release
// trees use. Every case names its expected number, because a number that reads
// too wide is harder to notice than no number.
func TestParse_BuiltinCorpus(t *testing.T) {
	for _, c := range []struct{ stem, number, family string }{
		// generic, and the per-prefix width table behind it.
		{"SSIS-001", "SSIS-001", "generic"},
		{"ssis001", "SSIS-001", "generic"},
		{"snis00440hhb", "SNIS-440", "generic"},
		{"SNIS-440", "SNIS-440", "generic"},
		{"ABP-123", "ABP-123", "generic"},
		{"IPZZ-096C", "IPZZ-096", "generic"},
		{"9ofje121A", "OFJE-121", "generic"},
		{"9MIDA-198", "MIDA-198", "generic"},
		{"200GANA-1234", "GANA-1234", "generic"},

		// dmm content id: the maker id is DMM's bookkeeping and the serial is
		// padded one digit wider than the catalogue prints it. The `h_` token is
		// distinctive enough that a site's digit glued in front must not hide it
		// and hand the text to generic, which would keep the pad zero.
		{"h_295ecr00074", "ECR-0074", "dmm-content-id"},
		{"h_906gaso00061", "GASO-0061", "dmm-content-id"},
		{"9h_295ecr00074", "ECR-0074", "dmm-content-id"},

		// date codes: the site word in front is not part of the number.
		{"010124-001", "010124-001", "date-code"},
		{"caribbeancom-010124-001", "010124-001", "date-code"},

		// FC2 keeps the PPV token; the PPV-less spelling stays as spelled.
		{"FC2-PPV-3336019", "FC2-PPV-3336019", "fc2"},
		{"fc2-ppv-3336019", "FC2-PPV-3336019", "fc2"},
		{"fc2ppv1234567", "FC2-PPV-1234567", "fc2"},
		{"FC2-3336019", "FC2-3336019", "fc2"},
	} {
		got, numbers := numberFor(t, builtinRules(), c.stem)
		if got.Normalized != c.number {
			t.Errorf("%q = %v, want %q", c.stem, numbers, c.number)
			continue
		}
		if got.Family != c.family {
			t.Errorf("%q family = %q, want %q", c.stem, got.Family, c.family)
		}
		if got.Raw == "" {
			t.Errorf("%q: the decided number has no raw text", c.stem)
		}
	}
}

// TestParse_ContainmentBeatsRuleOrder is the evaluation order the ticket asks
// for: every rule runs, and the less specific reading of the same text drops out.
// The generic rule reads "fc2" inside "fc2ppv1234567" and would call it FC-002.
func TestParse_ContainmentBeatsRuleOrder(t *testing.T) {
	got, numbers := numberFor(t, builtinRules(), "fc2ppv1234567")
	if len(numbers) != 1 {
		t.Fatalf("numbers = %v, want exactly the FC2 reading", numbers)
	}
	if got.Normalized != "FC2-PPV-1234567" || got.Family != "fc2" {
		t.Fatalf("got %+v, want the FC2 reading and not FC-002", got)
	}
}

// TestParse_LessSpecificReadingDropsEvenWhenItComesFirst: position credits the
// earlier rule with a number, but it does not make a less specific reading win.
// The user rule here comes first and reads only `PPV-3336019`.
func TestParse_LessSpecificReadingDropsEvenWhenItComesFirst(t *testing.T) {
	rules := []config.ParseRule{
		{Pattern: `(?i)(ppv[-_ ]?\d+)`, Family: "user"},
		{Builtin: "default"},
	}
	got, numbers := numberFor(t, rules, "FC2-PPV-3336019")
	if len(numbers) != 1 || got.Normalized != "FC2-PPV-3336019" || got.Family != "fc2" {
		t.Fatalf("got %+v %v, want the FC2 reading: the PPV-only reading sits inside it", got, numbers)
	}
}

// TestParse_ContainmentIsPositional: two different numbers in one name are two
// numbers, even when one's text also occurs inside the other's. A substring test
// would silently drop the second and guess the first.
func TestParse_ContainmentIsPositional(t *testing.T) {
	_, numbers := numberFor(t, builtinRules(), "ABP-1234-ABP-123")
	if got, want := strings.Join(numbers, ", "), "ABP-123, ABP-1234"; got != want {
		t.Fatalf("numbers = %q, want %q", got, want)
	}
}

// TestParse_SecondNumberIsSeenAfterItsSeparator: a boundary written into the
// pattern as `(?:[^0-9]|$)` would eat the separator and hide the number after it,
// because FindAll cannot overlap. Both of these names carry two numbers.
func TestParse_SecondNumberIsSeenAfterItsSeparator(t *testing.T) {
	for _, c := range []struct{ stem, want string }{
		{"010124-001_010125-002", "010124-001, 010125-002"},
		{"h_295ecr00074-h_906gaso00061", "ECR-0074, GASO-0061"},
	} {
		_, numbers := numberFor(t, builtinRules(), c.stem)
		if got := strings.Join(numbers, ", "); got != c.want {
			t.Errorf("%q = %q, want %q", c.stem, got, c.want)
		}
	}
}

// TestParse_FullWidthNamesReachTheSameRule: folding happens before matching, not
// only inside normalization, or a name typed on a CJK keyboard resolves to
// nothing.
func TestParse_FullWidthNamesReachTheSameRule(t *testing.T) {
	for _, c := range []struct{ stem, want string }{
		{"ＡＢＣ－１２３", "ABC-123"},
		{"ＭＩＤＥ－１２３", "MIDE-123"},
	} {
		got, numbers := numberFor(t, builtinRules(), c.stem)
		if len(numbers) != 1 || got.Normalized != c.want {
			t.Errorf("%q = %v, want %q", c.stem, numbers, c.want)
		}
	}
}

// TestParse_ALongerSerialIsRefusedNotTruncated: each rule reads a bounded serial.
// A longer digit run must not be cut to the bound (that number belongs to another
// work), and the looser readings inside the refused text must not survive either.
func TestParse_ALongerSerialIsRefusedNotTruncated(t *testing.T) {
	for _, stem := range []string{
		"SSIS-123456",       // generic reads 1-5 digits
		"010124-0012",       // date-code reads MMDDYY-NNN
		"h_295ecr0007412",   // dmm content id reads 1-6
		"FC2-PPV-123456789", // FC2 reads 5-8, and generic must not fall back to FC-002
	} {
		if _, numbers := numberFor(t, builtinRules(), stem); len(numbers) != 0 {
			t.Errorf("%q = %v, want none: the serial is longer than the rule allows", stem, numbers)
		}
	}
}

// TestParse_ADateCodeMustStartItsToken pins the one leading boundary the rules
// need: a date code glued behind letters is not one.
func TestParse_ADateCodeMustStartItsToken(t *testing.T) {
	if _, numbers := numberFor(t, builtinRules(), "abc010124-001"); len(numbers) != 0 {
		t.Fatalf("numbers = %v, want none: the date code must start its token", numbers)
	}
}

// TestParse_TwoDifferentNumbersAreAmbiguous: a name that says two things the
// rules cannot rank by containment offers both, and neither is guessed.
func TestParse_TwoDifferentNumbersAreAmbiguous(t *testing.T) {
	_, numbers := numberFor(t, builtinRules(), "ABC-123_XYZ-456")
	want := "ABC-123, XYZ-456"
	if got := strings.Join(numbers, ", "); got != want {
		t.Fatalf("numbers = %q, want %q", got, want)
	}
}

// TestParse_RuleTableIsPositional: `{builtin: default}` expands where it stands,
// so a user pattern above it is credited first and one below it loses; a table
// that never says `builtin` uses no built-in rules at all.
func TestParse_RuleTableIsPositional(t *testing.T) {
	mine := config.ParseRule{Pattern: `(?i)(abc[-_ ]?\d+)`, Family: "mine"}

	got, numbers := numberFor(t, []config.ParseRule{mine, {Builtin: "default"}}, "ABC-123")
	if len(numbers) != 1 || got.Family != "mine" {
		t.Fatalf("a user rule above the built-in set must be credited first: %+v %v", got, numbers)
	}

	got, numbers = numberFor(t, []config.ParseRule{{Builtin: "default"}, mine}, "ABC-123")
	if len(numbers) != 1 || got.Family != "generic" {
		t.Fatalf("a user rule below the built-in set loses to it: %+v %v", got, numbers)
	}

	// No `builtin` anywhere: the built-in families do not exist for this table, so
	// a name only a builtin rule would read stays unnumbered.
	onlyMine := config.ParseRule{Pattern: `(?i)(xyz\d+)`, Family: "mine"}
	if _, numbers := numberFor(t, []config.ParseRule{onlyMine}, "ABC-123"); len(numbers) != 0 {
		t.Fatalf("rules without builtin must not use the built-in set: %v", numbers)
	}
}

// TestParse_PrefixTableMakesTwoSpellingsOneNumber is the ticket's acceptance
// example: the encoder's zero-padded spelling and the catalogue's must land on
// one number, or the same work gets two output directories.
func TestParse_PrefixTableMakesTwoSpellingsOneNumber(t *testing.T) {
	padded, _ := numberFor(t, builtinRules(), "snis00440hhb")
	plain, _ := numberFor(t, builtinRules(), "SNIS-440")
	if padded.Normalized != plain.Normalized {
		t.Fatalf("snis00440hhb = %q, SNIS-440 = %q, want the same number", padded.Normalized, plain.Normalized)
	}
}

// TestParse_NoRuleMatchOffersNothing: no candidate is a reason, not an error.
func TestParse_NoRuleMatchOffersNothing(t *testing.T) {
	if _, numbers := numberFor(t, builtinRules(), "hello"); len(numbers) != 0 {
		t.Fatalf("numbers = %v, want none", numbers)
	}
}
