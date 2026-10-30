package pipeline

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/2017fighting/mdc-lite/internal/config"
)

// parseStep reads a number, a part marker and version flags off each candidate
// file's name.
//
// It consumes the filter's Candidates one file at a time: `group` has not run yet
// (ADR-0019), so there is no stem to borrow and no sibling to agree a conditional
// part marker. What the step can settle on its own, it settles: it folds the name
// to half-width, strips noise and flags, takes a confident part marker off the
// end, and runs the rule table over what is left. A conditional marker (`ABC-123-1`)
// stays in the stem: it is only a part if a sibling agrees, and only `group` can see
// the siblings.
//
// The number evaluation order is not first-match-wins. Every rule runs, and the span
// arbitrates: a candidate whose match sits inside another candidate's longer match
// is the less specific reading of the same text and drops out (the generic rule
// seeing `PPV-3336019` inside `FC2-PPV-3336019`). A reading a stricter rule matched
// and then refused also drops the looser readings inside it: an over-long FC2
// serial must not survive as generic's `FC-002`. What is left is deduped by
// normalized number, so two rules agreeing is one answer rather than two.
type parseStep struct {
	rules     []numberRule
	strip     *stripper
	partRules []*regexp.Regexp
}

func newParse(w config.ParseWith) (Step, error) {
	rules, err := compileParseRules(w)
	if err != nil {
		return nil, err
	}
	strip, err := newStripper(w.Sanitize)
	if err != nil {
		return nil, err
	}
	s := &parseStep{rules: rules, strip: strip}
	for _, p := range w.PartPatterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("part_patterns %q: %w", p, err)
		}
		s.partRules = append(s.partRules, re)
	}
	return s, nil
}

// compileParseRules expands the positional rule table. `{builtin: default}`
// expands the built-in set where it stands, so a user pattern written above it is
// credited first; a table that never says `builtin` uses no built-in rules at all.
func compileParseRules(w config.ParseWith) ([]numberRule, error) {
	var out []numberRule
	for i, r := range w.Rules {
		if r.Builtin != "" {
			if r.Builtin != "default" {
				// config refuses this at load time; the engine says so too rather than
				// expanding the built-in set for any non-empty name.
				return nil, fmt.Errorf("rules[%d].builtin %q is not a builtin set (only \"default\")", i, r.Builtin)
			}
			out = append(out, builtinNumberRules...)
			continue
		}
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			return nil, fmt.Errorf("rules[%d].pattern: %w", i, err)
		}
		family := r.Family
		if family == "" {
			family = "custom"
		}
		out = append(out, numberRule{
			family: family,
			res:    []*regexp.Regexp{re},
			// A user pattern names its number with its first capture group; one
			// without a group means the whole match, whose edges normalisation trims.
			group: min(1, re.NumSubexp()),
		})
	}
	return out, nil
}

func (s *parseStep) Type() string { return config.StepParse }

func (s *parseStep) Name() string { return "" }

func (s *parseStep) Requires() []string { return []string{config.ArtifactCandidates} }

func (s *parseStep) Produces() []string { return []string{config.ArtifactParsed} }

func (s *parseStep) Run(ctx context.Context, st *State) error {
	if st.Candidates == nil {
		return fmt.Errorf("no Candidates to parse")
	}
	out := &Parsed{}
	summary := &ParseSummary{
		Files:            len(st.Candidates.Files),
		Unresolved:       newCounts(),
		CandidateNumbers: map[string][]string{},
	}
	for _, c := range st.Candidates.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.parseCandidate(c.File, out, summary)
	}
	st.Parsed = out
	st.ParseRun = summary
	return nil
}

// parseCandidate reads one file's name. One number resolves it, several make it
// ambiguous, none sends it to a human with the reason that applies.
func (s *parseStep) parseCandidate(file FileRef, out *Parsed, summary *ParseSummary) {
	name := toHalfWidth(stemOf(file.Path))
	flags := versionFlags(name)
	stripped := s.strip.strip(name)
	t := partAt(stripped, s.partRules)
	// A conditional marker is not evidence on its own: `ABC-123-1` is work
	// `ABC-123-1` until a sibling says otherwise, and only `group` sees siblings.
	// So the stem keeps it, and the part index stays zero.
	part := Marker{}
	if t.tier == tierConfident {
		part = t.marker()
		stripped = t.stem
	}

	matched, refused := s.candidates(stripped)
	cands := adjudicate(matched, refused)
	switch len(cands) {
	case 1:
		out.Items = append(out.Items, ParsedCandidate{
			File:   file,
			Stem:   stripped,
			Number: numberFrom(cands, cands[0].number),
			Part:   part,
			Flags:  flags,
		})
		summary.Resolved++
		if part.Index > 0 {
			summary.Parts++
		}
		if flags.IsChinese {
			summary.Chinese++
		}
		if flags.IsUncensored {
			summary.Uncensored++
		}
	case 0:
		s.unresolve(out, summary, file, stripped, ReasonNoCandidate, nil)
	default:
		s.unresolve(out, summary, file, stripped, ReasonAmbiguous, numbersOf(cands))
	}
}

// unresolve records one file a human has to fill in, and the numbers it offered
// so the panel can show them as chips.
func (s *parseStep) unresolve(out *Parsed, summary *ParseSummary, file FileRef, stem, reason string, candidates []string) {
	out.Unresolved = append(out.Unresolved, Unresolved{
		Dir:        filepath.Dir(file.Path),
		Stem:       stem,
		Reason:     reason,
		Files:      []FileRef{file},
		Candidates: candidates,
	})
	summary.Unresolved[reason]++
	if len(candidates) > 0 && len(summary.CandidateNumbers) < maxCandidateFiles {
		summary.CandidateNumbers[file.Path] = append([]string(nil), candidates...)
	}
}

// candidates runs every rule over one stem. It returns what each match read, and
// the spans a rule matched and then refused: the refusal is how a strict rule
// takes a looser rule's reading of the same text down with it.
func (s *parseStep) candidates(stem string) (cands []numberCandidate, refused []span) {
	for _, r := range s.rules {
		for _, re := range r.res {
			for _, loc := range re.FindAllStringSubmatchIndex(stem, -1) {
				c, ok := r.read(stem, loc)
				if !ok {
					refused = append(refused, span{start: loc[0], end: loc[1]})
					continue
				}
				cands = append(cands, c)
			}
		}
	}
	return cands, refused
}

// read turns one regexp match into a candidate, applying the rule's token
// boundaries, its family spelling and the serial widths. The boundaries are
// checked here rather than written into the patterns because a pattern that ends
// on `(?:[^0-9]|$)` eats the separator after the number, and FindAll cannot
// overlap, so the next number in a name like `010124-001_010125-002` would never
// be seen.
func (r numberRule) read(stem string, loc []int) (numberCandidate, bool) {
	g := 2 * r.group
	if g+1 >= len(loc) || loc[g] < 0 {
		return numberCandidate{}, false
	}
	if r.leading && loc[0] > 0 && isAlnumByte(stem[loc[0]-1]) {
		return numberCandidate{}, false
	}
	if numberEnd := loc[g+1]; r.trailing && numberEnd < len(stem) && isDigit(stem[numberEnd]) {
		// A longer digit run than the rule allows: rejecting beats truncating it
		// into a number no site has.
		return numberCandidate{}, false
	}
	match := stem[loc[0]:loc[1]]
	raw := stem[loc[g]:loc[g+1]]
	number := NormalizeNumber(raw)
	if r.canonical != nil {
		number = r.canonical(match, raw)
	}
	if number == "" {
		return numberCandidate{}, false
	}
	number = trimSerialPadding(number, r.serialWidth)
	number = trimSerialPaddingByPrefix(number)
	return numberCandidate{
		start:  loc[0],
		end:    loc[1],
		raw:    raw,
		number: number,
		family: r.family,
	}, true
}

// span is a stretch of the stem: the text one regexp match covered.
type span struct {
	start, end int
}

// covers reports whether outer encloses inner. When strict is set, outer also has
// to be longer, which is what makes it the less specific reading; against a span
// a rule refused, an equal span drops too — the same text was declined.
func (s span) covers(inner span, strict bool) bool {
	if s.start > inner.start || inner.end > s.end {
		return false
	}
	return !strict || s.end-s.start > inner.end-inner.start
}

// numberCandidate is one rule's reading of one piece of text. start/end are the
// match's span in the stem, which is what containment arbitration compares; raw
// is the captured number text, which is what Number.Raw shows.
type numberCandidate struct {
	start, end int
	raw        string
	number     string
	family     string
}

// adjudicate reduces the candidates to the numbers that actually competed. A
// candidate whose match sits inside a longer match's span is the less specific
// reading of the same text (the generic rule inside FC2's, the bare date code
// inside the site-prefixed one), so it drops out; so does one that sits inside a
// span a rule matched and refused. Identical numbers collapse. Rule order is kept,
// so the earliest rule that produced a number is the one credited for it.
//
// The comparison is positional, not textual: `ABP-1234-ABP-123` offers two
// genuinely different numbers, and a substring test would drop the second one
// because its text appears inside the first's.
func adjudicate(cands []numberCandidate, refused []span) []numberCandidate {
	kept := make([]numberCandidate, 0, len(cands))
	for i, c := range cands {
		mine := span{start: c.start, end: c.end}
		contained := false
		for j, other := range cands {
			if i == j {
				continue
			}
			if (span{start: other.start, end: other.end}).covers(mine, true) {
				contained = true
				break
			}
		}
		for _, dead := range refused {
			if dead.covers(mine, false) {
				contained = true
				break
			}
		}
		if !contained {
			kept = append(kept, c)
		}
	}
	seen := map[string]bool{}
	out := kept[:0]
	for _, c := range kept {
		if seen[c.number] {
			continue
		}
		seen[c.number] = true
		out = append(out, c)
	}
	return out
}

// numbersOf is the sorted number list a candidate set offers: one entry per
// distinct number, which is what the number panel shows as chips.
func numbersOf(cands []numberCandidate) []string {
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.number
	}
	sort.Strings(out)
	return out
}

// numberFrom is the Number for one decided number: its spelling and family come
// from the first candidate that produced it.
func numberFrom(cands []numberCandidate, number string) Number {
	for _, c := range cands {
		if c.number == number {
			return Number{Raw: c.raw, Normalized: c.number, Family: c.family}
		}
	}
	return Number{Normalized: number}
}

// versionFlags reads the version evidence off a file name. The single-letter
// tokens are matched only after a separator, so a title that starts with `C` or
// `U` is not a version, and an `-cd1` part marker never reads as `-c`.
func versionFlags(name string) VersionFlags {
	var f VersionFlags
	lower := strings.ToLower(name)
	if strings.Contains(lower, "中文") || strings.Contains(lower, "中字") {
		f.IsChinese = true
	}
	for _, word := range []string{"uncensored", "leak", "無碼", "无码", "破解", "流出"} {
		if strings.Contains(lower, word) {
			f.IsUncensored = true
		}
	}
	fields := strings.FieldsFunc(lower, func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == ' ' || r == '\t' || r == '[' || r == ']' || r == '(' || r == ')'
	})
	for i, token := range fields {
		if i == 0 {
			// A leading `C`/`U` is the work's prefix (`C-123`), not a version flag.
			continue
		}
		switch token {
		case "c", "ch", "chs", "cht":
			f.IsChinese = true
		case "u":
			f.IsUncensored = true
		case "uc", "cu":
			f.IsChinese = true
			f.IsUncensored = true
		}
	}
	return f
}
