package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/2017fighting/mdc-lite/internal/config"
	"github.com/2017fighting/mdc-lite/internal/field"
	"github.com/2017fighting/mdc-lite/internal/source"
	"github.com/2017fighting/mdc-lite/internal/store"
	"github.com/2017fighting/mdc-lite/internal/tasks"
)

// runnerEnv is one task run's world: a real SQLite file, a real media directory, a
// real output directory and the fixture source. Nothing here is faked except the
// network, which is the ticket's one allowed double.
type runnerEnv struct {
	ctx   context.Context
	raw   *store.Store
	store *tasks.Store
	run   *Runner
	src   *fakeSource
	media string
	out   string
}

func newRunnerEnv(t *testing.T) *runnerEnv {
	t.Helper()
	ctx := context.Background()
	raw, err := store.Open(ctx, filepath.Join(t.TempDir(), "mdc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	st := tasks.New(raw, tasks.Config{RetryLimit: 5})

	media, src := standardFixture(t)
	out := t.TempDir()
	return &runnerEnv{
		ctx:   ctx,
		raw:   raw,
		store: st,
		run:   NewRunner(st, []config.Pipeline{chainConfig(t, out, "")}, Environment{Sources: source.NewRegistry(src)}),
		src:   src,
		media: media,
		out:   out,
	}
}

// createTask fans one group out the way the real intake does.
func (e *runnerEnv) createTask(t *testing.T, number string, names ...string) int64 {
	t.Helper()
	files := make([]tasks.InputFile, 0, len(names))
	for i, name := range names {
		path := filepath.Join(e.media, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		file := tasks.InputFile{
			Path: path,
			Kind: tasks.KindVideo,
			Role: tasks.RolePart,
			Size: info.Size(),
		}
		if name[strings.LastIndexByte(name, '.')+1:] == "srt" || name[strings.LastIndexByte(name, '.')+1:] == "jpg" {
			file.Kind, file.Role = tasks.KindCompanion, tasks.RoleCompanion
		} else {
			file.PartIndex = i + 1
			if i == 0 {
				file.Role = tasks.RolePrimary
			}
		}
		files = append(files, file)
	}
	res, err := e.store.CreateBatch(e.ctx, tasks.Intake{
		Trigger:    tasks.TriggerManual,
		TriggerRef: e.media,
		PipelineID: "main",
		Groups:     []tasks.Group{{Number: number, Files: files}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return e.taskID(t, res.BatchID)
}

func (e *runnerEnv) taskID(t *testing.T, batchID int64) int64 {
	t.Helper()
	var id int64
	if err := e.raw.Write().QueryRow(`SELECT id FROM tasks WHERE batch_id = ?`, batchID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRunner_OneTaskProducesItsDirectoryAndItsLedger(t *testing.T) {
	e := newRunnerEnv(t)
	taskID := e.createTask(t, "ABC-123", "ABC-123-cd1.mp4", "ABC-123-cd2.mp4", "ABC-123.zh.srt", "ABC-123.jpg")

	outcome, err := e.run.RunTask(e.ctx, taskID)
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if outcome.Status != tasks.RunSucceeded {
		t.Fatalf("status = %q, want succeeded (%+v)", outcome.Status, outcome)
	}

	task, err := e.store.Task(e.ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != tasks.StatusSucceeded {
		t.Errorf("task status = %q", task.Status)
	}
	for _, want := range []string{`"title":"My \u0026 Title"`, `"poster":"fixture (自动裁切)"`, `"poster_crop":true`, `"partial":false`} {
		if !strings.Contains(task.MetadataJSON, want) {
			t.Errorf("metadata_json does not contain %s: %s", want, task.MetadataJSON)
		}
	}

	dir := filepath.Join(e.out, "ABC-123")
	for _, name := range []string{
		"ABC-123-cd1.mp4", "ABC-123-cd2.mp4", "ABC-123-cd1.zh.srt", "ABC-123.jpg",
		"movie.nfo", "poster.jpg", "fanart.jpg", "extrafanart/fanart1.jpg",
	} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name))); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	runs, err := e.store.Runs(e.ctx, taskID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %d, %v", len(runs), err)
	}
	rows, err := e.store.FileRows(e.ctx, runs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	wantRoles := map[string]bool{
		"video:video": true, "image:poster": true, "image:fanart": true,
		"image:extrafanart": true, "nfo:nfo": true, "companion:companion": true,
	}
	got := map[string]int{}
	for _, row := range rows {
		got[row.Kind+":"+row.Role]++
		if row.Action != "created" {
			t.Errorf("first run wrote %s as %q, want created", row.Dst, row.Action)
		}
		if row.SHA256 == "" {
			t.Errorf("%s has no sha256: every ledger column carries a value", row.Dst)
		}
	}
	for key := range wantRoles {
		if got[key] == 0 {
			t.Errorf("the ledger has no %s row: %v", key, got)
		}
	}
	if got["video:video"] != 2 {
		t.Errorf("video rows = %d, want one per part", got["video:video"])
	}
	// Companion rows are their own kind and role: cleanup and idempotency both key
	// off the ledger, so a companion has to be in it.
	var companion *tasks.FileRow
	for i := range rows {
		if rows[i].Role == "companion" {
			companion = &rows[i]
		}
	}
	if companion == nil || filepath.Base(companion.Dst) != "ABC-123-cd1.zh.srt" {
		t.Fatalf("companion row = %+v", companion)
	}

	// Step traces: one row for each suffix step, each with a summary.
	steps, err := e.store.Steps(e.ctx, tasks.Owner{TaskRunID: runs[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, st := range steps {
		types = append(types, st.Type)
		if st.Status != "ok" {
			t.Errorf("step %s = %q", st.Type, st.Status)
		}
		if !json.Valid([]byte(st.SummaryJSON)) {
			t.Errorf("step %s has an invalid summary: %s", st.Type, st.SummaryJSON)
		}
	}
	if strings.Join(types, ",") != "scrape,images,nfo,output" {
		t.Errorf("step runs = %v, want the task-level suffix", types)
	}
	// The index is the step's position in the pipeline file, not its position in the
	// suffix: a rerun that starts at step 5 must say "step 5".
	if steps[0].Idx != 5 {
		t.Errorf("the first suffix step has idx %d, want 5", steps[0].Idx)
	}
	// The images summary is where ADR-0015's crop trace lives.
	var imagesSummary string
	for _, st := range steps {
		if st.Type == "images" {
			imagesSummary = st.SummaryJSON
		}
	}
	if !strings.Contains(imagesSummary, `"cropped":1`) {
		t.Errorf("the images summary must record the crop: %s", imagesSummary)
	}
	if !strings.Contains(imagesSummary, "从横版封套右侧裁切 poster.jpg (2:3)") {
		t.Errorf("the images summary must carry the crop trace: %s", imagesSummary)
	}

	// Source traces: one row, naming what the fixture contributed.
	sources := sourceRows(t, e)
	if len(sources) != 1 || sources[0].SourceID != "fixture" || sources[0].Status != "ok" {
		t.Fatalf("source rows = %+v", sources)
	}
	if !strings.Contains(sources[0].FieldsJSON, "fanart") {
		t.Errorf("fields_json must name the contributed fields: %s", sources[0].FieldsJSON)
	}
}

// sourceRows reads source_runs straight from the write handle: the read side of the
// API belongs to another ticket.
func sourceRows(t *testing.T, e *runnerEnv) []tasks.SourceRun {
	t.Helper()
	rows, err := e.raw.Write().Query(`SELECT source_id, status, coalesce(reason, ''), err, fields_json FROM source_runs`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []tasks.SourceRun
	for rows.Next() {
		var r tasks.SourceRun
		if err := rows.Scan(&r.SourceID, &r.Status, &r.Reason, &r.Err, &r.FieldsJSON); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func TestRunner_RerunSkipsUnchangedAndDeletesWhatIsNoLongerProduced(t *testing.T) {
	e := newRunnerEnv(t)
	// First run: two stills.
	second := source.ImageRef{Role: field.Extrafanarts, Index: 1, URL: "https://fixture/e2.jpg"}
	e.src.fields.Extrafanarts = append(e.src.fields.Extrafanarts, second)
	e.src.images[second.URL] = jpegImage(t, 32, 18)
	taskID := e.createTask(t, "ABC-123", "ABC-123-cd1.mp4", "ABC-123-cd2.mp4", "ABC-123.zh.srt", "ABC-123.jpg")
	if _, err := e.run.RunTask(e.ctx, taskID); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(e.out, "ABC-123", "extrafanart", "fanart2.jpg")
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("the first run should have written fanart2: %v", err)
	}
	// Record every product's mtime so the rerun can prove it left them all alone.
	mtimes := map[string]time.Time{}
	for _, name := range []string{
		"ABC-123-cd1.mp4", "ABC-123-cd2.mp4", "ABC-123-cd1.zh.srt",
		"poster.jpg", "fanart.jpg", "extrafanart/fanart1.jpg", "movie.nfo",
	} {
		info, err := os.Stat(filepath.Join(e.out, "ABC-123", filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		mtimes[name] = info.ModTime()
	}

	// Second run: one still. fanart2 is no longer produced, so cleanup removes it;
	// everything else is unchanged and must be skipped (mtime intact).
	e.src.fields.Extrafanarts = e.src.fields.Extrafanarts[:1]
	outcome, err := e.run.RunTask(e.ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != tasks.RunSucceeded {
		t.Fatalf("status = %q", outcome.Status)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("the still this run no longer produces must be deleted")
	}

	runs, err := e.store.Runs(e.ctx, taskID)
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs = %d, %v", len(runs), err)
	}
	rows, err := e.store.FileRows(e.ctx, runs[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	var deleted, skipped int
	for _, row := range rows {
		switch row.Action {
		case "deleted":
			deleted++
			if row.Dst != stale {
				t.Errorf("deleted %s, want %s", row.Dst, stale)
			}
		case "skipped":
			skipped++
		}
	}
	if deleted != 1 {
		t.Errorf("deleted rows = %d, want 1 (fanart2)", deleted)
	}
	if skipped == 0 {
		t.Error("unchanged files must be recorded as skipped")
	}
	// A rerun must not refresh any mtime: the media server decides what changed by
	// looking at them.
	for _, name := range []string{
		"ABC-123-cd1.mp4", "ABC-123-cd2.mp4", "ABC-123-cd1.zh.srt",
		"poster.jpg", "fanart.jpg", "extrafanart/fanart1.jpg",
	} {
		now, err := os.Stat(filepath.Join(e.out, "ABC-123", filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !now.ModTime().Equal(mtimes[name]) {
			t.Errorf("%s was rewritten: mtime %v → %v", name, mtimes[name], now.ModTime())
		}
	}
	if _, err := os.Stat(filepath.Join(e.out, "ABC-123", "extrafanart", "fanart1.jpg")); err != nil {
		t.Errorf("fanart1 must survive the cleanup: %v", err)
	}
}

func TestRunner_AMissingRequiredFieldFailsTheRun(t *testing.T) {
	e := newRunnerEnv(t)
	e.src.fields.Title = nil
	e.src.fields.Fanart = nil
	taskID := e.createTask(t, "ABC-123", "ABC-123-cd1.mp4", "ABC-123-cd2.mp4", "ABC-123.zh.srt", "ABC-123.jpg")

	outcome, err := e.run.RunTask(e.ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != tasks.RunFailed {
		t.Fatalf("status = %q, want failed", outcome.Status)
	}
	if !strings.Contains(outcome.Error, "missing required field(s)") {
		t.Errorf("the error must name the gate: %q", outcome.Error)
	}
	task, err := e.store.Task(e.ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != tasks.StatusFailed || task.LastError == "" {
		t.Errorf("task = %+v", task)
	}
	// Nothing is written: the plan never came into being.
	if _, err := os.Stat(filepath.Join(e.out, "ABC-123")); !os.IsNotExist(err) {
		t.Error("a failed run must not leave an output directory behind")
	}
}

func TestRunner_AMissingPipelineFailsWithTheId(t *testing.T) {
	raw, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "mdc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	st := tasks.New(raw, tasks.Config{RetryLimit: 5})
	src := newFakeSource()
	media, _ := standardFixture(t)
	env := &runnerEnv{
		ctx:   context.Background(),
		raw:   raw,
		store: st,
		run:   NewRunner(st, nil, Environment{Sources: source.NewRegistry(src)}),
		src:   src,
		media: media,
		out:   t.TempDir(),
	}
	taskID := env.createTask(t, "ABC-123", "ABC-123-cd1.mp4", "ABC-123-cd2.mp4", "ABC-123.zh.srt", "ABC-123.jpg")

	outcome, err := env.run.RunTask(env.ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != tasks.RunFailed || !strings.Contains(outcome.Error, `pipeline "main" no longer exists`) {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestRunner_ReleasesThePathsWhenTheRunEnds(t *testing.T) {
	e := newRunnerEnv(t)
	taskID := e.createTask(t, "ABC-123", "ABC-123-cd1.mp4", "ABC-123-cd2.mp4", "ABC-123.zh.srt", "ABC-123.jpg")
	if _, err := e.run.RunTask(e.ctx, taskID); err != nil {
		t.Fatal(err)
	}
	files, err := e.store.TaskFiles(e.ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Active {
			t.Errorf("%s is still held after a terminal run", f.Path)
		}
	}
}

func TestRunner_CleanupOnlyDeletesWhatTheLedgerRecordedAndTheDirectoryHolds(t *testing.T) {
	e := newRunnerEnv(t)
	taskID := e.createTask(t, "ABC-123", "ABC-123-cd1.mp4", "ABC-123-cd2.mp4", "ABC-123.zh.srt", "ABC-123.jpg")
	if _, err := e.run.RunTask(e.ctx, taskID); err != nil {
		t.Fatal(err)
	}
	runs, err := e.store.Runs(e.ctx, taskID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %d, %v", len(runs), err)
	}
	steps, err := e.store.Steps(e.ctx, tasks.Owner{TaskRunID: runs[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	stepID := steps[len(steps)-1].ID

	// Two rows the run says it wrote: one inside its output directory, one outside.
	// Only the first may be deleted by the next run's cleanup.
	inside := filepath.Join(e.out, "ABC-123", "old-companion.srt")
	outside := filepath.Join(t.TempDir(), "not-mine.srt")
	for _, p := range []string{inside, outside} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{inside, outside} {
		if _, err := e.raw.Write().Exec(
			`INSERT INTO file_rows (task_id, task_run_id, step_run_id, kind, role, idx, src, dst, bytes, sha256, action, created_at)
			 VALUES (?, ?, ?, 'companion', 'companion', 0, '', ?, 1, '', 'created', '2026-01-01T00:00:00.000000000Z')`,
			taskID, runs[0].ID, stepID, p); err != nil {
			t.Fatal(err)
		}
	}

	outcome, err := e.run.RunTask(e.ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != tasks.RunSucceeded {
		t.Fatalf("status = %q", outcome.Status)
	}
	if _, err := os.Stat(inside); !os.IsNotExist(err) {
		t.Error("a ledger row inside the output directory is deleted when it is no longer produced")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("a ledger row outside the output directory must never be touched: %v", err)
	}
	runs, err = e.store.Runs(e.ctx, taskID)
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs = %d, %v", len(runs), err)
	}
	rows, err := e.store.FileRows(e.ctx, runs[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	var deleted []string
	var skippedVideos int
	for _, row := range rows {
		if row.Action == "deleted" {
			deleted = append(deleted, row.Dst)
		}
		if row.Kind == "video" && row.Action == "skipped" {
			skippedVideos++
		}
	}
	if len(deleted) != 1 || deleted[0] != inside {
		t.Errorf("deleted = %v, want only %s", deleted, inside)
	}
	// A hardlink that is already the same inode is skipped, not re-created: the
	// source and the destination are one file.
	if skippedVideos != 2 {
		t.Errorf("skipped video rows = %d, want both parts", skippedVideos)
	}
}

func TestRunner_CleanupRemembersTheLastLedgerThatWroteSomething(t *testing.T) {
	e := newRunnerEnv(t)
	second := source.ImageRef{Role: field.Extrafanarts, Index: 1, URL: "https://fixture/e2.jpg"}
	e.src.fields.Extrafanarts = append(e.src.fields.Extrafanarts, second)
	e.src.images[second.URL] = jpegImage(t, 32, 18)
	taskID := e.createTask(t, "ABC-123", "ABC-123-cd1.mp4", "ABC-123-cd2.mp4", "ABC-123.zh.srt", "ABC-123.jpg")
	if _, err := e.run.RunTask(e.ctx, taskID); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(e.out, "ABC-123", "extrafanart", "fanart2.jpg")

	// A run that dies before it writes anything must not become "the last ledger":
	// the run after it still has to clean up what the first run left.
	e.src.scrapeErr = errors.New("fixture source is down")
	outcome, err := e.run.RunTask(e.ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != tasks.RunFailed {
		t.Fatalf("status = %q, want failed", outcome.Status)
	}

	e.src.scrapeErr = nil
	e.src.fields.Extrafanarts = e.src.fields.Extrafanarts[:1]
	outcome, err = e.run.RunTask(e.ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != tasks.RunSucceeded {
		t.Fatalf("status = %q", outcome.Status)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("the still the last writing run produced must still be cleaned up")
	}
}

func TestRunner_ASingleVideoKeepsItsNameAndItsSubtitle(t *testing.T) {
	// The AC's own corpus: `ABC-123.mp4` + `ABC-123.zh.srt` in one directory. A
	// work in one part has no `-cd`, and the subtitle follows the (unchanged) name.
	media := t.TempDir()
	writeFile(t, filepath.Join(media, "ABC-123.mp4"), 200)
	writeFile(t, filepath.Join(media, "ABC-123.zh.srt"), 10)
	src := newFakeSource()
	title := "Title"
	src.fields.Title = &title
	src.fields.Fanart = &source.ImageRef{Role: field.Fanart, URL: "https://fixture/fanart.jpg"}
	src.images["https://fixture/fanart.jpg"] = jpegImage(t, 160, 90)

	ctx := context.Background()
	raw, err := store.Open(ctx, filepath.Join(t.TempDir(), "mdc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	st := tasks.New(raw, tasks.Config{RetryLimit: 5})
	out := t.TempDir()
	runner := NewRunner(st, []config.Pipeline{chainConfig(t, out, "")}, Environment{Sources: source.NewRegistry(src)})

	info, err := os.Stat(filepath.Join(media, "ABC-123.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := st.CreateBatch(ctx, tasks.Intake{
		Trigger:    tasks.TriggerManual,
		PipelineID: "main",
		Groups: []tasks.Group{{Number: "ABC-123", Files: []tasks.InputFile{
			{Path: filepath.Join(media, "ABC-123.mp4"), Kind: tasks.KindVideo, Role: tasks.RolePrimary, PartIndex: 1, Size: info.Size()},
			{Path: filepath.Join(media, "ABC-123.zh.srt"), Kind: tasks.KindCompanion, Role: tasks.RoleCompanion},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	taskID := e_taskID(t, raw, res.BatchID)
	outcome, err := runner.RunTask(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	// Only title and fanart arrived, so the task is partial — but the files are
	// there, which is what this test is about.
	if outcome.Status != tasks.RunPartial {
		t.Fatalf("status = %q (%+v)", outcome.Status, outcome)
	}
	for _, name := range []string{"ABC-123.mp4", "ABC-123.zh.srt", "movie.nfo"} {
		if _, err := os.Stat(filepath.Join(out, "ABC-123", name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func e_taskID(t *testing.T, raw *store.Store, batchID int64) int64 {
	t.Helper()
	var id int64
	if err := raw.Write().QueryRow(`SELECT id FROM tasks WHERE batch_id = ?`, batchID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRunner_RecordsTheReasonCodeOfAFailedSource(t *testing.T) {
	// The reason code is the difference between "the site has no such work" and
	// "we could not read the site"; it has to survive from the Fetcher, through the
	// scrape step, into `source_runs.reason`.
	e := newRunnerEnv(t)
	e.src.scrapeErr = source.Fail(source.ReasonBlocked, errors.New("javbus: the age gate answered"))
	taskID := e.createTask(t, "ABC-123", "ABC-123-cd1.mp4", "ABC-123-cd2.mp4")

	outcome, err := e.run.RunTask(e.ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != tasks.RunFailed {
		t.Fatalf("status = %q, want failed (the required fields never arrived)", outcome.Status)
	}
	rows := sourceRows(t, e)
	if len(rows) != 1 {
		t.Fatalf("source rows = %+v", rows)
	}
	if rows[0].Status != "failed" || rows[0].Reason != source.ReasonBlocked {
		t.Errorf("source row = %+v, want failed with reason %q", rows[0], source.ReasonBlocked)
	}
	if !strings.Contains(rows[0].Err, "age gate") {
		t.Errorf("source row err = %q, want the failure's own text", rows[0].Err)
	}
}
