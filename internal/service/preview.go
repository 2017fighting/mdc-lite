package service

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/2017fighting/mdc-lite/internal/pipeline"
)

// This file is the scan preview: the async walk-plus-parse a human looks at before
// creating a batch.
//
// Its state lives in memory only (ADR-0013): a preview is a question about a
// directory right now, not a fact worth persisting. It expires after
// DefaultPreviewTTL, is replaced by the next preview, and has no cancel endpoint —
// the frontend simply asks again.

// DefaultPreviewTTL is how long a preview stays readable. Fifteen minutes is longer
// than a human takes to look at a table, and short enough that a forgotten preview
// does not keep a walk's result around.
const DefaultPreviewTTL = 15 * time.Minute

// The three states a preview can be in. `running` is the answer to the first poll;
// the result is one of the other two.
const (
	PreviewRunning   = "running"
	PreviewSucceeded = "succeeded"
	PreviewFailed    = "failed"
)

// PreviewRequest is `POST /api/scan/preview`'s body: which pipeline, which
// directories, and which subtrees to skip.
type PreviewRequest struct {
	Pipeline string   `json:"pipeline"`
	Roots    []string `json:"roots"`
	Exclude  []string `json:"exclude"`
}

// PreviewGroup is one row of the preview table. It deliberately has no `res`: V1
// probes no video stream (no ffprobe, no frame grab), so there is no resolution to
// report and the frontend has no column for one.
type PreviewGroup struct {
	Primary string   `json:"primary"`
	Parts   []string `json:"parts"`
	// Number is the normalized 番号, empty when this group waits for a human.
	Number string `json:"number"`
	// Candidates is what the rules offered for the primary file: the chips shown
	// beside a group that would enter the needs-number panel.
	Candidates []string `json:"candidates"`
	// Marker is the primary's part marker as written ("cd1", "A", "1"), empty when
	// the name carried none.
	Marker string `json:"marker"`
	Dir    string `json:"dir"`
	// Size is the primary file's bytes.
	Size int64 `json:"size"`
	// Occupied is dedup gate one: another live task already holds this path.
	Occupied bool `json:"occupied"`
}

// PreviewError is why a preview failed. It uses the standard machine-code shape, so
// the frontend can pick its wording from the same table as every other error.
type PreviewError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// PreviewResult is `GET /api/scan/previews/{id}`'s body.
type PreviewResult struct {
	State       string `json:"state"`
	WalkedFiles int    `json:"walked_files"`
	// Matched is how many files survived the filter — the number the table will
	// show, before grouping folds parts into one work.
	Matched int            `json:"matched"`
	Groups  []PreviewGroup `json:"groups"`
	Error   *PreviewError  `json:"error,omitempty"`
}

// preview is one in-flight or finished preview.
type preview struct {
	id      string
	result  PreviewResult
	created time.Time
}

// previews is the in-memory registry. One preview at a time: starting another is
// how the old one is dropped.
type previews struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	current *preview
	seq     int64
}

func newPreviews(ttl time.Duration, now func() time.Time) *previews {
	return &previews{ttl: ttl, now: now}
}

// start installs a running preview and returns its id.
func (p *previews) start() *preview {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seq++
	one := &preview{
		id:      "p" + strconv.FormatInt(p.seq, 10),
		created: p.now(),
		result:  PreviewResult{State: PreviewRunning, Groups: []PreviewGroup{}},
	}
	p.current = one
	return one
}

// finish stores one preview's ending, unless a newer preview has replaced it.
func (p *previews) finish(one *preview, res PreviewResult) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.current != one {
		return
	}
	one.result = res
}

// get reads a preview by id. An unknown id and an expired one are the same answer:
// the preview is gone, ask again.
func (p *previews) get(id string) (PreviewResult, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.current == nil || p.current.id != id {
		return PreviewResult{}, false
	}
	if p.now().Sub(p.current.created) > p.ttl {
		p.current = nil
		return PreviewResult{}, false
	}
	return p.current.result, true
}

// StartPreview validates the request, installs a running preview and walks in the
// background. The response is 202 with the id; the walk may take as long as the
// largest root takes.
//
// Everything a human can get wrong is refused here, before the 202: an unknown
// pipeline, no roots, a root that is gone, an exclusion that is not a real child of
// a root. A failure after this point is the walk's, and lands in the preview's
// `error`.
func (s *Service) StartPreview(ctx context.Context, req PreviewRequest) (string, error) {
	p, err := s.pipeline(req.Pipeline)
	if err != nil {
		return "", err
	}
	roots, err := cleanPaths(req.Roots)
	if err != nil {
		return "", err
	}
	if len(roots) == 0 {
		return "", badRequest(CodeEmptySelection, "select at least one directory to scan")
	}
	existing, missing, err := s.checkRoots(roots)
	if err != nil {
		return "", err
	}
	if len(missing) > 0 {
		return "", badRequestf(CodeBadRequest, "these paths do not exist: %v", missing)
	}
	exclude, err := cleanPaths(req.Exclude)
	if err != nil {
		return "", err
	}
	if err := checkExcludes(existing, exclude); err != nil {
		return "", err
	}

	one := s.previews.start()
	go func() {
		// The preview outlives the request that asked for it: the client got its id
		// and will poll, so a cancelled request must not abort the walk.
		ctx := context.Background()
		out := PreviewResult{State: PreviewSucceeded, Groups: []PreviewGroup{}}
		var run *pipeline.Result
		err := s.prefix(ctx, func(ctx context.Context) error {
			var steps []pipeline.StepOutcome
			res, runErr := s.runPrefix(ctx, p, pipeline.Input{Roots: existing, Exclude: exclude}, &steps)
			run = res
			return runErr
		})
		if err != nil {
			out.State = PreviewFailed
			out.Error = &PreviewError{Code: CodeInternal, Message: err.Error()}
			s.previews.finish(one, out)
			return
		}
		out = s.previewResult(ctx, run)
		s.previews.finish(one, out)
	}()
	return one.id, nil
}

// previewResult projects a finished prefix run onto the table. Occupied lookups need
// the database; without one every row says "not occupied", which is what a process
// whose database never opened can honestly say.
func (s *Service) previewResult(ctx context.Context, run *pipeline.Result) PreviewResult {
	out := PreviewResult{State: PreviewSucceeded, Groups: []PreviewGroup{}}
	if run == nil || run.State == nil {
		return out
	}
	st := run.State
	if st.ScanRun != nil {
		// Files that entered the walk's file set, not every entry the walker saw: the
		// field says "files", and a directory is not one.
		out.WalkedFiles = st.ScanRun.Files
	}
	if st.FilterRun != nil {
		out.Matched = st.FilterRun.Out
	}

	// The dedup gate is answered for every primary in one query.
	occupied := map[string]bool{}
	if st.Parsed != nil && s.tasks != nil {
		paths := make([]string, 0, len(st.Parsed.Items)+len(st.Parsed.Unresolved))
		for _, item := range st.Parsed.Items {
			paths = append(paths, item.Group.Primary.Path)
		}
		for _, u := range st.Parsed.Unresolved {
			if len(u.Files) > 0 {
				paths = append(paths, u.Files[0].Path)
			}
		}
		if states, err := s.tasks.LookupPaths(ctx, paths); err == nil {
			for path, state := range states {
				occupied[path] = state.Active
			}
		} else {
			s.logger.Error("the preview could not look up occupied paths", "err", err)
		}
	}

	if st.Parsed == nil {
		return out
	}
	for _, item := range st.Parsed.Items {
		g := item.Group
		parts := make([]string, 0, len(g.Parts))
		for _, part := range g.Parts {
			parts = append(parts, part.File.Path)
		}
		var candidates []string
		if len(item.Parts) > 0 {
			candidates = append(candidates, item.Parts[0]...)
		}
		out.Groups = append(out.Groups, PreviewGroup{
			Primary:    g.Primary.Path,
			Parts:      parts,
			Number:     item.Number.Normalized,
			Candidates: nonNil(candidates),
			Marker:     primaryMarker(g),
			Dir:        g.Dir,
			Size:       g.Primary.Size,
			Occupied:   occupied[g.Primary.Path],
		})
	}
	for _, u := range st.Parsed.Unresolved {
		if len(u.Files) == 0 {
			continue
		}
		parts := make([]string, 0, len(u.Files))
		for _, f := range u.Files {
			parts = append(parts, f.Path)
		}
		out.Groups = append(out.Groups, PreviewGroup{
			Primary:    u.Files[0].Path,
			Parts:      parts,
			Candidates: nonNil(append([]string(nil), u.Candidates...)),
			Dir:        u.Dir,
			Size:       u.Files[0].Size,
			Occupied:   occupied[u.Files[0].Path],
		})
	}
	return out
}

// Preview answers one poll.
func (s *Service) Preview(_ context.Context, id string) (PreviewResult, error) {
	res, ok := s.previews.get(id)
	if !ok {
		return PreviewResult{}, notFound("no such preview: %s (it expired, or a newer preview replaced it)", id)
	}
	return res, nil
}

// primaryMarker is the primary part's marker as written, or "" when the name
// carries none.
func primaryMarker(g pipeline.Group) string {
	for _, p := range g.Parts {
		if p.File.Path == g.Primary.Path {
			return p.Marker.Text
		}
	}
	return ""
}

// checkExcludes refuses an exclusion that is not a true child of one of the roots.
// Equal to a root is refused too: "exclude the whole thing" is expressed by not
// selecting it, and a whole-root exclusion would make the walk silently produce
// nothing.
func checkExcludes(roots, exclude []string) error {
	for _, ex := range exclude {
		if !underAnyRoot(roots, ex) {
			return badRequestf(CodeExcludeNotUnderRoot,
				"%s is not inside any of the selected directories (%v)", ex, roots)
		}
	}
	return nil
}

// underAnyRoot reports whether path is strictly below one of the roots.
func underAnyRoot(roots []string, path string) bool {
	for _, root := range roots {
		if path != root && withinPath(path, root) {
			return true
		}
	}
	return false
}

// withinPath is the lexical containment test the picker and the exclusions share: is
// child inside root (or equal to it)? Nothing is resolved, so a symlink cannot
// decide what a whitelist allows. The separator is part of the prefix so `/media2`
// is not inside `/media`.
func withinPath(child, root string) bool {
	if child == root {
		return true
	}
	prefix := root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(child, prefix)
}

// cleanPaths trims and absolutizes a list of paths, dropping empty entries. A path
// that cannot be absolutized is a 400: it is not a container path at all.
func cleanPaths(paths []string) ([]string, error) {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, badRequestf(CodeBadRequest, "%s: %v", p, err)
		}
		out = append(out, abs)
	}
	return out, nil
}

// nonNil keeps a JSON list from serialising as null: the API's lists are always
// arrays, empty ones included.
func nonNil(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}
