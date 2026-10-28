package pipeline

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2017fighting/mdc-lite/internal/config"
)

// fileAt builds a FileRef without touching the disk: filtering is a decision
// about a path, a size and an extension, so making it need real files would only
// make the cases slower to write.
func fileAt(path string, size int64) FileRef {
	return FileRef{
		Path:      path,
		Size:      size,
		SizeKnown: true,
		Ext:       strings.ToLower(filepath.Ext(path)),
	}
}

// filterFiles runs the filter step over files of one byte each.
func filterFiles(t *testing.T, rules []config.FilterRule, files ...string) *Candidates {
	t.Helper()
	fs := &FileSet{}
	for _, name := range files {
		fs.Files = append(fs.Files, fileAt(name, 1))
	}
	return runFilter(t, rules, fs)
}

func runFilter(t *testing.T, rules []config.FilterRule, fs *FileSet) *Candidates {
	t.Helper()
	step, err := newFilter(config.FilterWith{Rules: rules})
	if err != nil {
		t.Fatalf("newFilter: %v", err)
	}
	st := &State{Files: fs}
	if err := step.Run(context.Background(), st); err != nil {
		t.Fatalf("filter: %v", err)
	}
	return st.Candidates
}

func rule(name, then string, when config.FilterWhen) config.FilterRule {
	return config.FilterRule{Name: name, Then: then, When: when}
}

func extIn(name string, then string, exts ...string) config.FilterRule {
	return rule(name, then, config.FilterWhen{ExtIn: exts})
}

func sizes(n int64) *int64 { return &n }

// names renders the candidate paths, for assertions.
func candidateNames(c *Candidates) []string {
	out := make([]string, len(c.Files))
	for i, f := range c.Files {
		out[i] = f.File.Path
	}
	return out
}

func TestFilter_FirstHitWins(t *testing.T) {
	// The broad exclude rule comes first on purpose: order is the semantics, and a
	// later rule must not get a second chance at a file an earlier one claimed.
	rules := []config.FilterRule{
		extIn("everything-else", "exclude", ".mp4"),
		extIn("videos", "include", ".mp4"),
	}
	c := filterFiles(t, rules, "/media/a/x.mp4")
	if len(c.Files) != 0 {
		t.Fatalf("candidates = %v, want none: the first rule already decided", candidateNames(c))
	}
	if len(c.Rejected) != 1 || c.Rejected[0].Rule != "everything-else" {
		t.Fatalf("rejected = %+v", c.Rejected)
	}
}

func TestFilter_ExtInAndExtNotIn(t *testing.T) {
	c := filterFiles(t, []config.FilterRule{
		extIn("companions", "include", ".srt", ".ass"),
		extIn("videos", "include", ".mp4", ".mkv"),
	}, "/media/a/x.mp4", "/media/a/x.srt", "/media/a/x.txt")

	if len(c.Files) != 2 {
		t.Fatalf("candidates = %v, want the video and the subtitle", candidateNames(c))
	}
	if c.Files[0].Rule != "videos" || c.Files[1].Rule != "companions" {
		t.Errorf("rules = %q, %q", c.Files[0].Rule, c.Files[1].Rule)
	}
	if len(c.Rejected) != 1 || c.Rejected[0].Rule != SkipNoMatch {
		t.Errorf("rejected = %+v, want the .txt with no_match", c.Rejected)
	}

	notIn := filterFiles(t, []config.FilterRule{
		rule("not-video", "exclude", config.FilterWhen{ExtNotIn: []string{".mp4"}}),
		extIn("videos", "include", ".mp4"),
	}, "/media/a/x.mp4", "/media/a/x.srt")
	if len(notIn.Files) != 1 || notIn.Files[0].File.Ext != ".mp4" {
		t.Fatalf("candidates = %v", candidateNames(notIn))
	}
}

func TestFilter_MinSizeHitsWhenSmaller(t *testing.T) {
	rules := []config.FilterRule{
		rule("too-small", "exclude", config.FilterWhen{MinSize: sizes(100)}),
		extIn("videos", "include", ".mp4"),
	}
	c := runFilter(t, rules, &FileSet{Files: []FileRef{
		fileAt("/media/a/small.mp4", 99),
		fileAt("/media/a/exactly.mp4", 100),
		fileAt("/media/a/big.mp4", 101),
	}})
	if len(c.Files) != 2 {
		t.Fatalf("candidates = %v, want the 100-byte and the 101-byte file", candidateNames(c))
	}
	if len(c.Rejected) != 1 || c.Rejected[0].File.Path != "/media/a/small.mp4" {
		t.Errorf("rejected = %+v: only a strictly smaller file matches", c.Rejected)
	}
}

func TestFilter_StemMatchesUsesTheRawBaseName(t *testing.T) {
	// Stripping noise is `group`'s job and happens in exactly one place, so at
	// filter time the stem still carries the resolution token.
	rules := []config.FilterRule{
		rule("hi-res", "include", config.FilterWhen{StemMatches: `-1080p$`}),
	}
	c := filterFiles(t, rules, "/media/a/ABC-123-1080p.mp4")
	if len(c.Files) != 1 {
		t.Fatalf("candidates = %v, want the 1080p file", candidateNames(c))
	}
	if c.Files[0].Rule != "hi-res" {
		t.Errorf("rule = %q", c.Files[0].Rule)
	}
}

func TestFilter_SummaryCountsByRuleName(t *testing.T) {
	st := &State{Files: &FileSet{Files: []FileRef{
		fileAt("/media/a/one.mp4", 1),
		fileAt("/media/a/two.mp4", 1),
		fileAt("/media/a/three.txt", 1),
	}}}
	step, err := newFilter(config.FilterWith{Rules: []config.FilterRule{
		extIn("videos", "include", ".mp4"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := step.Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if st.FilterRun.In != 2 || st.FilterRun.Out != 1 {
		t.Errorf("in = %d, out = %d, want 2 and 1", st.FilterRun.In, st.FilterRun.Out)
	}
	if got := st.FilterRun.Skipped[SkipNoMatch]; got != 1 {
		t.Errorf("skipped[%s] = %d, want 1", SkipNoMatch, got)
	}
}

func TestFilter_ExtensionsCompareCaseInsensitively(t *testing.T) {
	fs := &FileSet{Files: []FileRef{fileAt("/media/a/x.MP4", 1)}}
	c := runFilter(t, []config.FilterRule{extIn("videos", "include", ".mp4")}, fs)
	if len(c.Files) != 1 {
		t.Fatalf("a .MP4 file must match an .mp4 rule: %+v", c.Rejected)
	}
}
