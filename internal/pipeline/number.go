package pipeline

import (
	"regexp"
	"strings"
)

// This file is the number side of `parse`: the built-in rule set, the
// adjudication of what several rules read off one name, and the normalization
// that makes a hand-typed number and a file name land on the same form.
//
// `parse` strips the noise off every name before a rule sees it (the stripper in
// stem.go), so nothing here sanitizes anything. This file is a pure function of
// the rule table, the stripped stem, and the per-prefix width table below.

// serialWidths is the per-prefix serial width table: the width the catalogues
// print a family's serial at, keyed by the number's prefix.
//
// It exists because the generic rule reads whatever width a file name spelled
// and nothing in `snis00440hhb` says the family's serial is three digits; without
// the table the name becomes SNIS-00440, and a wrong number is harder to notice
// than no number at all. The trim only drops leading zeros, so SNIS-044 stays
// SNIS-044 and the four-digit families are unharmed.
//
// Only families measured against a catalogue belong here. A family that is not
// listed keeps the width its name spelled, because the cost of an omission is a
// number that reads too wide, while the cost of a wrong entry is a number no
// site has.
var serialWidths = map[string]int{
	"SSIS":  3,
	"SNIS":  3,
	"IPZZ":  3,
	"OFJE":  3,
	"CAWD":  3,
	"SONE":  3,
	"FSDSS": 3,
	"DEVR":  3,
	// VR labels print a three-digit serial; the same release is spelled
	// `hnvr00152`, `HNVR-152` and `hnvr-152`, and only the table makes them one
	// number instead of HNVR-00152.
	"HNVR": 3,
	"SIVR": 3,
	"DSVR": 3,
	"MDVR": 3,
}

// NormalizeNumber canonicalises a number into the stored form. It is exported
// because a hand-typed number has to land on exactly the form a file name does —
// `ssis-1` and `SSIS-1` are both `SSIS-001` — and the number panel and `parse`
// consulting two different tables is how they would drift apart.
//
// The rules, in order: full-width to half-width (so a CJK keyboard's `－` is a
// hyphen), uppercase, the edges trimmed, `_` and spaces to `-`, a hyphen inserted
// into a glued letter/digit run (`ABC123` -> `ABC-123`), the trailing digit run
// padded to three digits but never shortened (`SSIS-1` -> `SSIS-001`, `MD-0123`
// keeps its four), and finally the per-prefix width table.
func NormalizeNumber(raw string) string {
	s := toHalfWidth(raw)
	s = strings.ToUpper(s)
	s = trimNumberEdges(s)
	s = strings.NewReplacer("_", "-", " ", "-", "\t", "-").Replace(s)
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	s = insertGluedHyphen(s)
	s = padSerial(s)
	return trimSerialPaddingByPrefix(s)
}

// toHalfWidth folds the full-width ASCII block (U+FF01–U+FF5E) onto ASCII, plus
// the ideographic space. It runs before matching so a full-width name reaches
// the same rule as its half-width spelling.
func toHalfWidth(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\u3000':
			return ' '
		case r >= '\uFF01' && r <= '\uFF5E':
			return r - 0xFEE0
		default:
			return r
		}
	}, s)
}

// trimNumberEdges strips leading and trailing characters that cannot be part of
// a number: the brackets and separators a rule's match carries around it.
func trimNumberEdges(s string) string {
	isNumberChar := func(r rune) bool {
		return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
	}
	return strings.TrimFunc(s, func(r rune) bool { return !isNumberChar(r) })
}

// trailingDigits matches the final run of digits, which is the part of a number
// that carries the serial.
var trailingDigits = regexp.MustCompile(`\d+$`)

// insertGluedHyphen puts a hyphen at the letter-to-digit transition when the
// serial is glued to the letters (`ABC123` -> `ABC-123`). A name that already
// separates them is left alone, which is why `FC2-PPV-123` keeps its punctuation.
func insertGluedHyphen(s string) string {
	loc := trailingDigits.FindStringIndex(s)
	if loc == nil || loc[0] == 0 {
		return s
	}
	i := loc[0]
	if isLetterByte(s[i-1]) {
		return s[:i] + "-" + s[i:]
	}
	return s
}

// padSerial pads a short final digit run to three digits. Digits are never
// dropped: `MD-0123` keeps its four, because a leading zero can be the family's
// own convention (the per-prefix table is the one place that knows better).
func padSerial(s string) string {
	loc := trailingDigits.FindStringIndex(s)
	if loc == nil {
		return s
	}
	digits := s[loc[0]:]
	for len(digits) < 3 {
		digits = "0" + digits
	}
	return s[:loc[0]] + digits
}

func isLetterByte(c byte) bool { return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' }

// isAlnumByte reports whether c is ASCII alphanumeric. A UTF-8 continuation byte
// is not, so a CJK character counts as a token boundary, which is what the rules
// want.
func isAlnumByte(c byte) bool { return isLetterByte(c) || isDigit(c) }

// numberPrefix is the number without its trailing numeric segment: the key the
// per-prefix table is read with (`FC2-PPV-1236019` -> `FC2-PPV`, `SSIS-001` ->
// `SSIS`). A number with no separator has no prefix.
func numberPrefix(number string) string {
	i := strings.LastIndexByte(number, '-')
	if i <= 0 {
		return ""
	}
	return number[:i]
}

// trimSerialPadding drops the pad zeros a source added to a serial, down to the
// width the number itself uses. Only leading zeros go, and only while the run is
// wider than the width: a serial the source did not over-pad (`NTR-082`) keeps
// the width it arrived with.
func trimSerialPadding(number string, width int) string {
	if width < 1 {
		return number
	}
	loc := trailingDigits.FindStringIndex(number)
	if loc == nil {
		return number
	}
	run := number[loc[0]:]
	for len(run) > width && run[0] == '0' {
		run = run[1:]
	}
	return number[:loc[0]] + run
}

// trimSerialPaddingByPrefix is trimSerialPadding with the width looked up by the
// number's prefix. A prefix the table does not declare is left exactly as it is.
func trimSerialPaddingByPrefix(number string) string {
	width, ok := serialWidths[numberPrefix(number)]
	if !ok {
		return number
	}
	return trimSerialPadding(number, width)
}

// numberRule is one compiled rule from the parse rule table: a family label, the
// pattern(s) it matches with, which capture group holds the number, the token
// boundaries the match may not cross, and the family's own spelling corrections.
type numberRule struct {
	family string
	// res holds one rule's spellings. date-code has two — with a site word in
	// front and bare — because either is the same family and the site word is
	// not part of the number.
	res []*regexp.Regexp
	// group is the capture group holding the number. Group 0 (the whole match)
	// is only safe for a rule that has a `canonical` spelling to rebuild it
	// with, because otherwise the edge characters would end up inside the number.
	group int
	// leading requires the match to start a token: the character before it may not
	// be a letter or a digit. It is off for the families that legitimately sit
	// behind glued noise (generic tolerates a site's digit in front, FC2 its own
	// label).
	leading bool
	// trailing requires the number not to be followed by another digit, so a
	// serial longer than the pattern allows is rejected rather than truncated into
	// a number no site has. It is off for user patterns, which are the escape
	// hatch: a pattern that deliberately captures a prefix of a longer digit run is
	// the operator's call.
	trailing bool
	// serialWidth is the width a rule's own source padded its serial to and the
	// number should be trimmed down to, or 0 when the rule has nothing to say about
	// it. DMM's content id is the case: it pads to five digits while the catalogues
	// print four, so the field says 4 and `00061` becomes `0061`.
	serialWidth int
	// canonical is the family's own spelling rule, applied instead of the plain
	// normalization. FC2 is the only family with one.
	canonical func(match, raw string) string
}

// builtinNumberRules is the built-in rule set: the four families V1 knows, in
// the order `{builtin: default}` expands them. Everything else (TokyoHot,
// label-prefix, the single-digit site tag, the extended families) is out of V1.
//
// The patterns carry no boundary characters of their own: `(?:^|[^0-9])` would
// consume the separator in front of the match, and FindAll cannot overlap, so the
// number right after it would be invisible. The boundaries are checked against
// the stem in code instead.
var builtinNumberRules = []numberRule{
	{
		// FC2's releases are the one family whose canonical form keeps the PPV
		// token, because that is the number the whole world indexes them under.
		family: "fc2",
		res:    []*regexp.Regexp{regexp.MustCompile(`(?i)fc2[-_ ]?(?:ppv[-_ ]?)?\d{5,8}`)},
		// The whole match is the number here, and the canonical spelling puts the
		// separators back, so group 0 carries no edge characters.
		group:     0,
		trailing:  true,
		canonical: fc2Spelling,
	},
	{
		// The date-code families (caribbeancom, 1pondo, 10musume, pacopacomama)
		// spell themselves MMDDYY-NNN, sometimes with the site word in front. The
		// site word is not part of the number: both readings end up 010124-001.
		family: "date-code",
		res: []*regexp.Regexp{
			regexp.MustCompile(`(?i)(?:caribbeancom|carib|1pondo|10musume|pacopacomama)[-_. ]?(\d{6}[-_]\d{3})`),
			regexp.MustCompile(`(\d{6}[-_]\d{3})`),
		},
		group:    1,
		leading:  true,
		trailing: true,
	},
	{
		// A DMM content id: h_<maker id><series><serial>, with the serial padded one
		// digit wider than the number the catalogues print. The maker id is DMM's
		// own bookkeeping, so the capture starts at the series; serial_width drops
		// the pad zero DMM added. `h_\d{3}` is an unambiguous token on its own, so it
		// needs no leading boundary: a site's digit glued in front (`9h_295ecr00074`)
		// must not hide the id and hand the text to generic, which would keep the pad
		// zero and produce a wrong number.
		family: "dmm-content-id",
		res: []*regexp.Regexp{
			regexp.MustCompile(`(?i)h_\d{3}([a-z]{2,6}\d{1,6})`),
		},
		group:       1,
		trailing:    true,
		serialWidth: 4,
	},
	{
		// The workhorse: letters plus digits, with an optional separator. It
		// deliberately has no leading boundary, because a download site's own digit
		// glued in front (`9ofje121`, `9MIDA-198`) is not part of the number and the
		// rule should still read it. Its trailing boundary stops a glued release
		// suffix (`IPZZ-096C`, `snis00440hhb`) from being swallowed.
		family: "generic",
		res: []*regexp.Regexp{
			regexp.MustCompile(`(?i)([a-z]{2,10}[-_ ]?\d{1,5})`),
		},
		group:    1,
		trailing: true,
	},
}

// fc2Spelling rebuilds an FC2 number in its canonical form: `FC2-PPV-3336019`
// when the name said PPV, `FC2-3336019` when it did not. The PPV-less form is
// kept because it is a different spelling that the sources rewrite for their own
// lookups; inventing the token here would hide that from them.
func fc2Spelling(match, _ string) string {
	loc := trailingDigits.FindStringIndex(match)
	if loc == nil {
		return ""
	}
	if strings.Contains(strings.ToLower(match), "ppv") {
		return "FC2-PPV-" + match[loc[0]:]
	}
	return "FC2-" + match[loc[0]:]
}
