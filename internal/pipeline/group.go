package pipeline

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/2017fighting/mdc-lite/internal/config"
)

// groupStep turns parsed candidates into works.
//
// Since ADR-0019 the identity of a work is its number, not its directory: `parse`
// has already read a normalized number off every file, so grouping is a fold by
// that number. Every file that parsed to one number — the parts of a split work,
// the original and its `-C` / `-U` copies — lands in one Group, wherever the files
// sit on disk. Two spellings that normalize the same (`hnvr00152`, `HNVR-152`) are
// the same work; that is the whole point of normalizing before grouping.
//
// The step also decides the primary: the lowest part number first, and among the
// versions of that part the original before the chinese copy before the uncensored
// one (ADR-0019). Everything downstream — the scraped number, the metadata, the
// output directory — is derived from the primary.
//
// A file whose name yielded no number is not a work: it never becomes a group and
// it becomes no part of one. It stays in Parsed.Unresolved and reaches a human as
// its own needs_number task. Grouping never guesses a number.
type groupStep struct {
	cfg config.GroupWith
}

func newGroup(w config.GroupWith) (Step, error) {
	return &groupStep{cfg: w}, nil
}

func (s *groupStep) Type() string { return config.StepGroup }

func (s *groupStep) Name() string { return "" }

func (s *groupStep) Requires() []string { return []string{config.ArtifactParsed} }

func (s *groupStep) Produces() []string { return []string{config.ArtifactGroups} }

// isCompanion reports whether the extension is one of the subtitle/image
// extensions. The table only says who counts as a companion; whether a file
// enters the pipeline at all is a filter rule's decision.
func (s *groupStep) isCompanion(ext string) bool {
	for _, want := range s.cfg.CompanionExtensions {
		if strings.EqualFold(want, ext) {
			return true
		}
	}
	return false
}

func (s *groupStep) Run(ctx context.Context, st *State) error {
	if st.Parsed == nil {
		return fmt.Errorf("no Parsed to group")
	}
	var videos []ParsedCandidate
	var companions []companionFile
	for _, c := range st.Parsed.Items {
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.isCompanion(c.File.Ext) {
			companions = append(companions, newCompanion(c.File, c.Stem, c.Part, c.Number))
			continue
		}
		videos = append(videos, c)
	}
	// A companion the parse could not number is still a companion: its own name
	// carries no number, but it can still name a group's stem.
	for _, u := range st.Parsed.Unresolved {
		for _, f := range u.Files {
			if s.isCompanion(f.Ext) {
				companions = append(companions, newCompanion(f, u.Stem, Marker{}, Number{}))
			}
		}
	}

	groups := aggregate(videos)
	unmatched := attachCompanions(companions, groups)
	sortGroups(groups)

	out := &Groups{Groups: groups, Unmatched: unmatched}
	st.Groups = out
	st.GroupRun = &GroupSummary{
		Groups:     len(groups),
		Videos:     out.VideoCount(),
		Companions: out.CompanionCount(),
		Unmatched:  len(unmatched),
	}
	return nil
}

// aggregate folds the resolved videos by normalized number. One number, one group:
// a work with several parts, several versions, or both, is still one work.
func aggregate(videos []ParsedCandidate) []Group {
	byNumber := map[string][]ParsedCandidate{}
	numbers := make([]string, 0, len(videos))
	for _, v := range videos {
		number := v.Number.Normalized
		if _, seen := byNumber[number]; !seen {
			numbers = append(numbers, number)
		}
		byNumber[number] = append(byNumber[number], v)
	}
	sort.Strings(numbers)

	out := make([]Group, 0, len(numbers))
	for _, number := range numbers {
		members := byNumber[number]
		grp := Group{
			// The primary speaks for the group, so its reading of the number is the
			// group's: `HNVR-152` rather than whichever spelling came first in the walk.
			Number: members[0].Number,
			Parts:  make([]Part, 0, len(members)),
		}
		for _, m := range members {
			grp.Parts = append(grp.Parts, Part{
				File:   m.File.withKind(KindVideo),
				Stem:   m.Stem,
				Marker: m.Part,
				Flags:  m.Flags,
			})
		}
		// The one thing `parse` could not settle on its own: a conditional ending is a
		// part marker only with a sibling's agreement, and only `group` sees them.
		resolveConditionalMarkers(grp.Parts)
		sortParts(grp.Parts)
		grp.Primary = grp.Parts[0].File
		grp.PrimaryStem = grp.Parts[0].Stem
		grp.Dir = filepath.Dir(grp.Primary.Path)
		for _, m := range members {
			if m.File.Path == grp.Primary.Path {
				grp.Number = m.Number
				break
			}
		}
		out = append(out, grp)
	}
	return out
}

// resolveConditionalMarkers settles the endings `parse` deliberately left in the
// stem: a trailing `-1` or a glued `A` is a part marker only when another file of
// the same work ends differently on the same stem. `ABC-123-1.mp4` on its own is a
// work whose name says `ABC-123-1`; next to `ABC-123-2.mp4` the two are that work's
// parts. Two files carrying the *same* ending are one part with two copies, which is
// why the endings must differ.
func resolveConditionalMarkers(parts []Part) {
	tails := make([]tail, len(parts))
	buckets := map[string][]int{}
	for i := range parts {
		t := partAt(parts[i].Stem, nil)
		tails[i] = t
		if t.tier != tierConditional {
			continue
		}
		buckets[t.stem] = append(buckets[t.stem], i)
	}
	for stem, members := range buckets {
		texts := map[string]bool{}
		for _, i := range members {
			texts[tails[i].text] = true
		}
		if len(members) < 2 || len(texts) < 2 {
			continue
		}
		for _, i := range members {
			parts[i].Stem, parts[i].Marker = stem, tails[i].marker()
		}
	}
}

// versionRank orders the versions of one part for the primary pick: the original
// first, then the chinese copy, then the uncensored one, and the copy that is both
// last. The flags are name evidence — nothing here opens the file — so the ranking
// is the whole rule.
func versionRank(f VersionFlags) int {
	switch {
	case !f.IsChinese && !f.IsUncensored:
		return 0
	case !f.IsUncensored:
		return 1
	case !f.IsChinese:
		return 2
	default:
		return 3
	}
}

// companionFile is one subtitle or image with the name work done: the stems it can
// match a video on, its own part marker and number, and its language token.
type companionFile struct {
	file FileRef
	// stem is the name the companion belongs to, without its language token
	// (`ABC-123.zh` → `ABC-123`); stems adds the spellings to match a part against.
	stem   string
	stems  []string
	marker Marker
	number Number
	lang   string
}

// newCompanion reads a companion's matching names. The language token is split off
// because a subtitle names its work, not its language: `ABC-123.zh.srt` belongs to
// `ABC-123.mp4`, and the token is what the rename keeps. A conditional ending is
// split off too (`9ofje121A.srt` names the work whose stem is `9ofje121`), so a
// companion of a sibling-agreed part finds it.
func newCompanion(file FileRef, stem string, marker Marker, number Number) companionFile {
	c := companionFile{file: file.withKind(KindCompanion), stem: stem, marker: marker, number: number}
	addStem := func(s string) {
		c.stems = append(c.stems, s)
		t := partAt(s, nil)
		if t.tier == tierNone {
			return
		}
		c.stems = append(c.stems, t.stem)
		if c.marker.Index == 0 {
			c.marker = t.marker()
		}
	}
	addStem(stem)
	if lang, base := splitLanguageToken(stem); lang != "" {
		c.lang, c.stem = lang, base
		addStem(base)
	}
	return c
}

// attachCompanions hangs the subtitles and images on the groups they name. A
// companion is never part evidence: it follows a group, it does not create or split
// one. The match is by name first and by number second, with the companion's own
// directory breaking ties, so `ABC-123.zh.srt` finds `ABC-123.mp4` whether it
// shares its directory or not, and `ABC-123-cd1.srt` keeps following the first disc.
func attachCompanions(companions []companionFile, groups []Group) []UnmatchedFile {
	sort.Slice(companions, func(a, b int) bool { return companions[a].file.Path < companions[b].file.Path })
	var unmatched []UnmatchedFile
	for _, c := range companions {
		gi, follows, ok := bestGroup(c, groups)
		if !ok {
			unmatched = append(unmatched, UnmatchedFile{File: c.file, Reason: SkipNoGroup})
			continue
		}
		groups[gi].Companions = append(groups[gi].Companions, Companion{
			File:        c.file,
			Stem:        c.stem,
			Marker:      c.marker,
			Lang:        c.lang,
			FollowsPart: follows,
		})
	}
	return unmatched
}

// bestGroup picks the group a companion belongs to.
//
// Two things make it a match: a part with the companion's stem (`ABC-123.mp4` and
// `ABC-123.srt`), or the companion's own number (`ABC-123.zh.srt` parsed to
// ABC-123). The score prefers the stronger evidence and, on a tie, the group living
// in the companion's own directory: a file sitting next to a work almost always
// belongs to it, while the same name somewhere else is only a claim.
//
// The returned index is the part the companion follows, 0 meaning the primary.
func bestGroup(c companionFile, groups []Group) (int, int, bool) {
	dir := filepath.Dir(c.file.Path)
	best, bestScore, bestFollows := -1, 0, 0
	for gi := range groups {
		grp := groups[gi]
		part, byStem := matchingPart(c, grp)
		byNumber := c.number.Normalized != "" && c.number.Normalized == grp.Number.Normalized
		if !byStem && !byNumber {
			continue
		}
		score := 0
		if byStem {
			score += 2
		}
		if byNumber {
			score++
		}
		if groupHasDir(grp, dir) {
			score += 4
		}
		if score <= bestScore {
			continue
		}
		best, bestScore, bestFollows = gi, score, follows(grp, part, c.marker)
	}
	return best, bestFollows, best >= 0
}

// matchingPart finds the part a companion names: one whose stem it shares, and, when
// the companion carries its own part marker, one with that part number. The part in
// the companion's own directory wins among equals.
func matchingPart(c companionFile, g Group) (int, bool) {
	dir := filepath.Dir(c.file.Path)
	best := -1
	for i := range g.Parts {
		p := g.Parts[i]
		if !sameStem(c.stems, p.Stem) {
			continue
		}
		if c.marker.Index > 0 && p.Index() != c.marker.Index {
			continue
		}
		if filepath.Dir(p.File.Path) == dir {
			return i, true
		}
		if best < 0 {
			best = i
		}
	}
	return best, best >= 0
}

// follows is the part index a companion renames against. A companion that names a
// part itself (`ABC-123-cd1.srt`) keeps following that disc; one that names none
// follows the primary, which is how a subtitle of the whole work stays with the work
// rather than with whichever disc happens to be first.
func follows(g Group, part int, m Marker) int {
	if part < 0 || m.Index == 0 {
		return 0
	}
	return g.Parts[part].Index()
}

// sameStem reports whether any of the names equals the part's stem. The comparison
// folds case because a name is the same name after a torrent re-upload changed its
// case; it does not fold more than that, because a stem is the strong evidence.
func sameStem(stems []string, partStem string) bool {
	for _, stem := range stems {
		if strings.EqualFold(stem, partStem) {
			return true
		}
	}
	return false
}

// groupHasDir reports whether the group has a part in dir.
func groupHasDir(g Group, dir string) bool {
	for _, p := range g.Parts {
		if filepath.Dir(p.File.Path) == dir {
			return true
		}
	}
	return false
}

// sortGroups orders groups by number then primary, and sorts what hangs off them, so
// two runs over the same tree print the same bytes.
func sortGroups(groups []Group) {
	for i := range groups {
		grp := &groups[i]
		sortParts(grp.Parts)
		sort.Slice(grp.Companions, func(a, b int) bool { return grp.Companions[a].File.Path < grp.Companions[b].File.Path })
	}
	sort.Slice(groups, func(a, b int) bool {
		if groups[a].Number.Normalized != groups[b].Number.Normalized {
			return groups[a].Number.Normalized < groups[b].Number.Normalized
		}
		return groups[a].Primary.Path < groups[b].Primary.Path
	})
}

// sortParts puts the parts in part order: by part number, then by the preferred
// version, then by path so two runs over one tree agree. An unmarked file counts as
// part 1, which is how `ABC-123.mp4` ends up in front of `ABC-123-2.mp4`, and why
// `cd2` stays in front of `cd10` (a dictionary comparison would say otherwise).
func sortParts(parts []Part) {
	sort.SliceStable(parts, func(a, b int) bool {
		if ia, ib := parts[a].Index(), parts[b].Index(); ia != ib {
			return ia < ib
		}
		if ra, rb := versionRank(parts[a].Flags), versionRank(parts[b].Flags); ra != rb {
			return ra < rb
		}
		return parts[a].File.Path < parts[b].File.Path
	})
}

// withKind returns the file tagged with the kind `group` has decided for it.
func (f FileRef) withKind(k FileKind) FileRef {
	f.Kind = k
	return f
}
