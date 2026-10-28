package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/2017fighting/mdc-lite/internal/config"
)

// writeFile puts a file of the requested size on disk, so that the size the scan
// reports is a real stat and not something the test told it to say.
func writeFile(t *testing.T, path string, size int) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func runScan(t *testing.T, w config.ScanWith, in Input) (*FileSet, *ScanSummary) {
	t.Helper()
	step := newScan(w)
	step.(inputStep).setInput(in)
	st := &State{}
	if err := step.Run(context.Background(), st); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return st.Files, st.ScanRun
}

func skippedReasons(fs *FileSet) map[string]int {
	out := map[string]int{}
	for _, s := range fs.Skipped {
		out[s.Reason]++
	}
	return out
}

func basenamesOf(fs *FileSet) []string {
	out := make([]string, len(fs.Files))
	for i, f := range fs.Files {
		out[i] = filepath.Base(f.Path)
	}
	sort.Strings(out)
	return out
}

func TestScan_WalksARootRecursively(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.mp4"), 10)
	writeFile(t, filepath.Join(root, "sub", "b.mkv"), 20)

	fs, summary := runScan(t, config.ScanWith{Roots: []string{root}}, Input{})
	if got := basenamesOf(fs); strings.Join(got, ",") != "a.mp4,b.mkv" {
		t.Fatalf("files = %v", got)
	}
	if summary.Roots != 1 || summary.Files != 2 {
		t.Errorf("summary = %+v", summary)
	}
	for _, f := range fs.Files {
		if !f.SizeKnown || f.Size == 0 {
			t.Errorf("%s: the scan reports a real size", f.Path)
		}
		if f.Kind != KindUnknown {
			t.Errorf("%s: kind = %q; only group decides video vs companion", f.Path, f.Kind)
		}
	}
}

func TestScan_TakesAFileRootOnItsOwn(t *testing.T) {
	dir := t.TempDir()
	one := writeFile(t, filepath.Join(dir, "one.mp4"), 1)
	writeFile(t, filepath.Join(dir, "two.mp4"), 1)

	fs, _ := runScan(t, config.ScanWith{Roots: []string{one}}, Input{})
	if got := basenamesOf(fs); len(got) != 1 || got[0] != "one.mp4" {
		t.Fatalf("files = %v, want just the file that was named", got)
	}
}

func TestScan_FiltersOnlyHiddenAndDownloadMarkers(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "keep.mp4"), 1)
	writeFile(t, filepath.Join(root, ".hidden.mp4"), 1)
	writeFile(t, filepath.Join(root, ".hidden", "inside.mp4"), 1)
	writeFile(t, filepath.Join(root, "downloading.mp4.part"), 1)
	writeFile(t, filepath.Join(root, "downloading2.mp4.!qB"), 1)
	writeFile(t, filepath.Join(root, "notes.txt"), 1)
	writeFile(t, filepath.Join(root, "noext"), 1)

	fs, summary := runScan(t, config.ScanWith{Roots: []string{root}}, Input{})
	if got := basenamesOf(fs); strings.Join(got, ",") != "keep.mp4,noext,notes.txt" {
		t.Fatalf("files = %v: extensions are a filter rule, not a scan decision", got)
	}
	reasons := skippedReasons(fs)
	if reasons[SkipHidden] != 2 {
		t.Errorf("hidden = %d, want the dot-file and the dot-directory", reasons[SkipHidden])
	}
	if reasons[SkipPartial] != 2 {
		t.Errorf("partial = %d, want .part and .!qB", reasons[SkipPartial])
	}
	if summary.Walked != summary.Files+len(fs.Skipped) {
		t.Errorf("walked = %d, files = %d, skipped = %d: each visited entry has to land somewhere",
			summary.Walked, summary.Files, len(fs.Skipped))
	}
}

func TestScan_ExcludesASubtree(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "keep.mp4"), 1)
	writeFile(t, filepath.Join(root, "ignore", "nested", "deep.mp4"), 1)
	writeFile(t, filepath.Join(root, "ignored-partner.mp4"), 1)

	fs, _ := runScan(t, config.ScanWith{
		Roots:   []string{root},
		Exclude: []string{filepath.Join(root, "ignore")},
	}, Input{})
	if got := basenamesOf(fs); strings.Join(got, ",") != "ignored-partner.mp4,keep.mp4" {
		t.Fatalf("files = %v: exclude is a subtree, not a prefix of every name", got)
	}
	if reasons := skippedReasons(fs); reasons[SkipExcluded] != 1 {
		t.Errorf("excluded = %d, want the one directory", reasons[SkipExcluded])
	}
}

func TestScan_InputOverridesTheConfiguredRoots(t *testing.T) {
	confRoot, injected := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(confRoot, "from-file.mp4"), 1)
	writeFile(t, filepath.Join(injected, "from-trigger.mp4"), 1)

	fs, _ := runScan(t, config.ScanWith{Roots: []string{confRoot}}, Input{Roots: []string{injected}})
	if got := basenamesOf(fs); strings.Join(got, ",") != "from-trigger.mp4" {
		t.Fatalf("files = %v: a trigger's roots win over scan.with.roots", got)
	}

	fs, _ = runScan(t, config.ScanWith{Roots: []string{confRoot}}, Input{})
	if got := basenamesOf(fs); strings.Join(got, ",") != "from-file.mp4" {
		t.Fatalf("files = %v: with nothing injected the file's roots are used", got)
	}
}

func TestScan_NoRootsIsAnError(t *testing.T) {
	step := newScan(config.ScanWith{})
	err := step.Run(context.Background(), &State{})
	if err == nil || !strings.Contains(err.Error(), "no roots") {
		t.Fatalf("got %v", err)
	}
}

func TestScan_MissingRootIsAnError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	step := newScan(config.ScanWith{Roots: []string{missing}})
	err := step.Run(context.Background(), &State{})
	if err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("got %v, want it to name the root", err)
	}
}

func TestScan_FollowsAFileSymlinkButNotADirectorySymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := writeFile(t, filepath.Join(outside, "target.mp4"), 1)
	nested := writeFile(t, filepath.Join(outside, "dir", "deep.mp4"), 1)
	_ = nested
	if err := os.Symlink(target, filepath.Join(root, "link.mp4")); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "dir"), filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}

	fs, _ := runScan(t, config.ScanWith{Roots: []string{root}}, Input{})
	if got := basenamesOf(fs); strings.Join(got, ",") != "link.mp4" {
		t.Fatalf("files = %v, want the file symlink taken as its target", got)
	}
	if reasons := skippedReasons(fs); reasons[SkipSymlinkDir] != 1 {
		t.Errorf("reasons = %v: a directory symlink is never followed", reasons)
	}
}
