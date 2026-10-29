package tasks_test

import (
	"errors"
	"testing"

	"github.com/2017fighting/mdc-lite/internal/tasks"
)

// createTask stores one queued task with the given primary path.
func createTask(t *testing.T, e *env, path string) int64 {
	t.Helper()
	res, err := e.st.CreateBatch(e.ctx, intake(tasks.Group{
		Number: "ABC-123",
		Files: []tasks.InputFile{
			video(path, tasks.RolePrimary, 1),
			companion(path[:len(path)-len(".mp4")]+".srt", 0),
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	return e.tasksIn(t, res.BatchID)[0].ID
}

func TestClaimNext_TakesTheOldestQueuedTaskOutOfTheQueue(t *testing.T) {
	e := openStore(t)
	first := createTask(t, e, "/media/in/a/first.mp4")
	second := createTask(t, e, "/media/in/a/second.mp4")

	task, run, err := e.st.ClaimNext(e.ctx)
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if task.ID != first {
		t.Errorf("claimed task %d, want the oldest (%d)", task.ID, first)
	}
	if run.TaskID != first || run.Status != tasks.RunRunning || run.No != 1 {
		t.Errorf("run = %+v, want the first run of task %d", run, first)
	}
	after, err := e.st.Task(e.ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != tasks.StatusRunning {
		t.Errorf("task status = %q, want running (the claim and the run are one transaction)", after.Status)
	}

	task, _, err = e.st.ClaimNext(e.ctx)
	if err != nil {
		t.Fatalf("second ClaimNext: %v", err)
	}
	if task.ID != second {
		t.Errorf("second claim = %d, want %d", task.ID, second)
	}
	if _, _, err := e.st.ClaimNext(e.ctx); !errors.Is(err, tasks.ErrNoQueuedTask) {
		t.Errorf("empty queue = %v, want ErrNoQueuedTask", err)
	}
}

func TestCancelTask_EndsAQueuedTaskAndReleasesItsPaths(t *testing.T) {
	e := openStore(t)
	id := createTask(t, e, "/media/in/a/cancel.mp4")

	task, err := e.st.CancelTask(e.ctx, id)
	if err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	if task.Status != tasks.StatusCancelled {
		t.Errorf("returned status = %q, want cancelled", task.Status)
	}
	after, err := e.st.Task(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != tasks.StatusCancelled {
		t.Errorf("task status = %q, want cancelled", after.Status)
	}
	if allActive(t, e, id) {
		t.Error("a cancelled task must release every path")
	}
	batch, err := e.st.Batch(e.ctx, after.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Status != tasks.BatchCancelled {
		t.Errorf("batch status = %q, want cancelled when every task was called off", batch.Status)
	}
	// The terminal write is not repeatable: a second cancel has nothing to call off.
	if _, err := e.st.CancelTask(e.ctx, id); !errors.Is(err, tasks.ErrTaskTerminal) {
		t.Errorf("second CancelTask = %v, want ErrTaskTerminal", err)
	}
}

func TestCancelTask_RefusesATaskThatIsRunning(t *testing.T) {
	e := openStore(t)
	task := oneTask(t, e)
	begin(t, e, task.ID)

	if _, err := e.st.CancelTask(e.ctx, task.ID); !errors.Is(err, tasks.ErrTaskRunning) {
		t.Fatalf("CancelTask of a running task = %v, want ErrTaskRunning", err)
	}
	// Nothing changed: a running task keeps its paths and its open run.
	if after, err := e.st.Task(e.ctx, task.ID); err != nil || after.Status != tasks.StatusRunning {
		t.Errorf("task = %+v (%v), want still running", after, err)
	}
	if !allActive(t, e, task.ID) {
		t.Error("a running task must keep its paths")
	}
}

func TestRequeue_PutsAFinishedTaskBackInTheQueueWithItsPaths(t *testing.T) {
	e := openStore(t)
	id := createTask(t, e, "/media/in/a/again.mp4")
	run, err := e.st.BeginRun(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.FinishRun(e.ctx, run.ID, tasks.Outcome{Status: tasks.RunSucceeded}); err != nil {
		t.Fatal(err)
	}
	requeued, err := e.st.Requeue(e.ctx, id)
	if err != nil {
		t.Fatalf("Requeue: %v", err)
	}
	if requeued.Status != tasks.StatusQueued {
		t.Errorf("status = %q, want queued", requeued.Status)
	}
	after, err := e.st.Task(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != tasks.StatusQueued || !allActive(t, e, id) {
		t.Errorf("task = %+v active=%v, want queued and holding its paths", after, allActive(t, e, id))
	}
	batch, err := e.st.Batch(e.ctx, after.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Status != tasks.BatchRunning {
		t.Errorf("batch status = %q, want running again", batch.Status)
	}
}

func TestRequeue_RefusesAFinishedTaskWhosePathWasClaimedAgain(t *testing.T) {
	e := openStore(t)
	path := "/media/in/a/shared.mp4"
	id := createTask(t, e, path)
	run, err := e.st.BeginRun(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.FinishRun(e.ctx, run.ID, tasks.Outcome{Status: tasks.RunSucceeded}); err != nil {
		t.Fatal(err)
	}
	// A later batch takes the path the finished task released.
	createTask(t, e, path)

	if _, err := e.st.Requeue(e.ctx, id); !errors.Is(err, tasks.ErrPathOccupied) {
		t.Fatalf("Requeue = %v, want ErrPathOccupied", err)
	}
}

func TestRequeue_RefusesAQueuedTask(t *testing.T) {
	e := openStore(t)
	id := createTask(t, e, "/media/in/a/queued.mp4")
	if _, err := e.st.Requeue(e.ctx, id); !errors.Is(err, tasks.ErrNotFinished) {
		t.Errorf("Requeue of a queued task = %v, want ErrNotFinished", err)
	}
}
