package pipeline

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// This file is the one place a file name is stripped. `group` calls it once per
// file and puts the result into the artifacts (`{stem, marker{text, index,
// confident}}`); `parse` reads the stem, it never re-derives it. Splitting it out
// is what makes the rules testable on their own.

// markerTier says how much a candidate marker can be trusted.
type markerTier int

const (
	// tierNone: the name ends in nothing that could be a part number.
	tierNone markerTier = iota
	// tierConditional: the ending looks like a part number, but it is only one if a
	// sibling in the same directory agrees. `ABC-123-1.mp4` on its own is a work
	// numbered 123-1; next to `ABC-123-2.mp4` it is part 1.
	tierConditional
	// tierConfident: the ending says "part" out loud (`cd1`, `disc2`, `-A`), so it
	// counts with no help from its neighbours.
	tierConfident
)

// tail is one candidate marker: what was matched, the part number it names, and
// what is left of the name once it is taken off.
type tail struct {
	stem  string
	text  string
	index int
	tier  markerTier
}

// builtinNoise is the noise always stripped off a name, on top of the user's
// `sanitize` table. Each token rule matches its own separator and puts it back
// (`$1`), so `ABC-1080p-123` loses the resolution without gluing the number onto
// the prefix. Go's regexp has no lookahead, hence the capture-and-restore shape.
var builtinNoise = []*regexp.Regexp{
	// ad domains, usually sitename.com, with or without brackets or a trailing @
	regexp.MustCompile(`(?i)\[?(?:[a-z0-9][a-z0-9-]*\.)+(?:com|net|org|cc|tv|me|tw|xyz|top|info|io|co|jp|us|vip|club)\]?@?`),
	// technical tokens
	regexp.MustCompile(`(?i)(^|[-_. ])(?:x?26[45]|h\.?26[45]|hevc|avc|vp9|av1|aac|ac3|dts(?:-hd)?|1[08]bit|hdr(?:10)?|[23]0fps|60fps)([-_. ]|$)`),
	// resolution tokens
	regexp.MustCompile(`(?i)(^|[-_. ])(?:4k|8k|fhd|uhd|hd|\d{3,4}[pi])([-_. ]|$)`),
	// flags: -C (chinese subtitles) and -U (uncensored) are neither parts nor fields
	regexp.MustCompile(`(?i)(^|[-_. ])(?:u|c)([-_. ]|$)`),
	// the same flags written out
	regexp.MustCompile(`(?i)(^|[-_. ])(?:uncensored|leak(?:ed)?|无码|破解|流出|中字|中文字幕|字幕)([-_. ]|$)`),
	// subtitle tails
	regexp.MustCompile(`(?i)(^|[-_. ])(?:chs|cht|sc|tc|eng|jpn|jp|kor|简体|繁体|简|繁)([-_. ]|$)`),
}

// noiseKeep is the replacement every token rule uses: the separator it ate.
const noiseKeep = "$1"

// The two marker tables of §4. The confident tier is the unconditional one; the
// conditional tier is the two shapes that need a sibling's agreement.
var (
	confidentNumbered = regexp.MustCompile(`(?i)(?:^|[-_. ])(?:cd|disc|part|vol)[-_. ]?(\d{1,2})$`)
	confidentLetter   = regexp.MustCompile(`(?:^|[-_. ])([ABD])$`)
	conditionalDigit  = regexp.MustCompile(`([-_. ]?)(\d{1,2})$`)
	conditionalLetter = regexp.MustCompile(`([ABD])$`)
)

// stripper removes noise from a base name: the user's `sanitize` table first,
// then the built-in tables, repeated until nothing changes.
type stripper struct {
	extra []*regexp.Regexp
}

func newStripper(sanitize []string) (*stripper, error) {
	s := &stripper{}
	for _, entry := range sanitize {
		src := regexp.QuoteMeta(entry)
		if rest, ok := strings.CutPrefix(entry, "r:"); ok {
			src = rest
		}
		re, err := regexp.Compile("(?i)" + src)
		if err != nil {
			return nil, fmt.Errorf("sanitize %q: %w", entry, err)
		}
		s.extra = append(s.extra, re)
	}
	return s, nil
}

// strip shears the noise off. It repeats because the tables see the name one
// token at a time and a token can be uncovered by removing the one after it
// (`ABC-123-cd1-1080p` only shows `-1080p` after `-1080p` is gone). Four passes
// is more than any real name needs; the loop stops as soon as nothing changes.
func (s *stripper) strip(base string) string {
	// Fold full-width to half-width here, where the stem is made: two spellings of
	// one name have to be one stem, or group's equality and duplicate-stem rules
	// see two works and the same title gets two output directories.
	out := toHalfWidth(base)
	for pass := 0; pass < 4; pass++ {
		before := out
		for _, re := range s.extra {
			out = re.ReplaceAllString(out, "")
		}
		for _, re := range builtinNoise {
			out = re.ReplaceAllString(out, noiseKeep)
		}
		out = trimSeparators(out)
		if out == before {
			break
		}
	}
	return out
}

// marker converts an effective tail into the artifact's Marker. A tail that did
// not survive (bad tier) yields the zero Marker, i.e. "this name claims nothing".
func (t tail) marker() Marker {
	if t.tier == tierNone {
		return Marker{}
	}
	return Marker{Text: t.text, Index: t.index, Confident: t.tier == tierConfident}
}

// partAt reads the marker off the end of s. When there is none, the whole name is
// the stem. `extra` holds the user's `part_patterns`: confident markers whose
// first capture group is the part number.
func partAt(s string, extra []*regexp.Regexp) tail {
	for _, re := range extra {
		if loc := re.FindStringSubmatchIndex(s); loc != nil && loc[1] == len(s) && loc[3] > loc[2] {
			if idx, err := strconv.Atoi(s[loc[2]:loc[3]]); err == nil && idx > 0 {
				return tail{stem: trimSeparators(s[:loc[0]]), text: s[loc[0]:loc[1]], index: idx, tier: tierConfident}
			}
		}
	}
	if loc := confidentNumbered.FindStringSubmatchIndex(s); loc != nil {
		if idx, err := strconv.Atoi(s[loc[2]:loc[3]]); err == nil && idx > 0 {
			return tail{
				stem:  trimSeparators(s[:loc[0]]),
				text:  trimSeparators(s[loc[0]:loc[1]]),
				index: idx,
				tier:  tierConfident,
			}
		}
	}
	if loc := confidentLetter.FindStringSubmatchIndex(s); loc != nil {
		letter := s[loc[2]:loc[3]]
		return tail{stem: trimSeparators(s[:loc[0]]), text: letter, index: letterIndex(letter), tier: tierConfident}
	}
	// A letter glued to the number (`9ofje121A`) is conditional: the A may be part
	// of the code. C is never a part — a C copy is a duplicate version, and `parse`
	// strips it as a flag.
	if loc := conditionalLetter.FindStringSubmatchIndex(s); loc != nil {
		start := loc[0]
		if start > 0 && isDigit(s[start-1]) {
			letter := s[loc[2]:loc[3]]
			return tail{stem: trimSeparators(s[:start]), text: letter, index: letterIndex(letter), tier: tierConditional}
		}
	}
	// A trailing 1-2 digit number is conditional too: `ABC-123-1` alone is work
	// 123-1, `ABC-123-1` next to `ABC-123-2` is part 1.
	if loc := conditionalDigit.FindStringSubmatchIndex(s); loc != nil {
		start := loc[0]
		// `ABC-1234` must not read as part 34, so a bare tail may not be preceded by
		// a digit; with a separator present the number is a token of its own.
		if loc[2] == loc[3] && start > 0 && isDigit(s[start-1]) {
			return tail{stem: s, tier: tierNone}
		}
		if idx, err := strconv.Atoi(s[loc[4]:loc[5]]); err == nil && idx > 0 {
			return tail{stem: trimSeparators(s[:start]), text: s[loc[4]:loc[5]], index: idx, tier: tierConditional}
		}
	}
	return tail{stem: s, tier: tierNone}
}

// letterIndex maps a part letter to its number. Only A, B and D are part
// letters; C is reserved for "chinese subtitles" and is a duplicate version.
func letterIndex(letter string) int {
	switch strings.ToUpper(letter) {
	case "A":
		return 1
	case "B":
		return 2
	case "D":
		return 4
	}
	return 0
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func trimSeparators(s string) string {
	return strings.Trim(s, "-_. \t")
}
