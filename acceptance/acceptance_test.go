//go:build acceptance

package acceptance

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// release is one corpus directory and what the pipeline must have made of it: the
// number it parsed to (which is the output directory's name), and the video file
// names the output side must carry.
type release struct {
	Number string
	// Videos are the file names inside the release's output directory, in part order.
	// A single-file release keeps the source file's name; a multi-disc release is
	// `{number}-cd{part}`, which is the naming rule this environment pins down.
	Videos []string
}

// releases is the main chain's expectation, in the corpus's own order.
var releases = []release{
	{Number: "CAWD-279", Videos: []string{"CAWD-279.mp4"}},
	// The `-C` in the directory and file name is a flag, not part of the number.
	{Number: "MIDA-557", Videos: []string{"MIDA-557-C.mp4"}},
	// A scene-named release: the number is inside the noise, the file keeps its name.
	{Number: "MIDV-804", Videos: []string{"MIDV-804.2025.1080p.DMM.WEB-DL.AAC2.0.HFR.H.264-MTeam.mkv"}},
	// Two discs of one number: the only case where the file template applies.
	{Number: "SSIS-001", Videos: []string{"SSIS-001-cd1.mp4", "SSIS-001-cd2.mp4"}},
}

// TestAcceptance is the gate. It brings the environment up from nothing, runs the
// real binary over the corpus, and asserts what the media server read.
//
// Nothing here counts entries in the library: the primer this environment plants is
// one of its own, and a media server that also indexed something else is not a
// failure. Assertions look for the values they expect (a title, a premiere date, a
// second media source) and for the values that must not be there.
func TestAcceptance(t *testing.T) {
	e := newEnvironment(t)
	p := &product{base: e.Server}

	batch := p.createBatch(t, "acceptance", []string{filepath.Join(e.Scratch, string(RootLibrary))})
	mainTasks := e.waitForBatchSettled(t, p, batch.BatchID)
	for _, task := range mainTasks {
		t.Logf("main batch task %d: number=%q status=%s stage=%s out=%s missing=%v",
			task.ID, task.Number, task.Status, task.Stage, task.OutDir, task.Missing)
	}

	var (
		ran     []string
		library []item
	)

	t.Run("① the main chain produces a release the media server reads", func(t *testing.T) {
		// The output side first: the files are what the pipeline claims it did.
		nfos := map[string]nfo{}
		for _, rel := range releases {
			dir := filepath.Join(e.Library, rel.Number)
			for _, name := range rel.Videos {
				if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
					t.Errorf("%s: %v", rel.Number, err)
				}
			}
			for _, name := range []string{"movie.nfo", "fanart.jpg", "poster.jpg"} {
				if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
					t.Errorf("%s: %v", rel.Number, err)
				}
			}
			nfos[rel.Number] = readNFO(t, dir)
		}

		// The replay side: each release's page has to have come from the fixtures. A
		// green run whose metadata arrived from somewhere else would be measuring the
		// network, not the pipeline.
		for _, rel := range releases {
			served := false
			for _, req := range e.Replay.Requests() {
				if req.Path == "/"+rel.Number && req.Status == http.StatusOK {
					served = true
					break
				}
			}
			if !served {
				t.Errorf("the replay never answered a page request for %s, so its metadata did not come from the committed captures",
					rel.Number)
			}
		}

		// The server side: it is told to look, and then the run waits for the items it
		// is supposed to find — not for a duration.
		e.JF.scan(t, e.Library)
		e.waitFor(t, 4*time.Minute, "the media server to index every release with the NFO's own fields", func() (bool, string) {
			library = e.JF.movies(t)
			var problems []string
			for _, rel := range releases {
				matches := movieItemsByNumber(library, rel.Number)
				if len(matches) != 1 {
					// Two entries for one release is the aggregation failing, and no entry
					// is the scan missing it; both are "not what the layout promises".
					problems = append(problems, rel.Number+": "+strconv.Itoa(len(matches))+" library entries")
					continue
				}
				it := &matches[0]
				// The same absolute path inside and outside the container is what makes a
				// library scan possible at all: the server has to be looking at the files
				// the pipeline wrote, not at a mount point of its own.
				if !strings.HasPrefix(it.Path, e.Library) {
					problems = append(problems, rel.Number+": the server reports "+it.Path+
						", which is not under the output root "+e.Library)
					continue
				}
				want := nfos[rel.Number]
				if it.Name != want.Title {
					// A title could have come from the directory name; agreement with the
					// NFO is what says the file was read.
					problems = append(problems, rel.Number+": the server's title is not the nfo's ("+want.Title+")")
				}
				if want.Premiered != "" && !strings.HasPrefix(it.PremiereDate, want.Premiered) {
					// A premiere date cannot be derived from a directory name at all.
					problems = append(problems, rel.Number+": the server's premiere date is not the nfo's ("+want.Premiered+")")
				}
			}
			if len(problems) > 0 {
				return false, strings.Join(problems, "; ")
			}
			return true, ""
		})
		ran = append(ran, "① main chain: 4 releases written and read back by the media server")
	})

	t.Run("② a two-disc release is one entry carrying both discs, its stills beside it", func(t *testing.T) {
		// The stills have to be in the release's own directory: that is the layout the
		// media server reads extrafanart from, and putting them anywhere else would
		// make this assertion about a directory the product does not write.
		stillsDir := filepath.Join(e.Library, "SSIS-001", "extrafanart")
		entries, err := os.ReadDir(stillsDir)
		if err != nil {
			t.Fatalf("the extrafanart directory is not beside the movie.nfo: %v", err)
		}
		if len(entries) == 0 {
			t.Fatal("extrafanart/ is empty, so nothing was scraped into it")
		}

		// One release, one entry: a second movie entry for the same directory would be
		// the layout failing to aggregate.
		matches := movieItemsByNumber(library, "SSIS-001")
		if len(matches) != 1 {
			t.Fatalf("SSIS-001 is %d entries in the library (%v); two discs are one release",
				len(matches), pathsOf(matches))
		}
		it := matches[0]

		// …and the entry has to carry the second disc, which is how the server spells
		// "these two files are the same movie".
		parts := e.JF.additionalParts(t, it.ID)
		if len(parts) != 1 || !strings.Contains(parts[0].Path, "SSIS-001-cd2") {
			t.Errorf("the entry's additional parts are %v, want the second disc", pathsOf(parts))
		}

		// Measured, not assumed — the two layout claims that had never been checked
		// against a real server. `IsInMixedFolder` is the one that decides whether the
		// stills beside the movie are read at all.
		mixed := e.JF.mixedFolder(t, e.jellyfinDB(), it.Path)
		t.Logf("measured: SSIS-001 IsInMixedFolder=%v backdrops=%d primary=%q stills=%d",
			mixed, len(it.BackdropImageTag), it.ImageTags["Primary"], len(entries))
		if mixed {
			t.Errorf("the media server reads the two-disc release as living in a mixed folder; " +
				"that is the layout claim this environment exists to check")
		}

		// The stills have to have been read: one backdrop for the cover, then one per
		// still. This is the only entry count in the file, and it is a count *within*
		// one release, because "each still in extrafanart/ becomes a backdrop" is
		// exactly the claim.
		if want := 1 + len(entries); len(it.BackdropImageTag) != want {
			t.Errorf("%d backdrops for one cover and %d stills, want %d: the extrafanart beside the movie was not read",
				len(it.BackdropImageTag), len(entries), want)
		}
		ran = append(ran, "② two discs are one entry with the second disc attached; IsInMixedFolder measured false; stills read as backdrops")
	})

	t.Run("⑤ the part basenames are {number}-cd{part}", func(t *testing.T) {
		// The file name is the rule the media server's own aggregation depends on: a
		// multi-disc directory whose files are not `{number}-cd{n}` is two movies.
		dir := filepath.Join(e.Library, "SSIS-001")
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		present := map[string]bool{}
		var names []string
		for _, entry := range entries {
			present[entry.Name()] = true
			names = append(names, entry.Name())
		}
		for _, want := range []string{"SSIS-001-cd1.mp4", "SSIS-001-cd2.mp4"} {
			if !present[want] {
				t.Errorf("%s is missing (the directory holds %v)", want, names)
			}
		}
		if present["SSIS-001.mp4"] {
			t.Error("a multi-disc release also wrote a bare `SSIS-001.mp4`; the part names are the whole point")
		}
		ran = append(ran, "⑤ multi-disc basenames are {number}-cd{part}")
	})

	t.Run("⑥ the grouping counterexamples stay counterexamples", func(t *testing.T) {
		batch := p.createBatch(t, "acceptance", []string{filepath.Join(e.Scratch, string(RootCounterexamples))})
		tasks := e.waitForBatchSettled(t, p, batch.BatchID)
		for _, task := range tasks {
			t.Logf("counterexample task %d: number=%q status=%s files-on-disk=%v last_error=%q",
				task.ID, task.Number, task.Status, detailFiles(t, p, task.ID), task.LastError)
		}

		// Neighbours must not merge: one release each, one file each.
		for _, number := range []string{"ABP-647", "ABP-648"} {
			task, ok := taskByNumber(tasks, number)
			if !ok {
				t.Errorf("no task parsed to %s: the neighbour pair was not two releases at all", number)
				continue
			}
			if files := detailFiles(t, p, task.ID); len(files) != 1 {
				t.Errorf("%s absorbed another file (%v); neighbours without part markers must not merge", number, files)
			}
		}

		// Two explicitly marked discs whose stems are one digit apart do merge, and the
		// number is the primary's.
		merged, ok := taskByNumber(tasks, "SSVR-122")
		if !ok {
			t.Error("SSVR-122-cd1 and SSVR-123-cd2 did not become one release")
		} else if files := detailFiles(t, p, merged.ID); len(files) != 2 {
			t.Errorf("SSVR-122 holds %v, want both discs", files)
		}
		if _, split := taskByNumber(tasks, "SSVR-123"); split {
			t.Error("SSVR-123 also became a release; the two marked discs were split instead of merged")
		}

		// Marked but too far apart: two releases, not one. This is the pair the group
		// step records as "looks like parts, did not merge".
		for _, number := range []string{"ABC-123", "ABC-456"} {
			task, ok := taskByNumber(tasks, number)
			if !ok {
				t.Errorf("the far-apart marked pair lost %s; it must stay two releases", number)
				continue
			}
			if files := detailFiles(t, p, task.ID); len(files) != 1 {
				t.Errorf("%s holds %v, want only its own disc", number, files)
			}
		}

		// The same stem twice with no part marker on either side goes to a human, with
		// the reason as a machine code and both files in the task so a person can
		// decide what they are.
		var waiting []taskView
		for _, task := range tasks {
			if task.Status == "needs_number" {
				waiting = append(waiting, task)
			}
		}
		if len(waiting) != 1 {
			t.Fatalf("tasks waiting for a number = %d (%v), want the duplicate stem and only it",
				len(waiting), numbersOf(waiting))
		}
		if waiting[0].LastError != "duplicate_stem" {
			t.Errorf("the waiting task says %q, want duplicate_stem", waiting[0].LastError)
		}
		if files := detailFiles(t, p, waiting[0].ID); len(files) != 2 {
			t.Errorf("the duplicate-stem task holds %v, want both copies so a human can decide", files)
		}

		// A lone trailing digit belongs to the number, not to a part marker: one file,
		// one task, no `-cd` rename. (What the number *reads* is the parse ticket's
		// business; this assertion is the grouping rule.)
		lone, ok := taskByFile(t, p, tasks, "ABC-124-1.mp4")
		if !ok {
			t.Error("ABC-124-1.mp4 is not in any task")
		} else if len(lone.TaskFiles) != 1 {
			t.Errorf("ABC-124-1.mp4 gathered %d files; a lone trailing digit is not a part", len(lone.TaskFiles))
		}

		ran = append(ran, "⑥ grouping counterexamples: neighbours split, marked pair merged, far pair split, duplicate stem to a human")
	})

	// The marker is the run's own record that it asserted something. A gate that
	// skipped every arm would otherwise still exit 0 — and one that failed must not
	// leave the marker either, or "the marker is present" would stop meaning "the
	// gate passed".
	if len(ran) == 0 {
		t.Fatal("no assertion arm ran")
	}
	if t.Failed() {
		t.Logf("ACCEPTANCE FAILED: an assertion arm failed, so no positive marker was written")
		return
	}
	e.markRan(t, ran...)
}

// movieItemsByNumber lists the library entries whose path is inside a release's
// directory. The directory is the key because the entry's own name comes from the NFO.
func movieItemsByNumber(items []item, number string) []item {
	var out []item
	for _, it := range items {
		if strings.Contains(it.Path, string(filepath.Separator)+number+string(filepath.Separator)) {
			out = append(out, it)
		}
	}
	return out
}

func pathsOf(items []item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Path)
	}
	return out
}

// taskByNumber finds the one task that parsed to a number.
func taskByNumber(tasks []taskView, number string) (taskView, bool) {
	for _, task := range tasks {
		if task.Number == number {
			return task, true
		}
	}
	return taskView{}, false
}

// taskByFile finds the task that holds a given input file, with its files read.
func taskByFile(t *testing.T, p *product, tasks []taskView, base string) (taskDetail, bool) {
	t.Helper()
	for _, task := range tasks {
		detail := p.detail(t, task.ID)
		for _, file := range detail.TaskFiles {
			if filepath.Base(file.Path) == base {
				return detail, true
			}
		}
	}
	return taskDetail{}, false
}

func numbersOf(tasks []taskView) []string {
	out := make([]string, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, task.Number)
	}
	return out
}

// detailFiles lists one task's file paths, from the detail endpoint the UI uses.
func detailFiles(t *testing.T, p *product, id int64) []string {
	t.Helper()
	out := []string{}
	for _, file := range p.detail(t, id).TaskFiles {
		out = append(out, filepath.Base(file.Path))
	}
	return out
}
