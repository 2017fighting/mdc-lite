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

// groupStep decides which candidates are the same work.
//
// Three rules, in order of strength:
//
//	① stems literally equal          → one group (including "one side marked, one
//	                                   side not")
//	② both marked and stems within   → one group (SSVR-122-cd1 + SSVR-123-cd2)
//	   max_distance
//	③ stems equal, no marker either  → not a group: a duplicate version goes to a
//	   human, because a copy is not a part
//
// Everything else stays apart, and `ABP-123` / `ABP-124` staying apart is the
// point of requiring positive evidence.
type groupStep struct {
	cfg       config.GroupWith
	strip     *stripper
	partRules []*regexp.Regexp
}

func newGroup(w config.GroupWith) (Step, error) {
	strip, err := newStripper(w.Sanitize)
	if err != nil {
		return nil, err
	}
	s := &groupStep{cfg: w, strip: strip}
	for _, p := range w.PartPatterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("part_patterns %q: %w", p, err)
		}
		s.partRules = append(s.partRules, re)
	}
	return s, nil
}

func (s *groupStep) Type() string { return config.StepGroup }

func (s *groupStep) Name() string { return "" }

func (s *groupStep) Requires() []string { return []string{config.ArtifactParsed} }

func (s *groupStep) Produces() []string { return []string{config.ArtifactGroups} }

// isCompanion reports whether the extension is one of the subtitle/image
// extensions. The table only says who counts as a companion; whether a file
// enters the pipeline at all is a filter rule's decision.
func (s *groupStep) isCompanion(f FileRef) bool {
	for _, ext := range s.cfg.CompanionExtensions {
		if strings.EqualFold(ext, f.Ext) {
			return true
		}
	}
	return false
}

func (s *groupStep) Run(ctx context.Context, st *State) error {
	if st.Candidates == nil {
		return fmt.Errorf("no Candidates to group")
	}
	byDir := map[string][]FileRef{}
	for _, c := range st.Candidates.Files {
		dir := filepath.Dir(c.File.Path)
		byDir[dir] = append(byDir[dir], c.File)
	}
	dirs := make([]string, 0, len(byDir))
	for dir := range byDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)

	out := &Groups{}
	for _, dir := range dirs {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.groupDir(byDir[dir], out)
	}
	sortGroups(out)
	st.Groups = out
	st.GroupRun = &GroupSummary{
		Groups:         len(out.Groups),
		Videos:         out.VideoCount(),
		Companions:     out.CompanionCount(),
		DuplicateStems: len(out.Duplicates),
		Unmatched:      len(out.Unmatched),
		Warnings:       len(out.Warnings),
	}
	for _, d := range out.Duplicates {
		st.GroupRun.DuplicateFiles += len(d.Files)
	}
	return nil
}

// video is one candidate video with the stem work done.
type video struct {
	file   FileRef
	stem   string
	marker Marker
	// conditional is set while a conditional marker is still waiting for its
	// siblings' agreement.
	conditional tail
}

func (s *groupStep) groupDir(files []FileRef, out *Groups) {
	var videos []*video
	var companions []FileRef
	for _, f := range files {
		if s.isCompanion(f) {
			companions = append(companions, f)
			continue
		}
		stripped := s.strip.strip(stemOf(f.Path))
		t := partAt(stripped, s.partRules)
		v := &video{file: f, stem: stripped}
		switch t.tier {
		case tierConfident:
			v.stem, v.marker = t.stem, t.marker()
		case tierConditional:
			// Settled once every video in the directory is known: an ending is a part
			// marker only if a sibling agrees with it.
			v.conditional = t
		case tierNone:
		}
		videos = append(videos, v)
	}
	s.resolveConditionalMarkers(videos)
	groups, duplicates, warnings := s.cluster(videos)
	// Attach before copying the groups into the result: appending to a group's
	// companions after the copy would write into a slice the copy no longer shares.
	unmatched := s.attachCompanions(companions, groups)
	out.Groups = append(out.Groups, groups...)
	out.Duplicates = append(out.Duplicates, duplicates...)
	out.Warnings = append(out.Warnings, warnings...)
	out.Unmatched = append(out.Unmatched, unmatched...)
}

// resolveConditionalMarkers applies the sibling rule: a conditional ending is a
// marker only when at least two files in the directory strip to the same stem
// and they do not all carry the same ending. A lone `ABC-123-1.mp4` is work
// `ABC-123-1`; `ABC-123-1.mp4` + `ABC-123-2.mp4` are that work's two parts. Two
// files carrying the *same* ending are a duplicate version, not two parts, which
// is why the endings must differ.
func (s *groupStep) resolveConditionalMarkers(videos []*video) {
	buckets := map[string][]*video{}
	for _, v := range videos {
		if v.conditional.tier == tierConditional {
			buckets[v.conditional.stem] = append(buckets[v.conditional.stem], v)
		}
	}
	for _, bucket := range buckets {
		texts := map[string]bool{}
		for _, v := range bucket {
			texts[v.conditional.text] = true
		}
		if len(bucket) < 2 || len(texts) < 2 {
			continue
		}
		for _, v := range bucket {
			v.stem, v.marker = v.conditional.stem, v.conditional.marker()
		}
	}
}

// cluster turns one directory's videos into groups. It returns the groups, the
// duplicate stems (deliberately not groups) and the advisory warnings.
func (s *groupStep) cluster(videos []*video) ([]Group, []DuplicateStem, []Warning) {
	// ① stems literally equal form one cluster — unless nobody in it carries a
	// marker, which is a duplicate version (③).
	byStem := map[string][]int{}
	for i, v := range videos {
		byStem[v.stem] = append(byStem[v.stem], i)
	}
	stems := make([]string, 0, len(byStem))
	for stem := range byStem {
		stems = append(stems, stem)
	}
	sort.Strings(stems)

	set := newDisjointSet(videos)
	duplicates := make([]DuplicateStem, 0, len(stems))
	for _, stem := range stems {
		members := byStem[stem]
		marked := false
		for _, i := range members {
			if videos[i].marker.Text != "" {
				marked = true
				break
			}
		}
		if !marked && len(members) > 1 {
			duplicates = append(duplicates, DuplicateStem{
				Dir:   filepath.Dir(videos[members[0]].file.Path),
				Stem:  stem,
				Files: siblingFiles(videos, members),
			})
			for _, i := range members {
				set.drop(i)
			}
			continue
		}
		for _, i := range members[1:] {
			set.union(members[0], i)
		}
	}

	// ② both sides marked and the stems close enough.
	s.mergeByDistance(videos, set)
	warnings := keepUnmerged(videos, set, s.warnDistance())

	groups := make([]Group, 0, len(stems))
	emitted := map[int]bool{}
	for i := range videos {
		if set.isDropped(i) {
			continue
		}
		root := set.find(i)
		if emitted[root] {
			continue
		}
		emitted[root] = true
		var parts []Part
		present := map[string]bool{}
		for j, v := range videos {
			if set.isDropped(j) || set.find(j) != root {
				continue
			}
			parts = append(parts, Part{File: v.file.withKind(KindVideo), Stem: v.stem, Marker: v.marker})
			present[v.stem] = true
		}
		sortParts(parts)
		groups = append(groups, Group{
			Dir:         filepath.Dir(parts[0].File.Path),
			Primary:     parts[0].File,
			PrimaryStem: parts[0].Stem,
			// More than one stem in a group means ② stitched them together, which the
			// summary says out loud: the group is a judgement, not a literal match.
			DistanceMerged: len(present) > 1,
			Parts:          parts,
		})
	}
	return groups, duplicates, warnings
}

func siblingFiles(videos []*video, members []int) []FileRef {
	files := make([]FileRef, len(members))
	for j, i := range members {
		files[j] = videos[i].file
	}
	sort.Slice(files, func(a, b int) bool { return files[a].Path < files[b].Path })
	return files
}

// mergeByDistance applies ②: a pair merges when both sides carry an effective
// marker and their stems are within max_distance.
func (s *groupStep) mergeByDistance(videos []*video, set *disjointSet) {
	marked := make([]int, 0, len(videos))
	for i, v := range videos {
		if v.marker.Text == "" {
			continue
		}
		marked = append(marked, i)
	}
	limit := s.cfg.MaxDistance

	if len(videos) <= s.cfg.MaxFilesForLevenshtein {
		for x := 0; x < len(marked); x++ {
			for y := x + 1; y < len(marked); y++ {
				if _, ok := withinDistance(videos[marked[x]].stem, videos[marked[y]].stem, limit); ok {
					set.joinByDistance(marked[x], marked[y])
				}
			}
		}
		return
	}

	// Too many files in one directory for a pairwise pass: build a BK-tree over
	// the marked stems instead. It answers the same question — "who is within
	// max_distance" — without comparing every pair.
	tree := newBKTree()
	for _, i := range marked {
		tree.add(videos[i].stem, i)
	}
	for _, i := range marked {
		var near []int
		tree.within(videos[i].stem, limit, func(j int) { near = append(near, j) })
		sort.Ints(near)
		for _, j := range near {
			set.joinByDistance(i, j)
		}
	}
}

// warnDistance bounds how far apart two marked stems may be and still be worth
// reporting as "this looks like parts and did not merge". Without a bound the
// summary would list every unrelated pair in a directory, which is not a hint,
// it is noise. Three times max_distance keeps the genuinely near misses.
func (s *groupStep) warnDistance() int { return 3 * s.cfg.MaxDistance }

// keepUnmerged picks the pairs that look like parts of one work but ended up in
// different groups. It runs after the clustering so that a pair merged through a
// third file (A–B are close, B–C are close, A–C are not) is not reported as a
// miss, and it is capped: the list is a hint, and a wall of text is not one.
func keepUnmerged(videos []*video, set *disjointSet, within int) []Warning {
	var marked []int
	for i, v := range videos {
		if v.marker.Text != "" && !set.isDropped(i) {
			marked = append(marked, i)
		}
	}
	out := make([]Warning, 0, maxWarnings)
	for x := 0; x < len(marked); x++ {
		for y := x + 1; y < len(marked); y++ {
			a, b := marked[x], marked[y]
			if set.find(a) == set.find(b) {
				continue
			}
			if _, ok := withinDistance(videos[a].stem, videos[b].stem, within); !ok {
				continue
			}
			if !sharesAPrefix(videos[a].stem, videos[b].stem) {
				continue // both marked but nothing suggests one work: two files, not a hint
			}
			out = append(out, Warning{
				Dir: filepath.Dir(videos[a].file.Path),
				A:   videos[a].file,
				B:   videos[b].file,
			})
			if len(out) == maxWarnings {
				return out
			}
		}
	}
	return out
}

// sharesAPrefix reports whether two stems agree on at least half of the shorter
// one. Every part of one work starts the same way (`SSVR-122` / `SSVR-123`), while
// a subtitle-shaped name from another work does not (`9ofje121` / `ABC-123`), and
// without this the warning list is mostly the latter.
func sharesAPrefix(a, b string) bool {
	n := min(len(a), len(b))
	common := 0
	for common < n && a[common] == b[common] {
		common++
	}
	return 2*common >= n
}

// maxWarnings caps how many "looks like parts, did not merge" pairs a summary
// carries.
const maxWarnings = 10

// attachCompanions hangs the directory's subtitles and images on the groups they
// belong to. A companion is never part evidence: it follows a group, it does not
// create or split one.
func (s *groupStep) attachCompanions(companions []FileRef, groups []Group) []UnmatchedFile {
	var unmatched []UnmatchedFile
	sort.Slice(companions, func(a, b int) bool { return companions[a].Path < companions[b].Path })
	for _, f := range companions {
		stripped := s.strip.strip(stemOf(f.Path))
		t := partAt(stripped, s.partRules)
		// Most specific first: the whole stripped name, then the name with its own
		// part marker taken off, which is how `SSVR-122-cd1.srt` finds its video.
		stems := []string{stripped}
		if t.tier != tierNone && t.stem != stripped {
			stems = append(stems, t.stem)
		}
		// A subtitle name often carries its language after the work's stem
		// (`ABC-123.zh.srt`), and that token is not part evidence: matching on the
		// name without it is what binds the subtitle to its video.
		lang, withoutLang := splitLanguageToken(stripped)
		if lang != "" {
			stems = append(stems, withoutLang)
		}
		attached := false
		for _, stem := range stems {
			for gi := range groups {
				follows, ok := follows(groups[gi], stem, t.marker())
				if !ok {
					continue
				}
				groups[gi].Companions = append(groups[gi].Companions, Companion{
					File:        f.withKind(KindCompanion),
					Stem:        stem,
					Marker:      t.marker(),
					Lang:        lang,
					FollowsPart: follows,
				})
				attached = true
				break
			}
			if attached {
				break
			}
		}
		if !attached {
			unmatched = append(unmatched, UnmatchedFile{File: f.withKind(KindCompanion), Reason: SkipNoGroup})
		}
	}
	return unmatched
}

// languageToken matches a subtitle's language suffix: a dot followed by a short
// language name, optionally with a region (`zh`, `zh-cn`, `eng`, `jpn`).
var languageToken = regexp.MustCompile(`(?i)^\.[a-z]{2,3}(?:[-_][a-z0-9]{2,4})?$`)

// splitLanguageToken separates a name's trailing language token from the rest:
// `ABC-123.zh` → (".zh", "ABC-123"). No trailing dot-token, or a token that does
// not look like a language, is left alone: `.2024` and `.cd1` are not languages.
func splitLanguageToken(stem string) (token, base string) {
	i := strings.LastIndexByte(stem, '.')
	if i <= 0 {
		return "", stem
	}
	token = stem[i:]
	if !languageToken.MatchString(token) {
		return "", stem
	}
	return token, stem[:i]
}

// follows reports whether a companion with this stem belongs to the group, and
// which part it follows (0 = the primary).
//
// A companion that names a part itself (`SSVR-122-cd1.srt`) binds to that part,
// so a subtitle keeps following the same half even after parts are renumbered;
// one that names none follows the primary.
func follows(g Group, stem string, m Marker) (int, bool) {
	if m.Index == 0 && g.PrimaryStem == stem {
		return 0, true
	}
	var anyPart *Part
	for i := range g.Parts {
		p := &g.Parts[i]
		if p.Stem != stem || p.Marker.Text == "" {
			continue
		}
		if m.Index > 0 && p.Index() == m.Index {
			return p.Index(), true
		}
		if anyPart == nil {
			anyPart = p
		}
	}
	if anyPart != nil {
		return anyPart.Index(), true
	}
	if g.PrimaryStem == stem {
		return 0, true
	}
	return 0, false
}

// sortGroups orders groups by directory then primary, and sorts what hangs off
// them, so two runs over the same tree print the same bytes.
func sortGroups(g *Groups) {
	for i := range g.Groups {
		grp := &g.Groups[i]
		sortParts(grp.Parts)
		sort.Slice(grp.Companions, func(a, b int) bool { return grp.Companions[a].File.Path < grp.Companions[b].File.Path })
	}
	sort.Slice(g.Groups, func(a, b int) bool {
		if g.Groups[a].Dir != g.Groups[b].Dir {
			return g.Groups[a].Dir < g.Groups[b].Dir
		}
		return g.Groups[a].Primary.Path < g.Groups[b].Primary.Path
	})
	sort.Slice(g.Duplicates, func(a, b int) bool {
		if g.Duplicates[a].Dir != g.Duplicates[b].Dir {
			return g.Duplicates[a].Dir < g.Duplicates[b].Dir
		}
		return g.Duplicates[a].Stem < g.Duplicates[b].Stem
	})
	sort.Slice(g.Unmatched, func(a, b int) bool { return g.Unmatched[a].File.Path < g.Unmatched[b].File.Path })
	sort.Slice(g.Warnings, func(a, b int) bool { return g.Warnings[a].A.Path < g.Warnings[b].A.Path })
}

// sortParts puts the parts in part order: by part number, and by name among the
// ones that carry no number. An unmarked file counts as part 1, which is how
// `ABC-123.mp4` ends up in front of `ABC-123-2.mp4`, and why `cd2` stays in front
// of `cd10` (a dictionary comparison would say otherwise).
func sortParts(parts []Part) {
	sort.SliceStable(parts, func(a, b int) bool {
		if ia, ib := parts[a].Index(), parts[b].Index(); ia != ib {
			return ia < ib
		}
		return filepath.Base(parts[a].File.Path) < filepath.Base(parts[b].File.Path)
	})
}

// withKind returns the file tagged with the kind `group` has decided for it.
func (f FileRef) withKind(k FileKind) FileRef {
	f.Kind = k
	return f
}

// disjointSet is the union-find the clustering needs: ② merges clusters through
// any pair that is close enough, so a cluster is a transitive closure rather than
// a single pass over neighbours.
type disjointSet struct {
	parent, rank []int
	dropped      []bool
	// indices is the set of part numbers a cluster already occupies, held at the
	// cluster's root. It is what joinByDistance checks before merging.
	indices []map[int]bool
}

func newDisjointSet(videos []*video) *disjointSet {
	d := &disjointSet{
		parent:  make([]int, len(videos)),
		rank:    make([]int, len(videos)),
		dropped: make([]bool, len(videos)),
		indices: make([]map[int]bool, len(videos)),
	}
	for i := range d.parent {
		d.parent[i] = i
		d.indices[i] = map[int]bool{}
		if videos[i].marker.Index > 0 {
			d.indices[i][videos[i].marker.Index] = true
		}
	}
	return d
}

func (d *disjointSet) find(i int) int {
	for d.parent[i] != i {
		d.parent[i] = d.parent[d.parent[i]]
		i = d.parent[i]
	}
	return i
}

// joinByDistance unions two marked videos when their clusters are near enough in
// stem and share no part number. The part-number check is what stops
// single-linkage chaining: `WORK-100-cd1` + `WORK-100-cd2` are one work, but
// `WORK-100-cd2` + `WORK-101-cd1` is a pair of adjacent works one character
// apart, and merging them would put two part 1s in one group.
func (d *disjointSet) joinByDistance(a, b int) {
	ra, rb := d.find(a), d.find(b)
	if ra == rb {
		return
	}
	for idx := range d.indices[rb] {
		if d.indices[ra][idx] {
			return
		}
	}
	d.union(a, b)
}

func (d *disjointSet) union(a, b int) {
	ra, rb := d.find(a), d.find(b)
	if ra == rb {
		return
	}
	if d.rank[ra] < d.rank[rb] {
		ra, rb = rb, ra
	}
	d.parent[rb] = ra
	if d.rank[ra] == d.rank[rb] {
		d.rank[ra]++
	}
	for idx := range d.indices[rb] {
		d.indices[ra][idx] = true
	}
	d.indices[rb] = nil
}

// drop takes one index out of the clustering: its files went to the duplicate
// bucket instead, where a human decides.
func (d *disjointSet) drop(i int) { d.dropped[i] = true }

func (d *disjointSet) isDropped(i int) bool { return d.dropped[i] }
