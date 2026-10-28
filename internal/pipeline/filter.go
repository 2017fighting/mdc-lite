package pipeline

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/2017fighting/mdc-lite/internal/config"
)

// filterStep turns the FileSet into candidates by walking an ordered rule list
// and letting the first match decide. Order is the whole semantics: a catch-all
// at the bottom means "everything the rules above did not claim is out".
//
// Extensions are judged here and only here. That is why scan does not
// pre-filter them, and why the same extension appearing both in a filter rule
// and in `companion_extensions` is normal: one decides whether the file enters
// the pipeline, the other whether `group` treats it as a companion.
type filterStep struct {
	cfg   config.FilterWith
	rules []filterRule
}

// filterRule is one compiled rule. `when`'s four keys are mutually exclusive, so
// a rule has at most one of them.
type filterRule struct {
	name     string
	include  bool
	extIn    map[string]bool
	extNotIn map[string]bool
	minSize  *int64
	stem     *regexp.Regexp
}

func newFilter(w config.FilterWith) (Step, error) {
	s := &filterStep{cfg: w}
	for _, r := range w.Rules {
		cr := filterRule{name: r.Name, include: r.Then == "include"}
		if len(r.When.ExtIn) > 0 {
			cr.extIn = map[string]bool{}
			for _, e := range r.When.ExtIn {
				cr.extIn[strings.ToLower(e)] = true
			}
		}
		if len(r.When.ExtNotIn) > 0 {
			cr.extNotIn = map[string]bool{}
			for _, e := range r.When.ExtNotIn {
				cr.extNotIn[strings.ToLower(e)] = true
			}
		}
		cr.minSize = r.When.MinSize
		if r.When.StemMatches != "" {
			re, err := regexp.Compile(r.When.StemMatches)
			if err != nil {
				return nil, fmt.Errorf("rule %q: stem_matches: %w", r.Name, err)
			}
			cr.stem = re
		}
		s.rules = append(s.rules, cr)
	}
	return s, nil
}

func (s *filterStep) Type() string { return config.StepFilter }

func (s *filterStep) Name() string { return "" }

func (s *filterStep) Requires() []string { return []string{config.ArtifactFileSet} }

func (s *filterStep) Produces() []string { return []string{config.ArtifactCandidates} }

func (s *filterStep) Run(_ context.Context, st *State) error {
	if st.Files == nil {
		return fmt.Errorf("no FileSet to filter")
	}
	out := &Candidates{}
	summary := &FilterSummary{Skipped: newCounts()}
	for _, f := range st.Files.Files {
		rule, include, matched := s.decide(f)
		switch {
		case matched && include:
			out.Files = append(out.Files, Candidate{File: f, Rule: rule})
		case matched:
			out.Rejected = append(out.Rejected, RejectedFile{File: f, Rule: rule})
			summary.Skipped[rule]++
		default:
			// No rule claimed it: out, with a reason that is not a rule name.
			out.Rejected = append(out.Rejected, RejectedFile{File: f, Rule: SkipNoMatch})
			summary.Skipped[SkipNoMatch]++
		}
	}
	summary.In = len(out.Files)
	summary.Out = len(out.Rejected)
	st.Candidates = out
	st.FilterRun = summary
	return nil
}

// decide returns the first rule that matches, whether it includes or excludes,
// and whether any rule matched at all.
func (s *filterStep) decide(f FileRef) (name string, include, matched bool) {
	stem := stemOf(f.Path)
	for _, r := range s.rules {
		if !r.matches(f, stem) {
			continue
		}
		return r.name, r.include, true
	}
	return "", false, false
}

func (r filterRule) matches(f FileRef, stem string) bool {
	switch {
	case r.extIn != nil:
		return r.extIn[f.Ext]
	case r.extNotIn != nil:
		return !r.extNotIn[f.Ext]
	case r.minSize != nil:
		// "smaller than min_size matches"; an unknown size cannot be proven small,
		// so it does not match.
		return f.SizeKnown && f.Size < *r.minSize
	case r.stem != nil:
		return r.stem.MatchString(stem)
	}
	return false
}

// stemOf is the name without its extension. At filter time this is the raw base
// name: stripping noise is `group`'s job, and it happens in exactly one place.
func stemOf(path string) string {
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}
