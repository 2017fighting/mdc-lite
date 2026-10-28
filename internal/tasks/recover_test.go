package tasks_test

import (
	"errors"
	"testing"

	"github.com/2017fighting/mdc-lite/internal/tasks"
)

// crash leaves a task the way a killed process does: a run that was still running
// when the lights went out.
func crash(t *testing.T, e *env, taskID int64) tasks.Run {
	t.Helper()
	return begin(t, e, taskID)
}

func TestRecover_InterruptsRunsAndRequeuesTheirTasks(t *testing.T) {
	e := openStore(t)

	crashed := oneTask(t, e)
	crash(t, e, crashed.ID)

	queued, err := e.st.CreateBatch(e.ctx, intake(tasks.Group{
		Number: "DEF-456",
		Files:  []tasks.InputFile{video("/media/in/b/DEF-456.mp4", tasks.RolePrimary, 1)},
	}))
	if err != nil {
		t.Fatal(err)
	}
	waiting := waitingTask(t, e)

	rec, err := e.st.Recover(e.ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if rec.Interrupted != 1 {
		t.Errorf("interrupted runs = %d, want 1", rec.Interrupted)
	}
	if rec.Requeued != 2 {
		t.Errorf("requeued tasks = %d, want 2 (the crashed one and the one never taken)", rec.Requeued)
	}
	if rec.Failed != 0 {
		t.Errorf("failed tasks = %d, want 0", rec.Failed)
	}

	runs, err := e.st.Runs(e.ctx, crashed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if runs[0].Status != tasks.RunInterrupted || runs[0].FinishedAt == nil {
		t.Errorf("run = %+v, want interrupted and finished so it is not retried as open", runs[0])
	}
	after, err := e.st.Task(e.ctx, crashed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != tasks.StatusQueued {
		t.Errorf("crashed task status = %q, want queued", after.Status)
	}
	if !allActive(t, e, crashed.ID) {
		t.Error("a requeued task must keep holding its paths")
	}

	// Its batch is running again, not left looking finished.
	batch, err := e.st.Batch(e.ctx, crashed.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Status != tasks.BatchRunning || batch.FinishedAt != nil {
		t.Errorf("batch = %q finished %v, want running", batch.Status, batch.FinishedAt)
	}

	// The untouched queued task is still queued, and the task that was waiting for a
	// human is still waiting: a crash is not an answer to a question.
	still, err := e.st.Task(e.ctx, e.tasksIn(t, queued.BatchID)[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if still.Status != tasks.StatusQueued {
		t.Errorf("queued task = %q, want queued", still.Status)
	}
	wait, err := e.st.Task(e.ctx, waiting.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wait.Status != tasks.StatusNeedsNumber || wait.LastError != tasks.ReasonAmbiguous {
		t.Errorf("waiting task = %+v, want needs_number with its reason kept", wait)
	}
	if !allActive(t, e, waiting.ID) {
		t.Error("a task waiting for a human must keep holding its paths")
	}
}

func TestRecover_FailsATaskThatUsedUpItsRetries(t *testing.T) {
	e := openStore(t)
	exhausted := oneTask(t, e)
	spare, err := e.st.CreateBatch(e.ctx, intake(tasks.Group{
		Number: "DEF-456",
		Files:  []tasks.InputFile{video("/media/in/b/DEF-456.mp4", tasks.RolePrimary, 1)},
	}))
	if err != nil {
		t.Fatal(err)
	}
	nearly := e.tasksIn(t, spare.BatchID)[0]

	// Each restart costs one run: five crashes is the limit this store was built
	// with, so an interrupted fifth run is the last attempt there is.
	for i := 0; i < 4; i++ {
		run := crash(t, e, exhausted.ID)
		if err := e.st.FinishRun(e.ctx, run.ID, tasks.Outcome{Status: tasks.RunInterrupted}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 4; i++ {
		run := crash(t, e, nearly.ID)
		if err := e.st.FinishRun(e.ctx, run.ID, tasks.Outcome{Status: tasks.RunInterrupted}); err != nil {
			t.Fatal(err)
		}
	}
	// The fifth attempt of the exhausted task is the one the process died in.
	crash(t, e, exhausted.ID)

	rec, err := e.st.Recover(e.ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if rec.Failed != 1 || rec.Requeued != 1 {
		t.Errorf("recovery = %+v, want 1 failed and 1 requeued", rec)
	}

	dead, err := e.st.Task(e.ctx, exhausted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dead.Status != tasks.StatusFailed {
		t.Errorf("exhausted task status = %q, want failed", dead.Status)
	}
	if dead.LastError != tasks.LastErrorRetryLimit {
		t.Errorf("last_error = %q, want %q", dead.LastError, tasks.LastErrorRetryLimit)
	}
	if allActive(t, e, exhausted.ID) {
		t.Error("a failed task must release its paths")
	}

	alive, err := e.st.Task(e.ctx, nearly.ID)
	if err != nil {
		t.Fatal(err)
	}
	if alive.Status != tasks.StatusQueued {
		t.Errorf("task under the limit = %q, want queued (it gets another run)", alive.Status)
	}

	batch, err := e.st.Batch(e.ctx, exhausted.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Status != tasks.BatchFailed || batch.Failed != 1 {
		t.Errorf("batch = %q failed=%d, want failed with failed=1", batch.Status, batch.Failed)
	}
}

func TestRecover_RunningTwiceChangesNothing(t *testing.T) {
	e := openStore(t)
	task := oneTask(t, e)
	crash(t, e, task.ID)

	if _, err := e.st.Recover(e.ctx); err != nil {
		t.Fatal(err)
	}
	rec, err := e.st.Recover(e.ctx)
	if err != nil {
		t.Fatalf("second Recover: %v", err)
	}
	if rec.Interrupted != 0 || rec.Failed != 0 {
		t.Errorf("second recovery = %+v, want nothing left to interrupt", rec)
	}
}

func TestRecover_RecoversNothingOnACleanStart(t *testing.T) {
	e := openStore(t)
	rec, err := e.st.Recover(e.ctx)
	if err != nil {
		t.Fatalf("Recover on a fresh database: %v", err)
	}
	if rec.Interrupted != 0 || rec.Requeued != 0 || rec.Failed != 0 {
		t.Errorf("recovery = %+v, want zero", rec)
	}
	if _, err := e.st.NextQueued(e.ctx); !errors.Is(err, tasks.ErrNoQueuedTask) {
		t.Errorf("queue after a clean start: err = %v, want ErrNoQueuedTask", err)
	}
}

func TestRecover_WithNoRetriesFailsOnlyWorkThatWasInterrupted(t *testing.T) {
	e := openStoreWith(t, 0)
	crashed := oneTask(t, e)
	crash(t, e, crashed.ID)

	// A task nobody has started yet has used no attempt, so a limit of zero must
	// not fail it: it is not retried out of anything.
	never, err := e.st.CreateBatch(e.ctx, intake(tasks.Group{
		Number: "DEF-456",
		Files:  []tasks.InputFile{video("/media/in/b/DEF-456.mp4", tasks.RolePrimary, 1)},
	}))
	if err != nil {
		t.Fatal(err)
	}
	fresh := e.tasksIn(t, never.BatchID)[0]

	rec, err := e.st.Recover(e.ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if rec.Failed != 1 || rec.Requeued != 1 {
		t.Errorf("recovery = %+v, want 1 failed (the interrupted run) and 1 requeued", rec)
	}
	dead, err := e.st.Task(e.ctx, crashed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dead.Status != tasks.StatusFailed || dead.LastError != tasks.LastErrorRetryLimit {
		t.Errorf("crashed task = %+v, want failed at the retry limit", dead)
	}
	alive, err := e.st.Task(e.ctx, fresh.ID)
	if err != nil {
		t.Fatal(err)
	}
	if alive.Status != tasks.StatusQueued {
		t.Errorf("never-started task = %q, want queued", alive.Status)
	}
}
