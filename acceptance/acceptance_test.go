//go:build acceptance

package acceptance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
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
// real binary over the corpus, and asserts what the media server read and what the
// database recorded.
//
// Nothing here counts entries in the library: the primer this environment plants is
// one of its own, and a media server that also indexed something else is not a
// failure. Assertions look for the values they expect (a title, a premiere date, a
// second media source), for the values that must not be there, and for the run's own
// records of what it did.
//
// The arms run in one order, and the order is part of the test: the interface arm
// needs the server and the waiting task the counterexample batch leaves behind, and
// the interrupted-write arm takes the server away on purpose, so it comes last.
func TestAcceptance(t *testing.T) {
	e := newEnvironment(t)
	p := &product{base: e.Server}

	var (
		ran []string
		// library is the media server's view of the output root, read once in ① and
		// reused by ②.
		library []item
	)

	mainBatch := p.createBatch(t, "acceptance", []string{filepath.Join(e.Scratch, string(RootLibrary))})
	mainTasks := e.waitForBatchSettled(t, p, mainBatch.BatchID)
	for _, task := range mainTasks {
		t.Logf("main batch task %d: number=%q status=%s stage=%s out=%s missing=%v",
			task.ID, task.Number, task.Status, task.Stage, task.OutDir, task.Missing)
	}

	t.Run("the media server is the pinned version", func(t *testing.T) {
		// An upgrade is an explicit act, and this is what makes it one: the run refuses
		// a server that is not the tag compose.yaml pins. Assertion ② measures
		// `IsInMixedFolder` again on every run, so an upgrade cannot skip the
		// re-measurement — it fails here until the pin is changed on purpose.
		version := e.JF.version(t)
		tag := e.image[strings.LastIndex(e.image, ":")+1:]
		if tag == "" || tag == "latest" || strings.Contains(e.image, "@") {
			t.Fatalf("the media server image %q does not pin a version; an unpinned server makes ② measure something else every night", e.image)
		}
		if !strings.HasPrefix(version, tag) {
			t.Errorf("the media server answers version %s, the pin says %s: an upgrade has to be an explicit edit to compose.yaml", version, tag)
		}
		ran = append(ran, "the media server under test is the pinned "+version)
	})

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
			if !e.answered(t, "javbus.com", "/"+rel.Number, http.StatusOK) {
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

	t.Run("both terminal states are really reached, not just logged", func(t *testing.T) {
		// The old repository's gap was that javbus-only corpora stay `partial` forever and
		// `succeeded` was never observed. Here both happen in one batch, so both are
		// asserted rather than counted on.
		byNumber := tasksByNumber(mainTasks)
		// SSIS-001 is the release javdb, javbus and the corpus's own coverage fill
		// completely: all 14 source-level fields arrive, so nothing is missing.
		ssis, ok := byNumber["SSIS-001"]
		if !ok {
			t.Fatal("SSIS-001 is not in the batch")
		}
		if ssis.Status != "succeeded" {
			t.Errorf("SSIS-001 ended %q; a fully covered release has to reach `succeeded`", ssis.Status)
		}
		// The others are javbus-only, which supplies no plot, rating, extrafanarts or
		// series: a task with no missing field would be lying about the source.
		for _, number := range []string{"CAWD-279", "MIDA-557", "MIDV-804"} {
			task, ok := byNumber[number]
			if !ok {
				t.Errorf("%s is not in the batch", number)
				continue
			}
			if task.Status != "partial" {
				t.Errorf("%s ended %q; a javbus-only release ends `partial`", number, task.Status)
			}
		}

		// The status and the metadata have to agree: `succeeded` means nothing is
		// missing, and `partial` means something is.
		succeeded := p.detail(t, ssis.ID)
		if succeeded.Metadata.Partial || len(succeeded.Metadata.Missing) != 0 {
			t.Errorf("a succeeded task reports missing %v (partial=%v)",
				succeeded.Metadata.Missing, succeeded.Metadata.Partial)
		}
		partial := p.detail(t, byNumber["CAWD-279"].ID)
		if !partial.Metadata.Partial || len(partial.Metadata.Missing) == 0 {
			t.Errorf("a partial task reports nothing missing (missing=%v partial=%v)",
				partial.Metadata.Missing, partial.Metadata.Partial)
		}
		ran = append(ran, "both terminal states: SSIS-001 `succeeded` (all 14 fields), the javbus-only releases `partial`")
	})

	// The counterexample batch runs here because its waiting task is what the
	// interface arm below fills a number into.
	counterBatch := p.createBatch(t, "acceptance", []string{filepath.Join(e.Scratch, string(RootCounterexamples))})
	counterTasks := e.waitForBatchSettled(t, p, counterBatch.BatchID)

	t.Run("⑥ the grouping counterexamples stay counterexamples", func(t *testing.T) {
		for _, task := range counterTasks {
			t.Logf("counterexample task %d: number=%q status=%s files-on-disk=%v last_error=%q",
				task.ID, task.Number, task.Status, detailFiles(t, p, task.ID), task.LastError)
		}

		// Neighbours must not merge: one release each, one file each.
		for _, number := range []string{"ABP-647", "ABP-648"} {
			task, ok := taskByNumber(counterTasks, number)
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
		merged, ok := taskByNumber(counterTasks, "SSVR-122")
		if !ok {
			t.Error("SSVR-122-cd1 and SSVR-123-cd2 did not become one release")
		} else if files := detailFiles(t, p, merged.ID); len(files) != 2 {
			t.Errorf("SSVR-122 holds %v, want both discs", files)
		}
		if _, split := taskByNumber(counterTasks, "SSVR-123"); split {
			t.Error("SSVR-123 also became a release; the two marked discs were split instead of merged")
		}

		// Marked but too far apart: two releases, not one. This is the pair the group
		// step records as "looks like parts, did not merge".
		for _, number := range []string{"ABC-123", "ABC-456"} {
			task, ok := taskByNumber(counterTasks, number)
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
		for _, task := range counterTasks {
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

		// A lone trailing digit stays in the stem and is not a part marker: one file,
		// one task, no `-cd` rename. (What the number reads is pinned just below.)
		lone, ok := taskByFile(t, p, counterTasks, "ABC-124-1.mp4")
		if !ok {
			t.Error("ABC-124-1.mp4 is not in any task")
		} else if len(lone.TaskFiles) != 1 {
			t.Errorf("ABC-124-1.mp4 gathered %d files; a lone trailing digit is not a part", len(lone.TaskFiles))
		}

		// And what the number reads is pinned too (ticket #30): the lone digit
		// stays in the stem and out of the number — ABC-124, not ABC-124-001, which no
		// site indexes. The task must be found by that number; a silent skip here would
		// let the old parse regression back in unnoticed.
		if loneByNumber, ok := taskByNumber(counterTasks, "ABC-124"); ok {
			if lone.ID != 0 && loneByNumber.ID != lone.ID {
				t.Errorf("ABC-124-1.mp4 landed in the task of number %q; the lone digit must read as ABC-124", lone.Number)
			}
		} else {
			t.Error("no task parsed to ABC-124: ABC-124-1.mp4 stopped parsing altogether")
		}
		// The required-fields gate is what turns these into failures: their numbers have
		// no capture, so no source can supply a title, and `output` refuses to write a
		// release without one. That refusal is also the other half of "narrowing
		// required_fields does not manufacture a `succeeded` task" below.
		for _, number := range []string{"ABC-124", "ABP-647"} {
			task, ok := taskByNumber(counterTasks, number)
			if !ok {
				t.Errorf("no task parsed to %s: the counterexample batch lost a release entirely", number)
				continue
			}
			if task.Status != "failed" {
				t.Errorf("%s ended %q, want failed: nothing supplied its required fields", number, task.Status)
				continue
			}
			if !strings.Contains(task.LastError, "missing required field(s)") {
				t.Errorf("%s failed for a reason other than the required-fields gate: %q", number, task.LastError)
			}
			// The message has to name the step that refused, at its position in the
			// pipeline file — the runner used to blame whichever step sat at that offset.
			if !strings.Contains(task.LastError, "(output)") {
				t.Errorf("%s: the refusal is not attributed to the output step: %q", number, task.LastError)
			}
		}

		ran = append(ran, "⑥ grouping counterexamples: neighbours split, marked pair merged, far pair split, duplicate stem to a human")
	})

	t.Run("a narrowed required_fields gate passes but does not make the task succeeded", func(t *testing.T) {
		// The parent ticket asked for a `succeeded` task made by narrowing
		// `required_fields`. That cannot happen here, and the reason is the product's
		// own semantics: the gate decides failed-or-not, while `Missing` (all 14
		// source-level fields) decides partial-or-succeeded. This arm pins that down
		// rather than pretending the narrowing did something it cannot.
		tasks := e.runBatch(t, p, "acceptance-lenient", RootSuccess)
		if len(tasks) != 1 {
			t.Fatalf("the narrowed pipeline produced %d tasks, want one", len(tasks))
		}
		task := tasks[0]
		if task.Number != "IPX-535" {
			t.Fatalf("the narrowed pipeline parsed %q, want IPX-535", task.Number)
		}
		if task.LastError != "" {
			t.Errorf("the narrowed gate refused a release that has its one required field: %q", task.LastError)
		}
		if task.Status != "partial" {
			t.Errorf("IPX-535 ended %q under `required_fields: [title]`; the gate passing does not make a javbus-only release `succeeded`",
				task.Status)
		}
		// The gate passed, so the release was written: a `partial` task with no products
		// would mean the run stopped somewhere else.
		dir := filepath.Join(e.Library, "IPX-535")
		for _, name := range []string{"IPX-535.mp4", "movie.nfo", "fanart.jpg", "poster.jpg"} {
			if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
		detail := p.detail(t, task.ID)
		if len(detail.Metadata.Missing) == 0 {
			t.Error("a partial task has to say which fields are missing")
		}
		ran = append(ran, "a narrowed `required_fields: [title]` lets the release through and the task is still `partial`: the gate is not `Partial`")
	})

	// The source answers are scripted before their batch runs, because a script is the
	// network's answer and the batch is what asks the question.
	e.scriptSourceAnswers(t)
	sourceBatch := p.createBatch(t, "acceptance", []string{filepath.Join(e.Scratch, string(RootSources))})
	sourceTasks := e.waitForBatchSettled(t, p, sourceBatch.BatchID)
	for _, task := range sourceTasks {
		t.Logf("source-answer task %d: number=%q status=%s last_error=%q", task.ID, task.Number, task.Status, task.LastError)
	}

	t.Run("⑦ the three states, and the field-level fallback", func(t *testing.T) {
		// javbus: the site's own 404 is an answer — `notfound`, which is not a failure
		// and carries no reason code. The replay really answered the captured 404 page.
		javbusMiss := e.detailByNumber(t, p, sourceTasks, "ZZZZ-9999")
		run, ok := sourceRunOf(javbusMiss, "javbus")
		if !ok {
			t.Fatal("javbus was never asked about ZZZZ-9999")
		}
		if run.Status != "notfound" {
			t.Errorf("javbus's 404 is recorded as %q, want notfound", run.Status)
		}
		if run.Reason != "" {
			t.Errorf("a notfound answer carries reason %q; `notfound` is not one of the six", run.Reason)
		}
		if fields := fieldsOf(t, run); len(fields) != 0 {
			t.Errorf("a notfound answer contributed fields: %v", fields)
		}
		if !e.answered(t, "javbus.com", "/ZZZZ-9999", http.StatusNotFound) {
			t.Error("the replay did not answer javbus's 404 page with a 404, so the capture was not replayed")
		}

		// fc2: the miss page is a 200 with the site's own marker in it, and reading it
		// as an answer rather than a failure is the claim.
		fc2Miss := e.detailByNumber(t, p, sourceTasks, "FC2-PPV-1234567")
		run, ok = sourceRunOf(fc2Miss, "fc2")
		if !ok {
			t.Fatal("fc2 was never asked about FC2-PPV-1234567")
		}
		if run.Status != "notfound" {
			t.Errorf("fc2's miss page is recorded as %q, want notfound (%s)", run.Status, run.Err)
		}
		if run.Reason != "" {
			t.Errorf("fc2's 200 miss page carries reason %q; the site answered", run.Reason)
		}
		if fields := fieldsOf(t, run); len(fields) != 0 {
			t.Errorf("a miss page contributed fields: %v", fields)
		}
		if !e.answered(t, "adult.contents.fc2.com", "/article/1234567/", http.StatusOK) {
			t.Error("the replay did not answer the fc2 miss page with a 200, so nothing proves the miss marker was read off a 200")
		}

		// Field-level fallback: one release, two sources, and each field belongs to the
		// earliest source in the candidate list that had it. javdb has thirteen of the
		// fourteen; publisher is javbus's, and nothing is missing.
		ssis, ok := taskByNumber(mainTasks, "SSIS-001")
		if !ok {
			t.Fatal("SSIS-001 is not in the main batch")
		}
		detail := p.detail(t, ssis.ID)
		if got := detail.Metadata.Sources["title"]; got != "javdb" {
			t.Errorf("title came from %q, want javdb: it is the first candidate and it had the field", got)
		}
		if got := detail.Metadata.Sources["publisher"]; got != "javbus" {
			t.Errorf("publisher came from %q, want javbus: javdb does not supply it, so the field fell through", got)
		}
		if len(detail.Metadata.Missing) != 0 {
			t.Errorf("the fallback left fields missing: %v", detail.Metadata.Missing)
		}
		ran = append(ran, "⑦ three states: javbus 404 and fc2's 200 miss page are both `notfound` with no reason code; field-level fallback fills publisher from the second source")
	})

	t.Run("⑧ step_runs and source_runs are assertable, including the six reason codes", func(t *testing.T) {
		ssis, ok := taskByNumber(mainTasks, "SSIS-001")
		if !ok {
			t.Fatal("SSIS-001 is not in the main batch")
		}
		detail := p.detail(t, ssis.ID)

		// The task's own steps, in order, at their positions in the pipeline file. A
		// task run starts after `parse`, so the four task-level steps are 5..8 and the
		// batch-level prefix is not part of this task's trace.
		want := []string{"scrape", "images", "nfo", "output"}
		var got []string
		for i, step := range detail.StepRuns {
			got = append(got, step.Type)
			if step.Status != "ok" {
				t.Errorf("step %s ended %q (%s)", step.Type, step.Status, step.Error)
			}
			if step.Idx != 5+i {
				t.Errorf("step %s is at idx %d, want %d: the trace names the pipeline's own position", step.Type, step.Idx, 5+i)
			}
			if len(step.Summary) == 0 || string(step.Summary) == "null" {
				t.Errorf("step %s has no summary to read", step.Type)
			}
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("the task's steps are %v, want %v", got, want)
		}

		// The scrape step's per-source traces: a `notfound` and an `ok`, and the fields
		// each source actually contributed (not its declared list).
		for _, id := range []string{"javdb", "javbus"} {
			run, ok := sourceRunOf(detail, id)
			if !ok {
				t.Errorf("%s has no source_runs row for SSIS-001", id)
				continue
			}
			if run.Status != "ok" {
				t.Errorf("%s ended %q for SSIS-001", id, run.Status)
			}
			if len(fieldsOf(t, run)) == 0 {
				t.Errorf("%s contributed fields but its source_runs row names none", id)
			}
		}
		counter := "CAWD-279"
		if task, ok := taskByNumber(mainTasks, counter); ok {
			run, ok := sourceRunOf(p.detail(t, task.ID), "javdb")
			if !ok {
				t.Error("javdb has no source_runs row for CAWD-279")
			} else if run.Status != "notfound" || run.Reason != "" || len(fieldsOf(t, run)) != 0 {
				t.Errorf("javdb's notfound row is status=%q reason=%q fields=%v", run.Status, run.Reason, fieldsOf(t, run))
			}
		}

		// The six reason codes, each produced by the answer the scripted route gives:
		// 500 → http_5xx (after the transport's two retries), no answer in time →
		// timeout, an outright refusal → blocked, a 503 → cooldown, a 200 that is not
		// JSON → parse, and a dead connection → egress_unavailable.
		codes := []struct {
			number string
			source string
			reason string
		}{
			{"FAIL-500", "javbus", "http_5xx"},
			{"FAIL-501", "javbus", "timeout"},
			{"FAIL-409", "javbus", "blocked"},
			{"FAIL-503", "javdb", "cooldown"},
			{"FAIL-200", "javdb", "parse"},
			{"FAIL-902", "javbus", "egress_unavailable"},
		}
		for _, want := range codes {
			detail := e.detailByNumber(t, p, sourceTasks, want.number)
			run, ok := sourceRunOf(detail, want.source)
			if !ok {
				t.Errorf("%s has no source_runs row for %s", want.number, want.source)
				continue
			}
			if run.Status != "failed" {
				t.Errorf("%s: %s ended %q, want failed", want.number, want.source, run.Status)
			}
			if run.Reason != want.reason {
				t.Errorf("%s: %s carries reason %q, want %q (%s)", want.number, want.source, run.Reason, want.reason, run.Err)
			}
			if strings.TrimSpace(run.Err) == "" {
				t.Errorf("%s: %s failed without a human-readable cause beside the code", want.number, want.source)
			}
		}

		// A reason code means "failed" and nothing else: an `ok` or `notfound` row that
		// carried one would make the column say two things at once.
		for _, task := range sourceTasks {
			for _, run := range p.detail(t, task.ID).SourceRuns {
				if run.Status == "failed" {
					continue
				}
				if run.Reason != "" {
					t.Errorf("task %s: %s is %q and carries reason %q", task.Number, run.SourceID, run.Status, run.Reason)
				}
			}
		}
		ran = append(ran, "⑧ step_runs (types, positions, summaries) and source_runs are asserted, including all six reason codes")
	})

	t.Run("④ a rerun is idempotent: identical products are skipped and keep their mtime", func(t *testing.T) {
		ssis, ok := taskByNumber(mainTasks, "SSIS-001")
		if !ok {
			t.Fatal("SSIS-001 is not in the main batch")
		}
		dir := filepath.Join(e.Library, "SSIS-001")
		watched := []string{
			"SSIS-001-cd1.mp4", "SSIS-001-cd2.mp4", "movie.nfo", "poster.jpg", "fanart.jpg",
			"extrafanart/fanart1.jpg",
		}
		before := map[string]time.Time{}
		for _, name := range watched {
			before[name] = mtimeOf(t, filepath.Join(dir, filepath.FromSlash(name)))
		}

		detail := e.rerunAndWait(t, p, ssis.ID)
		if detail.Status != "succeeded" {
			t.Errorf("the rerun ended %q, want succeeded", detail.Status)
		}

		// Every write the rerun made found the same content already there, and a skipped
		// write is what keeps the media server from rescanning a file that did not
		// change.
		if len(detail.FileRows) == 0 {
			t.Fatal("the rerun wrote no ledger rows at all")
		}
		for _, row := range detail.FileRows {
			if row.Action != "skipped" {
				t.Errorf("%s was %q on a rerun that should have found identical content", filepath.Base(row.Dst), row.Action)
			}
		}
		// …and nothing was deleted: this run produces exactly what the last one did.
		for _, row := range detail.FileRows {
			if row.Action == "deleted" {
				t.Errorf("%s was deleted on an identical rerun", row.Dst)
			}
		}
		for _, name := range watched {
			now := mtimeOf(t, filepath.Join(dir, filepath.FromSlash(name)))
			if !now.Equal(before[name]) {
				t.Errorf("%s was rewritten: mtime %v → %v", name, before[name], now)
			}
		}
		ran = append(ran, "④ a rerun of an unchanged release skipped every write and left every mtime alone")
	})

	t.Run("④ a rerun removes the stills it no longer produces, from its own ledger", func(t *testing.T) {
		ssis, ok := taskByNumber(mainTasks, "SSIS-001")
		if !ok {
			t.Fatal("SSIS-001 is not in the main batch")
		}
		stillsDir := filepath.Join(e.Library, "SSIS-001", "extrafanart")
		before := entryNames(t, stillsDir)

		// One of the release's own stills is gone from the site: the replay answers that
		// one address with a 404 from now on. That is the only honest way for a rerun to
		// legitimately produce fewer stills — editing the capture would be editing the
		// evidence, and a smaller number could not otherwise happen.
		const gone = "fanart10.jpg"
		if !contains(before, gone) {
			t.Fatalf("the first run wrote %v; this arm needs the tenth still to exist", before)
		}
		e.Replay.ScriptStatus(t, "still-9-is-gone", route("tp.spfcas.com", "/rhe951l4q/samples/zy/ZY5eq_l_9.jpg"), http.StatusNotFound)
		defer e.Replay.ClearScripts()

		detail := e.rerunAndWait(t, p, ssis.ID)
		if detail.Status != "succeeded" {
			t.Errorf("the rerun ended %q; a still that did not arrive is a missing extrafanart, not a failed task (%s)",
				detail.Status, detail.LastError)
		}

		after := entryNames(t, stillsDir)
		if len(after) != len(before)-1 {
			t.Errorf("the rerun left %d stills, want %d (was %v, now %v)", len(after), len(before)-1, before, after)
		}
		if contains(after, gone) {
			t.Errorf("%s survived a run that did not produce it: the previous run's ledger was not honoured", gone)
		}

		// The removal is on the record: one `deleted` row, naming the file, and the file
		// is really gone — the ledger row and the unlink are one fact.
		var deleted []fileRowView
		for _, row := range detail.FileRows {
			if row.Action == "deleted" {
				deleted = append(deleted, row)
			}
		}
		if len(deleted) != 1 {
			t.Fatalf("deleted rows = %d (%v), want exactly the still that did not come back", len(deleted), dstsOf(deleted))
		}
		if filepath.Base(deleted[0].Dst) != gone {
			t.Errorf("deleted %s, want %s", deleted[0].Dst, gone)
		}
		if _, err := os.Stat(deleted[0].Dst); !os.IsNotExist(err) {
			t.Errorf("the ledger says %s was deleted but it is still there (%v)", deleted[0].Dst, err)
		}
		// The stills that did come back were untouched: their bytes are the same, so the
		// writes were skipped and the media server has nothing to re-read.
		for _, row := range detail.FileRows {
			if row.Action == "deleted" || row.Role != "extrafanart" {
				continue
			}
			if row.Action != "skipped" {
				t.Errorf("%s was %q; a still that came back unchanged must not be rewritten", row.Dst, row.Action)
			}
		}
		ran = append(ran, "④ a rerun deleted exactly the still that no longer came back, and left the rest skipped")
	})

	t.Run("③ a hardlink across two real filesystems fails the task and names the fix", func(t *testing.T) {
		// The two mount points, asserted here as well as at mount time: the claim is
		// about a real `link(2)` crossing devices, and a same-device setup would make
		// this arm assert nothing.
		corpusDev, err := deviceOf(filepath.Join(e.Scratch, string(RootExdev)))
		if err != nil {
			t.Fatalf("%v", err)
		}
		// The mount, not the output root: the root is created by the run that is about
		// to fail, and everything under the mount is on the mount's device.
		outDev, err := deviceOf(e.secondFS)
		if err != nil {
			t.Fatalf("%v", err)
		}
		if corpusDev == outDev {
			t.Fatalf("the corpus and the exdev output root are both on device %d, so no hardlink can be EXDEV", corpusDev)
		}

		tasks := e.runBatch(t, p, "acceptance-exdev", RootExdev)
		if len(tasks) != 1 {
			t.Fatalf("the cross-device batch produced %d tasks, want one", len(tasks))
		}
		task := tasks[0]
		if task.Status != "failed" {
			t.Errorf("the task ended %q; a hardlink across filesystems must fail rather than fall back", task.Status)
		}
		for _, want := range []string{"not on the same filesystem", "copy", "move", "EXDEV"} {
			if !strings.Contains(task.LastError, want) {
				t.Errorf("the failure does not name %q: %q", want, task.LastError)
			}
		}

		// The output step is the one that refused, and it left no half product: the
		// video is either the previous run's or absent, never a partial file.
		detail := p.detail(t, task.ID)
		if len(detail.StepRuns) == 0 {
			t.Fatal("the failed run recorded no steps")
		}
		last := detail.StepRuns[len(detail.StepRuns)-1]
		if last.Type != "output" || last.Status != "failed" {
			t.Errorf("the last step is %s/%s, want output/failed", last.Type, last.Status)
		}
		if !strings.Contains(last.Error, "copy") || !strings.Contains(last.Error, "move") {
			t.Errorf("the step's own error does not name the fix: %q", last.Error)
		}
		dst := filepath.Join(e.secondFS, "library", "CAWD-279", "CAWD-279.mp4")
		if _, err := os.Stat(dst); !os.IsNotExist(err) {
			t.Errorf("%s exists after a failed hardlink (%v); the product must not leave a product it could not link", dst, err)
		}
		ran = append(ran, "③ a hardlink across two real mounts failed the task and the message names `copy|move`")
	})

	t.Run("⑨ the interface renders the views and the needs-number flow goes through", func(t *testing.T) {
		var waiting taskView
		found := false
		for _, task := range counterTasks {
			if task.Status == "needs_number" {
				waiting, found = task, true
				break
			}
		}
		if !found {
			t.Fatal("there is no task waiting for a number, so the panel has nothing to fill")
		}
		detail := p.detail(t, waiting.ID)
		var videos []string
		for _, file := range detail.TaskFiles {
			if file.Kind == "video" {
				videos = append(videos, file.Path)
			}
		}
		if len(videos) != 2 {
			t.Fatalf("the waiting task holds %d videos (%v), want the duplicate stem's two copies", len(videos), videos)
		}
		main, ok := taskByNumber(mainTasks, "CAWD-279")
		if !ok {
			t.Fatal("CAWD-279 is not in the main batch")
		}

		// The numbers the UI types are a number neither file carries, so the submission
		// is a human decision rather than something the parse step could have read.
		const typed = "ABC-123"
		out := e.runInterfaceSmoke(t, map[string]string{
			"MDC_E2E_BASE_URL":            e.Server,
			"MDC_E2E_NUMBER":              main.Number,
			"MDC_E2E_TASK_ID":             strconv.FormatInt(main.ID, 10),
			"MDC_E2E_CORPUS_DIR":          string(RootLibrary),
			"MDC_E2E_WAITING_FILE":        filepath.Base(videos[0]),
			"MDC_E2E_WAITING_SECOND_FILE": filepath.Base(videos[1]),
			"MDC_E2E_WAITING_PATH":        videos[0],
			"MDC_E2E_WAITING_SECOND_PATH": videos[1],
			"MDC_E2E_WAITING_NUMBER":      typed,
		})
		t.Logf("interface smoke: %s", out)

		// The panel's submission went to the API and the task really left the queue: the
		// browser typed a number, and this is the server's own record of it.
		e.waitFor(t, 3*time.Minute, "the task to settle after the interface submitted its numbers", func() (bool, string) {
			tasks := p.tasks(t, counterBatch.BatchID)
			for _, task := range tasks {
				if task.ID != waiting.ID {
					continue
				}
				switch task.Status {
				case "queued", "running", "needs_number":
					return false, "still " + task.Status
				}
				return true, task.Status
			}
			return false, "the waiting task disappeared from its batch"
		})
		after := p.detail(t, waiting.ID)
		for _, file := range after.TaskFiles {
			if file.Kind != "video" {
				continue
			}
			if file.AssignedNumber != typed {
				t.Errorf("%s carries assigned_number %q, want %q: the number the interface typed", filepath.Base(file.Path), file.AssignedNumber, typed)
			}
		}
		ran = append(ran, "⑨ a real browser rendered the scan view, the task list and the detail tabs, and filled the needs-number panel (no axe)")
	})

	t.Run("④ a write killed in the middle leaves no half file, and the restart finishes it", func(t *testing.T) {
		batch := p.createBatch(t, "acceptance-copy", []string{filepath.Join(e.Scratch, string(RootSlow))})
		tasks := p.tasks(t, batch.BatchID)
		if len(tasks) != 1 {
			t.Fatalf("the interrupted-write batch produced %d tasks, want one", len(tasks))
		}
		id := tasks[0].ID
		dir := filepath.Join(e.Scratch, "slow-library", "CAWD-279")
		src := filepath.Join(e.Scratch, string(RootSlow), "CAWD-279", "CAWD-279.mp4")
		dst := filepath.Join(dir, "CAWD-279.mp4")
		tmp := dst + ".tmp"

		// The wait is for the temp file, which exists exactly while the copy is in
		// flight. No sleep decides when the process dies: the write's own state does.
		// The interval is 1 ms — `waitFor`'s 100 ms would step straight over a copy this
		// machine finishes in a fraction of a second.
		waitForFast(t, 2*time.Minute, "the copy's temp file to appear in the output directory", func() bool {
			_, err := os.Stat(tmp)
			return err == nil
		})
		e.killServer(t)

		// The claim: under the product's own name there is either the old file or the
		// complete new one. A half-written `CAWD-279.mp4` is the failure this arm exists
		// for, and the temp file is why it cannot happen.
		if info, err := os.Stat(dst); err == nil {
			if info.Size() == largeCopyBytes {
				t.Fatalf("the copy finished before the process could be killed, so nothing was interrupted "+
					"(%s is already %d bytes); the interrupted-write release has to be larger on this machine",
					filepath.Base(dst), info.Size())
			}
			t.Fatalf("a file of %d bytes appeared under the product's name mid-copy: the write is not atomic",
				info.Size())
		}

		// The restart is the other half: the process comes back over the same database,
		// the interrupted run is set back to queued, and the task writes its product
		// again — through the same temp file and rename, so the recovery leaves no temp.
		e.restartServer(t)
		want := fileSHA256(t, src)
		e.waitFor(t, 3*time.Minute, "the interrupted task to be rerun and its copy to complete", func() (bool, string) {
			info, err := os.Stat(dst)
			if err != nil {
				return false, "the copy is not there yet: " + err.Error()
			}
			if info.Size() != largeCopyBytes {
				return false, fmt.Sprintf("%d of %d bytes", info.Size(), largeCopyBytes)
			}
			return true, ""
		})
		if got := fileSHA256(t, dst); got != want {
			t.Errorf("the recovered copy has sha256 %s, the source's is %s", got, want)
		}
		if _, err := os.Stat(tmp); !os.IsNotExist(err) {
			t.Errorf("the rerun left %s behind (%v); a completed write renames its temp away", filepath.Base(tmp), err)
		}
		settled := e.waitForTaskSettled(t, p, id)
		if settled.Status != "partial" && settled.Status != "succeeded" {
			t.Errorf("the recovered task ended %q (%s)", settled.Status, settled.LastError)
		}
		ran = append(ran, "④ a copy killed mid-write left no file under the product's name, and the restart's rerun completed it")
	})

	t.Run("every image the run fetched answered 200 (a non-200 image is fatal, not a fallback)", func(t *testing.T) {
		// The failure this guards against is a fixture that quietly stops covering an
		// image: the product would treat it as "this source has no cover", the task would
		// go partial, and the run would look green while the fixtures had rotted. The one
		// deliberate exception is the still the cleanup arm deletes on purpose, which the
		// replay records as scripted.
		images, bad := 0, 0
		var problems []string
		for _, req := range e.Replay.Requests() {
			if !isImagePath(req.Path) {
				continue
			}
			images++
			if req.Scripted != "" {
				continue
			}
			if req.Status != http.StatusOK {
				bad++
				problems = append(problems, fmt.Sprintf("%s%s answered %d", req.Host, req.Path, req.Status))
			}
		}
		if images == 0 {
			t.Fatal("the run fetched no images at all, so this arm asserted nothing")
		}
		if bad > 0 {
			t.Errorf("%d of %d image requests did not answer 200: %s", bad, images, strings.Join(problems, "; "))
		}
		ran = append(ran, fmt.Sprintf("every non-scripted image request answered 200 (%d requests)", images))
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

// ---- batch and task helpers ----

// runBatch creates one manual batch over a corpus root and waits for its tasks.
func (e *env) runBatch(t *testing.T, p *product, pipeline string, root CorpusRoot) []taskView {
	t.Helper()
	batch := p.createBatch(t, pipeline, []string{filepath.Join(e.Scratch, string(root))})
	tasks := e.waitForBatchSettled(t, p, batch.BatchID)
	for _, task := range tasks {
		t.Logf("%s batch task %d: number=%q status=%s last_error=%q", root, task.ID, task.Number, task.Status, task.LastError)
	}
	return tasks
}

// rerunAndWait asks for a rerun and waits for the run it produced to report.
//
// The anchor is the step trace's own row ids: the rerun endpoint cannot name the new
// run (the slot that claims the task creates it), and waiting on "the task is terminal"
// would return the verdict of the run that was already finished.
func (e *env) rerunAndWait(t *testing.T, p *product, id int64) taskDetail {
	t.Helper()
	before := p.detail(t, id)
	var anchor int64
	for _, step := range before.StepRuns {
		if step.ID > anchor {
			anchor = step.ID
		}
	}
	p.rerun(t, id)
	var detail taskDetail
	e.waitFor(t, 3*time.Minute, fmt.Sprintf("task %d's rerun to report its steps", id), func() (bool, string) {
		detail = p.detail(t, id)
		switch detail.Status {
		case "queued", "running":
			return false, "still " + detail.Status
		}
		for _, step := range detail.StepRuns {
			if step.ID > anchor {
				return true, ""
			}
		}
		return false, "the latest steps are still the previous run's"
	})
	return detail
}

// waitForTaskSettled waits until one task is out of the queue and running states.
func (e *env) waitForTaskSettled(t *testing.T, p *product, id int64) taskDetail {
	t.Helper()
	var detail taskDetail
	e.waitFor(t, 3*time.Minute, fmt.Sprintf("task %d to settle", id), func() (bool, string) {
		detail = p.detail(t, id)
		switch detail.Status {
		case "queued", "running":
			return false, "still " + detail.Status
		}
		return true, detail.Status
	})
	return detail
}

// detailByNumber reads the detail of the one task that parsed to a number.
func (e *env) detailByNumber(t *testing.T, p *product, tasks []taskView, number string) taskDetail {
	t.Helper()
	task, ok := taskByNumber(tasks, number)
	if !ok {
		t.Fatalf("no task parsed to %s", number)
	}
	return p.detail(t, task.ID)
}

// waitForFast is waitFor for the one thing that is gone in a fraction of a second: a
// temp file written by a copy. `waitFor` polls every 100 ms, which is more than the
// whole copy takes on a fast disk; this one steps every millisecond.
func waitForFast(t *testing.T, timeout time.Duration, what string, probe func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if probe() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("acceptance: waited %s for %s", timeout, what)
		}
		time.Sleep(time.Millisecond)
	}
}

// ---- replay helpers ----

// answered reports whether the replay answered a route with a status, which is how an
// assertion says "the fixture was replayed" rather than "the pipeline thinks so".
func (e *env) answered(t *testing.T, host, path string, status int) bool {
	t.Helper()
	for _, req := range e.Replay.Requests() {
		// The captured host may or may not carry the site's `www.`, and the replay
		// treats them as one site; so does this check.
		if strings.TrimPrefix(req.Host, "www.") != strings.TrimPrefix(host, "www.") || req.Path != path {
			continue
		}
		if req.Status == status {
			return true
		}
	}
	return false
}

// scriptSourceAnswers makes each of the sources-in numbers' answers what the arm
// asserts about. The answers are the network's, the reading of them is the product's:
// a scripted 500 is a real 500 as far as every line of code under test is concerned.
//
// The javbus answers are keyed by number and matched on host+path, because that is
// what javbus asks. The javdb ones match on the query as well: a search request is
// `?q=<number>`, and answering every javdb request with a failure would report the
// wrong thing about the numbers that asked it first.
func (e *env) scriptSourceAnswers(t *testing.T) {
	t.Helper()
	e.Replay.ScriptStatus(t, "javbus-500", route("javbus.com", "/FAIL-500"), http.StatusInternalServerError)
	// Held back past the source's own 1s request timeout (acceptance.yaml), so the
	// product's clock is what decides, not the replay's patience.
	e.Replay.ScriptDelay(t, "javbus-slow", route("javbus.com", "/FAIL-501"), 4*time.Second)
	e.Replay.ScriptStatus(t, "javbus-refused", route("javbus.com", "/FAIL-409"), http.StatusGone)
	e.Replay.ScriptHangUp(t, "javbus-egress", route("javbus.com", "/FAIL-902"))
	e.Replay.ScriptStatus(t, "javdb-503", query(host("jdforrepam.com"), "/api/v2/search", "q=FAIL-503"), http.StatusServiceUnavailable)
	// A 200 that is not the JSON the API answers with: a real javbus page's bytes, so the
	// parser sees a page it cannot read rather than an invented string.
	e.Replay.ScriptBody(t, "javdb-not-json", query(host("jdforrepam.com"), "/api/v2/search", "q=FAIL-200"),
		http.StatusOK, readCapture(t, "javbus", "javbus-CAWD-279.html"))
}

// route matches a request by host and path: the address a source asks for when the
// number is the whole URL.
func route(host, path string) func(h, p, q string) bool {
	return func(h, p, _ string) bool {
		return strings.TrimPrefix(h, "www.") == strings.TrimPrefix(host, "www.") && p == path
	}
}

// query matches a request by host, path and a fragment of its query string, which is
// how javdb's search names the number.
func query(match func(h, p, q string) bool, path, fragment string) func(h, p, q string) bool {
	return func(h, p, q string) bool {
		return match(h, path, q) && strings.Contains(q, fragment)
	}
}

// host matches a request by host alone, whatever its path: a source that asks the same
// host several times keeps one route shape for the test.
func host(name string) func(h, p, q string) bool {
	return func(h, _, _ string) bool {
		return strings.TrimPrefix(h, "www.") == strings.TrimPrefix(name, "www.")
	}
}

// readCapture reads one committed capture's bytes, for a script whose body has to be a
// real page rather than a string this test invented.
func readCapture(t *testing.T, sourceID, name string) []byte {
	t.Helper()
	path := filepath.Join("..", "internal", "source", sourceID, "testdata", "captures", name)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the capture %s: %v", path, err)
	}
	return body
}

// ---- assertion helpers ----

// isImagePath reports whether a request asked for an image. The replay answers images
// by extension, so the same rule is what the image assertion reads.
func isImagePath(path string) bool {
	return imageExts[strings.ToLower(filepath.Ext(path))]
}

// tasksByNumber indexes a batch's tasks by the number they parsed to.
func tasksByNumber(tasks []taskView) map[string]taskView {
	out := make(map[string]taskView, len(tasks))
	for _, task := range tasks {
		out[task.Number] = task
	}
	return out
}

// sourceRunOf finds one source's trace in a task's detail.
func sourceRunOf(detail taskDetail, sourceID string) (sourceRunView, bool) {
	for _, run := range detail.SourceRuns {
		if run.SourceID == sourceID {
			return run, true
		}
	}
	return sourceRunView{}, false
}

// fieldsOf reads the contributed field names off a source_runs row.
func fieldsOf(t *testing.T, run sourceRunView) []string {
	t.Helper()
	if len(run.Fields) == 0 || string(run.Fields) == "null" {
		return nil
	}
	// The column is `{"fields": [...]}` — and `{}` when nothing was contributed,
	// which is what an `ok` row that lost every field and a `notfound` row look like.
	var out struct {
		Fields []string `json:"fields"`
	}
	if err := json.Unmarshal(run.Fields, &out); err != nil {
		t.Fatalf("source_runs.fields of %s is not the expected shape: %v (%s)", run.SourceID, err, run.Fields)
	}
	return out.Fields
}

func dstsOf(rows []fileRowView) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Dst)
	}
	return out
}

// entryNames lists a directory's entry names, sorted, so a failure's message is the
// same on every run.
func entryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Name())
	}
	sort.Strings(out)
	return out
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func mtimeOf(t *testing.T, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.ModTime()
}

// fileSHA256 hashes a file: the recovery arm compares the copy with the corpus file,
// which is the claim "the bytes are the bytes".
func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatalf("hash %s: %v", path, err)
	}
	return hex.EncodeToString(h.Sum(nil))
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
