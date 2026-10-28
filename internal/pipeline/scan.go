package pipeline

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/2017fighting/mdc-lite/internal/config"
)

// scanStep enumerates the directories and files a run starts from.
//
// It filters on exactly three things: hidden entries, the two download markers
// (`.part`, `.!qB`), and the `exclude` directories. It deliberately does not
// look at extensions: extensions are a filter rule, and pre-filtering them here
// would keep companion files out of the FileSet, which would make
// `group.with.companion_extensions` dead configuration.
type scanStep struct {
	cfg   config.ScanWith
	input Input
}

func newScan(w config.ScanWith) Step { return &scanStep{cfg: w} }

func (s *scanStep) setInput(in Input) { s.input = in }

func (s *scanStep) Type() string { return config.StepScan }

func (s *scanStep) Name() string { return "" }

func (s *scanStep) Requires() []string { return nil }

func (s *scanStep) Produces() []string { return []string{config.ArtifactFileSet} }

func (s *scanStep) Run(ctx context.Context, st *State) error {
	// A trigger injects `{roots, exclude}` and that injection wins over the file.
	roots := s.cfg.Roots
	exclude := s.cfg.Exclude
	if len(s.input.Roots) > 0 {
		roots = s.input.Roots
	}
	if len(s.input.Exclude) > 0 {
		exclude = s.input.Exclude
	}
	if len(roots) == 0 {
		return fmt.Errorf("no roots: pass --roots or set scan.with.roots")
	}
	absRoots, err := absolute(roots)
	if err != nil {
		return err
	}
	absExclude, err := absolute(exclude)
	if err != nil {
		return err
	}

	w := &walker{exclude: absExclude, files: &FileSet{Roots: absRoots}, walked: 0}
	for _, root := range absRoots {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := w.root(ctx, root); err != nil {
			return err
		}
	}
	st.Files = w.files
	st.ScanRun = &ScanSummary{
		Roots:   len(absRoots),
		Walked:  w.walked,
		Files:   len(w.files.Files),
		Skipped: w.skipped,
	}
	return nil
}

// walker accumulates one run's FileSet.
type walker struct {
	exclude []string
	files   *FileSet
	walked  int
	skipped map[string]int
}

func (w *walker) skip(path, reason string) {
	w.files.Skipped = append(w.files.Skipped, SkippedFile{Path: path, Reason: reason})
	if w.skipped == nil {
		w.skipped = newCounts()
	}
	w.skipped[reason]++
}

// root takes one root. A root may be a directory (walked recursively) or a
// single file (taken on its own, which is how a manual preview hands over a
// selection).
func (w *walker) root(ctx context.Context, root string) error {
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("root %s: %w", root, err)
	}
	if !info.IsDir() {
		w.walked++
		w.takeFile(root, info)
		return nil
	}
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if path == root {
			return nil // the root itself is not an entry of the walk
		}
		w.walked++
		if err != nil {
			return w.onWalkError(path, d)
		}
		name := d.Name()
		if d.IsDir() {
			switch {
			case w.isExcluded(path):
				w.skip(path, SkipExcluded)
				return fs.SkipDir
			case isHidden(name):
				w.skip(path, SkipHidden)
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return w.takeSymlink(path)
		}
		if !d.Type().IsRegular() {
			w.skip(path, SkipUnreadable)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return w.onWalkError(path, d)
		}
		w.takeFile(path, info)
		return nil
	})
}

// onWalkError records an entry the walk could not read. One unreadable directory
// is a skipped subtree, not the end of the run: a library with one dead mount
// should still produce the groups it does have.
func (w *walker) onWalkError(path string, d fs.DirEntry) error {
	w.skip(path, SkipUnreadable)
	if d != nil && d.IsDir() {
		return fs.SkipDir
	}
	return nil
}

// takeSymlink resolves a symlink. A directory symlink is never followed — a cycle
// would hang the walk — while a file symlink is taken as the file it points at,
// which is what a library with symlinked media expects.
func (w *walker) takeSymlink(path string) error {
	target, err := os.Stat(path)
	switch {
	case err != nil:
		w.skip(path, SkipUnreadable)
	case target.IsDir():
		w.skip(path, SkipSymlinkDir)
	default:
		w.takeFile(path, target)
	}
	return nil
}

// takeFile applies the three name filters and records the file. info may be nil,
// in which case the stat is done here.
func (w *walker) takeFile(path string, info fs.FileInfo) {
	name := filepath.Base(path)
	if w.isExcluded(path) {
		w.skip(path, SkipExcluded)
		return
	}
	if isHidden(name) {
		w.skip(path, SkipHidden)
		return
	}
	if isPartialDownload(name) {
		w.skip(path, SkipPartial)
		return
	}
	if info == nil {
		st, err := os.Stat(path)
		if err != nil {
			w.skip(path, SkipUnreadable)
			return
		}
		info = st
	}
	w.files.Files = append(w.files.Files, FileRef{
		Path:      path,
		Size:      info.Size(),
		SizeKnown: true,
		Ext:       strings.ToLower(filepath.Ext(name)),
		ModTime:   info.ModTime(),
		Kind:      KindUnknown,
	})
}

// isExcluded reports whether path is one of the excluded subtrees.
func (w *walker) isExcluded(path string) bool {
	for _, ex := range w.exclude {
		if path == ex || strings.HasPrefix(path, ex+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// isHidden is the dot-file rule, and the one reason the walk never enters a
// hidden directory: everything inside one is hidden with it.
func isHidden(name string) bool { return strings.HasPrefix(name, ".") }

// isPartialDownload reports the two markers a half-downloaded file carries:
// `.part` from most clients and `.!qB` from qBittorrent.
func isPartialDownload(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".part") || strings.HasSuffix(lower, ".!qb")
}

// absolute cleans and absolutises a list of paths. Roots and excludes are
// compared as prefixes, so they have to be in one form before anything compares
// them.
func absolute(paths []string) ([]string, error) {
	out := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		abs = filepath.Clean(abs)
		if seen[abs] {
			continue
		}
		seen[abs] = true
		out = append(out, abs)
	}
	sort.Strings(out)
	return out, nil
}
