package service

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/2017fighting/mdc-lite/internal/config"
)

// This file is the container-internal directory picker's only endpoint:
// `GET /api/fs/dirs`. It lists directories, never files, and only inside
// `browse_roots` — the picker is how a human says "this directory", and the
// whitelist is the whole security boundary (ADR-0016).

// BrowseLimit is how many child directories one listing returns. A media root with
// more than 200 children is a directory a human should not be scrolling anyway;
// the cap also keeps one request from turning into 200 NFS round trips when stats
// are asked for.
const BrowseLimit = 200

// videoExtensions is what `stats=1` counts as a video in a subdirectory. It is the
// picker's own list: the pipeline's rule table remains the authority on what enters
// a run, and this column is only "how much of this looks like footage".
var videoExtensions = map[string]bool{
	".mp4": true, ".mkv": true, ".avi": true, ".wmv": true, ".mov": true,
	".m4v": true, ".ts": true, ".m2ts": true, ".mpg": true, ".mpeg": true,
	".rmvb": true, ".flv": true, ".webm": true, ".iso": true,
}

// BrowseDir is one child directory. Entries and Videos are set only when the caller
// asked for `stats=1`: their absence is what keeps "no statistics" from looking
// like "an empty directory".
type BrowseDir struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// Entries counts the directory's direct children; Videos counts the ones among
	// them that look like footage. Neither recurses.
	Entries *int `json:"entries,omitempty"`
	Videos  *int `json:"videos,omitempty"`
}

// BrowseRootView is one entry of the picker: a `browse_roots` path with its mount
// label. The Deck draws every one of them as an entry point, which is how the roots
// themselves replace the prototype's preset chips.
type BrowseRootView struct {
	Path  string `json:"path"`
	Label string `json:"label"`
}

// BrowseResult is the picker's answer: the directory that was read, its mount label,
// the whitelist's entry points, and its direct children sorted by name. An empty
// directory is `[]`, never null.
type BrowseResult struct {
	Path string `json:"path"`
	// Label is the mount label of the browse root Path lives under. It is the one
	// place the UI can show "this is the VR share" rather than a bare path.
	Label string           `json:"label"`
	Roots []BrowseRootView `json:"roots"`
	Dirs  []BrowseDir      `json:"dirs"`
}

// Browse lists the child directories of path when path is inside `browse_roots`.
//
// The comparison is lexical (Clean + Abs, no symlink resolution) on purpose: the
// whitelist protects paths, not inodes, and resolving would let a symlink decide
// what the operator allowed.
func (s *Service) Browse(_ context.Context, path string, stats bool) (BrowseResult, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		// Bootstrap: the Deck draws one entry point per `browse_roots` entry, and it
		// cannot guess those paths. Asking without a path therefore answers the
		// whitelist and lists nothing — it never lists a directory the caller did
		// not name.
		return BrowseResult{Roots: s.browseRootViews(), Dirs: []BrowseDir{}}, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return BrowseResult{}, badRequest(CodeRootNotBrowsable, "the path cannot be resolved: "+err.Error())
	}
	if !filepath.IsAbs(path) || s.approvingRoot(abs) == nil {
		return BrowseResult{}, badRequestf(CodeRootNotBrowsable,
			"%s is not inside browse_roots (have: %s)", abs, s.describeBrowseRoots())
	}
	info, err := os.Stat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return BrowseResult{}, notFound("no such directory: %s", abs)
	case err != nil:
		return BrowseResult{}, badRequest(CodeBadRequest, err.Error())
	case !info.IsDir():
		return BrowseResult{}, badRequestf(CodeBadRequest, "%s is not a directory", abs)
	}

	entries, err := os.ReadDir(abs)
	if err != nil {
		return BrowseResult{}, badRequest(CodeBadRequest, err.Error())
	}
	out := BrowseResult{
		Path:  abs,
		Label: s.labelOf(abs),
		Roots: s.browseRootViews(),
		Dirs:  make([]BrowseDir, 0, len(entries)),
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		child := BrowseDir{Name: e.Name(), Path: filepath.Join(abs, e.Name())}
		if stats {
			entries, videos := countChildren(child.Path)
			child.Entries, child.Videos = &entries, &videos
		}
		out.Dirs = append(out.Dirs, child)
	}
	sort.Slice(out.Dirs, func(i, j int) bool { return out.Dirs[i].Name < out.Dirs[j].Name })
	if len(out.Dirs) > BrowseLimit {
		out.Dirs = out.Dirs[:BrowseLimit]
	}
	return out, nil
}

// approvingRoot is the `browse_roots` entry that makes abs browsable, or nil when
// the whitelist does not cover it. With nested roots the innermost one approves: the
// label then matches the mount a human is actually looking at, which is the same
// choice the Deck makes when it labels a path.
func (s *Service) approvingRoot(abs string) *config.BrowseRoot {
	var approved *config.BrowseRoot
	best := -1
	for i, root := range s.settings.BrowseRoots {
		rootAbs, err := filepath.Abs(root.Path)
		if err != nil {
			continue
		}
		if withinPath(abs, rootAbs) && len(rootAbs) > best {
			best = len(rootAbs)
			approved = &s.settings.BrowseRoots[i]
		}
	}
	return approved
}

// labelOf is the mount label for a browsable path, empty when nothing covers it.
func (s *Service) labelOf(abs string) string {
	if root := s.approvingRoot(abs); root != nil {
		return root.Label
	}
	return ""
}

// browseRootViews is the whitelist as the Deck's entry points. Labels fall back to
// the path, which is what the config loader does too (a label is a convenience).
func (s *Service) browseRootViews() []BrowseRootView {
	out := make([]BrowseRootView, 0, len(s.settings.BrowseRoots))
	for _, root := range s.settings.BrowseRoots {
		label := root.Label
		if label == "" {
			label = root.Path
		}
		out = append(out, BrowseRootView{Path: root.Path, Label: label})
	}
	return out
}

// describeBrowseRoots renders the whitelist for the error message, so an operator
// does not have to read the config file to find out what is browsable.
func (s *Service) describeBrowseRoots() string {
	if len(s.settings.BrowseRoots) == 0 {
		return "none"
	}
	paths := make([]string, len(s.settings.BrowseRoots))
	for i, r := range s.settings.BrowseRoots {
		paths[i] = r.Path
	}
	return strings.Join(paths, ", ")
}

// countChildren does the one readdir `stats=1` is allowed per child directory: how
// many direct children it has, and how many of them look like videos. It never
// recurses, and a directory it cannot read counts as empty rather than failing the
// whole listing.
func countChildren(dir string) (entries, videos int) {
	children, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	for _, c := range children {
		entries++
		if c.IsDir() {
			continue
		}
		if videoExtensions[strings.ToLower(filepath.Ext(c.Name()))] {
			videos++
		}
	}
	return entries, videos
}
