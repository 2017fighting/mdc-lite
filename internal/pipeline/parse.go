package pipeline

import (
	"context"
	"fmt"
	"regexp"
	"sort"

	"github.com/2017fighting/mdc-lite/internal/config"
)

// parseStep turns each group's stems into a number.
//
// `group` has already decided which files belong together and stripped each name
// down to its stem, so this step has one job: run the rule table over every video
// in the group, decide one number from what came back, and hand anything it
// cannot decide to a human instead of guessing.
//
// The evaluation order is not first-match-wins. Every rule runs, and the span
// arbitrates: a candidate whose match sits inside another candidate's longer match
// is the less specific reading of the same text and drops out (the generic rule
// seeing `PPV-3336019` inside `FC2-PPV-3336019`). A reading a stricter rule matched
// and then refused also drops the looser readings inside it: an over-long FC2
// serial must not survive as generic's `FC-002`. What is left is deduped by
// normalized number, so two rules agreeing is one answer rather than two.
type parseStep struct {
	rules []numberRule
}

func newParse(w config.ParseWith) (Step, error) {
	rules, err := compileParseRules(w)
	if err != nil {
		return nil, err
	}
	return &parseStep{rules: rules}, nil
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
	if st.Groups == nil {
		return fmt.Errorf("no Groups to parse")
	}
	out := &Parsed{}
	summary := &ParseSummary{
		Groups:           len(st.Groups.Groups),
		Unresolved:       newCounts(),
		CandidateNumbers: map[string][]string{},
		conflicts:        map[string]bool{},
	}
	for _, grp := range st.Groups.Groups {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.parseGroup(grp, out, summary); err != nil {
			return err
		}
	}
	// A duplicate stem never became a group, so it was never parsed: it goes to a
	// human as-is, with the reason group gave it.
	for _, dup := range st.Groups.Duplicates {
		out.Unresolved = append(out.Unresolved, Unresolved{
			Dir:    dup.Dir,
			Stem:   dup.Stem,
			Reason: ReasonDuplicateStem,
			Files:  dup.Files,
		})
		summary.Unresolved[ReasonDuplicateStem]++
	}
	summary.Conflicts = sortedKeys(summary.conflicts)
	st.Parsed = out
	st.ParseRun = summary
	return nil
}

// parseGroup decides one group's number and records what the other parts read.
func (s *parseStep) parseGroup(grp Group, out *Parsed, summary *ParseSummary) error {
	if len(grp.Parts) == 0 {
		// `group` cannot emit one, so this is a broken state rather than a group to
		// number; saying so beats both a panic and a group that vanishes from both
		// halves of the result.
		return fmt.Errorf("group in %s has no parts to parse", grp.Dir)
	}
	parsed := ParsedGroup{Group: grp}
	var every []numberCandidate
	var union []string
	for _, p := range grp.Parts {
		// group already folded the stem; folding again is idempotent and keeps the
		// step correct if it is ever handed a stem from somewhere else.
		matched, refused := s.candidates(toHalfWidth(p.Stem))
		cands := adjudicate(matched, refused)
		numbers := numbersOf(cands)
		parsed.Parts = append(parsed.Parts, numbers)
		union = mergeNumbers(union, numbers)
		every = append(every, cands...)
		summary.Parts++
	}

	// Parts[0] is the primary: group sorts parts by part number, and the smallest
	// one decides the number and the output directory's name.
	primary := parsed.Parts[0]
	switch {
	case len(primary) == 1:
		parsed.Number = numberFrom(every, primary[0])
	case len(primary) > 1:
		// The primary's own text offers several numbers and none of them is the
		// less specific reading of another: ask, do not guess.
		s.unresolve(out, summary, grp, ReasonAmbiguous, primary, parsed.Parts)
		return nil
	case len(union) == 1:
		// The primary produced nothing, and the rest of the group agreed on one
		// number: take it, and say out loud that the primary is what failed.
		parsed.Number = numberFrom(every, union[0])
		parsed.PrimaryFailed = true
		summary.PrimaryFailed++
	case len(union) == 0:
		s.unresolve(out, summary, grp, ReasonNoCandidate, nil, parsed.Parts)
		return nil
	default:
		s.unresolve(out, summary, grp, ReasonAmbiguous, union, parsed.Parts)
		return nil
	}

	// Divergence is recorded, not a failure: a part that reads another number (or
	// several) is a hint that the group is stitched together, and the summary
	// carries it for a human to look at.
	for _, n := range conflictsOf(parsed) {
		summary.conflicts[n] = true
	}
	out.Items = append(out.Items, parsed)
	summary.Resolved++
	return nil
}

// unresolve records a group a human has to fill in, and the numbers each of its
// files offered so the panel can show them as chips.
func (s *parseStep) unresolve(out *Parsed, summary *ParseSummary, grp Group, reason string, candidates []string, parts [][]string) {
	files := make([]FileRef, len(grp.Parts))
	for i, p := range grp.Parts {
		files[i] = p.File
	}
	out.Unresolved = append(out.Unresolved, Unresolved{
		Dir:        grp.Dir,
		Stem:       grp.PrimaryStem,
		Reason:     reason,
		Files:      files,
		Candidates: candidates,
	})
	summary.Unresolved[reason]++
	s.recordCandidates(summary, grp, candidates, parts)
}

// recordCandidates files one unresolved group's numbers under the paths they came
// from. A file's own reading is what its row offers; the primary falls back to the
// group's union when its own text offered nothing, because that is the set the
// human is being asked to choose from.
func (s *parseStep) recordCandidates(summary *ParseSummary, grp Group, union []string, parts [][]string) {
	for i, p := range grp.Parts {
		if len(summary.CandidateNumbers) >= maxCandidateFiles {
			return
		}
		var numbers []string
		if i < len(parts) {
			numbers = parts[i]
		}
		if i == 0 && len(numbers) == 0 {
			numbers = union
		}
		if len(numbers) == 0 {
			continue
		}
		summary.CandidateNumbers[p.File.Path] = append([]string(nil), numbers...)
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

// conflictsOf is the sorted set of numbers the group's other parts read that the
// decided number is not. It is the `conflict` list of the parse summary.
func conflictsOf(parsed ParsedGroup) []string {
	seen := map[string]bool{}
	var out []string
	for _, numbers := range parsed.Parts[1:] {
		for _, n := range numbers {
			if n == parsed.Number.Normalized || seen[n] {
				continue
			}
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// mergeNumbers unions two sorted number lists.
func mergeNumbers(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	for _, s := range a {
		seen[s] = true
	}
	for _, s := range b {
		seen[s] = true
	}
	return sortedKeys(seen)
}

// sortedKeys is the sorted key set of a set map.
func sortedKeys(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
