package pipeline

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/2017fighting/mdc-lite/internal/store"
	"github.com/2017fighting/mdc-lite/internal/tasks"
)

// writeMedia puts files on disk with a size above the filter's floor, so the
// corpus goes through the real scan → filter → parse → group chain.
func writeMedia(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, name := range names {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, bytes.Repeat([]byte("m"), 4096), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// scanAndStore runs the batch-level prefix over dir and stores what came out, the
// way the intake path does: one real scan, one batch, one task per group.
func scanAndStore(t *testing.T, dir string) (*store.Store, *tasks.Store, tasks.BatchResult) {
	t.Helper()
	ctx := context.Background()
	e, err := build(t, testPipeline, Options{Input: Input{Roots: []string{dir}}, Until: "parse"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	res, err := e.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	raw, err := store.Open(ctx, filepath.Join(t.TempDir(), "mdc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	st := tasks.New(raw, tasks.Config{RetryLimit: 5})
	out, err := st.CreateBatch(ctx, ToIntake(tasks.TriggerManual, dir, res))
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	return raw, st, out
}

func TestToIntake_ARealScanStoresOneBatchAndOneTaskPerGroup(t *testing.T) {
	dir := t.TempDir()
	writeMedia(t, dir, "ABC-123.mp4", "ABC-123-cd2.mp4", "ABC-123.srt", "unknown.mp4")

	raw, st, out := scanAndStore(t, dir)
	ctx := context.Background()

	if out.Created != 2 {
		t.Fatalf("created = %d, want 2 tasks (the numbered group and the one with no number)", out.Created)
	}
	batch, err := st.Batch(ctx, out.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	// Four paths went in; two became tasks; one of those is waiting for a human.
	if batch.Total != 4 || batch.Tasks != 2 || batch.Skipped != 0 || batch.NeedsNumber != 1 {
		t.Errorf("batch = total %d tasks %d skipped %d needs_number %d, want 4/2/0/1",
			batch.Total, batch.Tasks, batch.Skipped, batch.NeedsNumber)
	}
	if batch.Trigger != tasks.TriggerManual || batch.TriggerRef != dir || batch.PipelineID != "main" {
		t.Errorf("batch = %+v", batch)
	}

	ids := taskIDs(t, raw, out.BatchID)
	if len(ids) != 2 {
		t.Fatalf("tasks = %d, want 2", len(ids))
	}

	numbered, err := st.Task(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if numbered.Status != tasks.StatusQueued || numbered.Number != "ABC-123" {
		t.Errorf("numbered task = %+v", numbered)
	}
	files, err := st.TaskFiles(ctx, numbered.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("numbered task files = %d, want the two parts and the subtitle", len(files))
	}
	if files[0].Role != tasks.RolePrimary || filepath.Base(files[0].Path) != "ABC-123.mp4" {
		t.Errorf("primary = %+v", files[0])
	}
	if files[1].PartIndex != 2 {
		t.Errorf("second part = %+v, want part_index 2", files[1])
	}
	if files[2].Role != tasks.RoleCompanion || files[2].Kind != tasks.KindCompanion {
		t.Errorf("companion = %+v", files[2])
	}

	waiting, err := st.Task(ctx, ids[1])
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Status != tasks.StatusNeedsNumber || waiting.LastError != tasks.ReasonNoCandidate {
		t.Errorf("waiting task = %+v, want needs_number with no_candidate", waiting)
	}
}

func TestToIntake_ADuplicateStemBecomesATaskThatHoldsBothFiles(t *testing.T) {
	dir := t.TempDir()
	writeMedia(t, dir, "ABC-123.mp4", "ABC-123-U.mp4")

	raw, st, out := scanAndStore(t, dir)
	ctx := context.Background()

	ids := taskIDs(t, raw, out.BatchID)
	if len(ids) != 1 {
		t.Fatalf("tasks = %d, want 1", len(ids))
	}
	task, err := st.Task(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	// A duplicate stem never became a group, so its files arrive as bare paths; the
	// first is the primary and both paths stay held while a human decides.
	if task.Status != tasks.StatusNeedsNumber || task.LastError != tasks.ReasonDuplicateStem {
		t.Errorf("task = %+v, want needs_number with duplicate_stem", task)
	}
	files, err := st.TaskFiles(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("files = %d, want both versions held by the one task", len(files))
	}
	if files[0].Role != tasks.RolePrimary || files[1].Role != tasks.RolePart {
		t.Errorf("roles = %s, %s, want primary then part", files[0].Role, files[1].Role)
	}
	// The names carry no part evidence, so no part order is invented: the human
	// ticks "same work, a part of it" for that.
	for _, f := range files {
		if f.PartIndex != 0 {
			t.Errorf("%s got part_index %d, want 0 until a human decides", f.Path, f.PartIndex)
		}
	}
}

// taskIDs is the one id list the task API does not hand out yet: the batch's task
// query (with its filters and paging) belongs to the REST ticket.
func taskIDs(t *testing.T, raw *store.Store, batchID int64) []int64 {
	t.Helper()
	rows, err := raw.Write().QueryContext(context.Background(),
		`SELECT id FROM tasks WHERE batch_id = ? ORDER BY id`, batchID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestToIntake_AParsedMissIsItsOwnNeedsNumberTask: parse hands a file it could not
// number over as a one-file Unresolved entry, and the intake turns that entry into
// a needs_number task holding exactly that file. Two misses stay two tasks.
func TestToIntake_AParsedMissIsItsOwnNeedsNumberTask(t *testing.T) {
	res := &Result{
		Pipeline: "main",
		State: &State{Parsed: &Parsed{
			Items: []ParsedCandidate{{
				File:   FileRef{Path: "/media/a/ABC-123.mp4", Size: 10},
				Stem:   "ABC-123",
				Number: Number{Raw: "ABC-123", Normalized: "ABC-123", Family: "generic"},
			}},
			Unresolved: []Unresolved{
				{Reason: ReasonNoCandidate, Dir: "/media/a", Stem: "hello", Files: []FileRef{{Path: "/media/a/hello.mp4", Size: 11}}},
				{Reason: ReasonAmbiguous, Dir: "/media/a", Stem: "ABC-123_XYZ-456", Files: []FileRef{{Path: "/media/a/ABC-123_XYZ-456.mp4", Size: 12}}, Candidates: []string{"ABC-123", "XYZ-456"}},
			},
		}},
	}

	in := ToIntake(tasks.TriggerManual, "/media/a", res)
	if len(in.Groups) != 3 {
		t.Fatalf("groups = %+v, want the numbered work and two one-file needs_number groups", in.Groups)
	}
	if in.Groups[0].Number != "ABC-123" || in.Groups[0].NeedsNumber != "" {
		t.Errorf("first group = %+v, want the numbered work", in.Groups[0])
	}
	for i, want := range []struct {
		reason string
		path   string
	}{
		{ReasonNoCandidate, "/media/a/hello.mp4"},
		{ReasonAmbiguous, "/media/a/ABC-123_XYZ-456.mp4"},
	} {
		got := in.Groups[i+1]
		if got.NeedsNumber != want.reason {
			t.Errorf("group %d = %+v, want reason %q", i+1, got, want.reason)
		}
		if len(got.Files) != 1 || got.Files[0].Path != want.path || got.Files[0].Role != tasks.RolePrimary {
			t.Errorf("group %d files = %+v, want the one file as primary", i+1, got.Files)
		}
	}
}
