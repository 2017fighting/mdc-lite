package tasks_test

import (
	"errors"
	"testing"

	"github.com/2017fighting/mdc-lite/internal/tasks"
)

func TestCreateBatch_APathHeldByALiveTaskIsSkippedAndCounted(t *testing.T) {
	e := openStore(t)
	in := intake(tasks.Group{
		Number: "ABC-123",
		Files: []tasks.InputFile{
			video("/media/in/a/ABC-123.mp4", tasks.RolePrimary, 1),
			companion("/media/in/a/ABC-123.srt", 0),
		},
	})

	first, err := e.st.CreateBatch(e.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if first.Created != 1 {
		t.Fatalf("first batch created = %d, want 1", first.Created)
	}

	second, err := e.st.CreateBatch(e.ctx, in)
	if err != nil {
		t.Fatalf("a refused path is not an error: %v", err)
	}
	if second.Created != 0 {
		t.Errorf("created = %d, want 0 (both paths are still held)", second.Created)
	}
	if len(second.Skipped) != 2 {
		t.Fatalf("skipped = %v, want both paths", second.Skipped)
	}
	for _, s := range second.Skipped {
		if s.Reason != tasks.ReasonPathOccupied {
			t.Errorf("%s skipped for %q, want %q", s.Path, s.Reason, tasks.ReasonPathOccupied)
		}
	}

	batch, err := e.st.Batch(e.ctx, second.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	// The trigger happened, so the row stays even though nothing ran.
	if batch.Tasks != 0 || batch.Skipped != 2 || batch.Total != 2 {
		t.Errorf("all-skip batch = tasks %d skipped %d total %d, want 0/2/2", batch.Tasks, batch.Skipped, batch.Total)
	}
	if batch.Status != tasks.BatchSucceeded || batch.FinishedAt == nil {
		t.Errorf("all-skip batch = %q finished %v, want closed as succeeded", batch.Status, batch.FinishedAt)
	}
	if n := countTasks(t, e); n != 1 {
		t.Errorf("tasks in the database = %d, want 1 (a refused group creates no task)", n)
	}

	// Reaching a terminal state releases the path, which is what makes the same
	// directory processable again.
	run := begin(t, e, firstTaskID(t, e))
	if err := e.st.FinishRun(e.ctx, run.ID, tasks.Outcome{Status: tasks.RunSucceeded}); err != nil {
		t.Fatal(err)
	}
	third, err := e.st.CreateBatch(e.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if third.Created != 1 {
		t.Errorf("after the first task ended, created = %d, want 1", third.Created)
	}
}

func TestCreateBatch_OnlyTheGroupWhosePathIsHeldStandsDown(t *testing.T) {
	e := openStore(t)
	held := tasks.Group{
		Number: "ABC-123",
		Files:  []tasks.InputFile{video("/media/in/a/ABC-123.mp4", tasks.RolePrimary, 1)},
	}
	free := tasks.Group{
		Number: "DEF-456",
		Files:  []tasks.InputFile{video("/media/in/b/DEF-456.mp4", tasks.RolePrimary, 1)},
	}

	if _, err := e.st.CreateBatch(e.ctx, intake(held)); err != nil {
		t.Fatal(err)
	}
	both, err := e.st.CreateBatch(e.ctx, intake(held, free))
	if err != nil {
		t.Fatal(err)
	}
	if both.Created != 1 {
		t.Errorf("created = %d, want 1 (only the unheld group)", both.Created)
	}
	if len(both.Skipped) != 1 || both.Skipped[0].Path != "/media/in/a/ABC-123.mp4" {
		t.Errorf("skipped = %v, want just the held path", both.Skipped)
	}
	batch, err := e.st.Batch(e.ctx, both.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Tasks != 1 || batch.Skipped != 1 || batch.Total != 2 {
		t.Errorf("batch = tasks %d skipped %d total %d, want 1/1/2", batch.Tasks, batch.Skipped, batch.Total)
	}
}

func TestLookupPaths_SaysWhoHoldsAPathAndWhetherItWasProcessedBefore(t *testing.T) {
	e := openStore(t)
	task := oneTask(t, e)

	// Gate one: a live task holds the path, so the preview says "occupied".
	states, err := e.st.LookupPaths(e.ctx, []string{"/media/in/a/ABC-123.mp4", "/media/in/nothing.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	held, ok := states["/media/in/a/ABC-123.mp4"]
	if !ok || !held.Active || held.TaskID != task.ID || held.Status != tasks.StatusQueued {
		t.Errorf("held path = %+v, want active and owned by task %d", held, task.ID)
	}
	if _, ok := states["/media/in/nothing.mp4"]; ok {
		t.Error("a path no task knows must not appear")
	}

	// Gate two: a finished task does not block the path, it only hints that it can
	// be rerun.
	run := begin(t, e, task.ID)
	if err := e.st.FinishRun(e.ctx, run.ID, tasks.Outcome{Status: tasks.RunSucceeded}); err != nil {
		t.Fatal(err)
	}
	states, err = e.st.LookupPaths(e.ctx, []string{"/media/in/a/ABC-123.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	done := states["/media/in/a/ABC-123.mp4"]
	if done.Active || done.Status != tasks.StatusSucceeded {
		t.Errorf("finished path = %+v, want inactive and succeeded", done)
	}
}

func TestNextQueued_HandsOutTasksInCreationOrderAndSkipsThoseWaitingForANumber(t *testing.T) {
	e := openStore(t)

	firstBatch, err := e.st.CreateBatch(e.ctx, intake(
		tasks.Group{Number: "AAA-001", Files: []tasks.InputFile{video("/media/in/a/AAA-001.mp4", tasks.RolePrimary, 1)}},
		tasks.Group{NeedsNumber: tasks.ReasonAmbiguous, Files: []tasks.InputFile{video("/media/in/a/wait.mp4", tasks.RolePrimary, 1)}},
	))
	if err != nil {
		t.Fatal(err)
	}
	secondBatch, err := e.st.CreateBatch(e.ctx, intake(
		tasks.Group{Number: "BBB-002", Files: []tasks.InputFile{video("/media/in/b/BBB-002.mp4", tasks.RolePrimary, 1)}},
	))
	if err != nil {
		t.Fatal(err)
	}

	order := []string{}
	for {
		task, err := e.st.NextQueued(e.ctx)
		if errors.Is(err, tasks.ErrNoQueuedTask) {
			break
		}
		if err != nil {
			t.Fatalf("NextQueued: %v", err)
		}
		order = append(order, task.Number)
		begin(t, e, task.ID) // taking it out is what removes it from the queue
	}
	if got := order; len(got) != 2 || got[0] != "AAA-001" || got[1] != "BBB-002" {
		t.Errorf("queue order = %v, want [AAA-001 BBB-002] (created_at, then id)", got)
	}

	// The needs_number task is still waiting, and it is not in the queue: a task
	// that is holding a path for a human is not work the scheduler can take.
	waiting := e.tasksIn(t, firstBatch.BatchID)[1]
	if waiting.Status != tasks.StatusNeedsNumber {
		t.Fatalf("waiting task = %+v", waiting)
	}
	if waiting.BatchID != firstBatch.BatchID || secondBatch.Created != 1 {
		t.Errorf("second batch created = %d, want 1", secondBatch.Created)
	}
}

func firstTaskID(t *testing.T, e *env) int64 {
	t.Helper()
	rows, err := e.db().Query(`SELECT id FROM tasks ORDER BY id LIMIT 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var id int64
	if !rows.Next() {
		t.Fatal("no tasks in the database")
	}
	if err := rows.Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func countTasks(t *testing.T, e *env) int {
	t.Helper()
	var n int
	if err := e.db().QueryRow(`SELECT count(*) FROM tasks`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCreateBatch_TwoTriggersCannotClaimTheSamePathAtOnce(t *testing.T) {
	e := openStore(t)
	in := intake(tasks.Group{
		Number: "ABC-123",
		Files:  []tasks.InputFile{video("/media/in/a/ABC-123.mp4", tasks.RolePrimary, 1)},
	})

	type outcome struct {
		res tasks.BatchResult
		err error
	}
	results := make(chan outcome, 2)
	for i := 0; i < 2; i++ {
		go func() {
			res, err := e.st.CreateBatch(e.ctx, in)
			results <- outcome{res: res, err: err}
		}()
	}

	created := 0
	for i := 0; i < 2; i++ {
		got := <-results
		if got.err != nil {
			t.Fatalf("CreateBatch: %v", got.err)
		}
		created += got.res.Created
	}
	// The unique index decides, not timing: one of the two claims the path and the
	// other leaves a batch row saying it was skipped.
	if created != 1 {
		t.Errorf("tasks created = %d, want exactly 1", created)
	}
	states, err := e.st.LookupPaths(e.ctx, []string{"/media/in/a/ABC-123.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	if !states["/media/in/a/ABC-123.mp4"].Active {
		t.Error("the winning task must still hold the path")
	}
	var holders int
	if err := e.db().QueryRow(`SELECT count(*) FROM task_files WHERE path = ? AND active = 1`,
		"/media/in/a/ABC-123.mp4").Scan(&holders); err != nil {
		t.Fatal(err)
	}
	if holders != 1 {
		t.Errorf("active rows for the path = %d, want 1", holders)
	}
}
