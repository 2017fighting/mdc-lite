package tasks_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/2017fighting/mdc-lite/internal/tasks"
)

// waitingTask creates the batch a human has to settle: parse read no number
// because the file name offers two.
func waitingTask(t *testing.T, e *env) tasks.Task {
	t.Helper()
	res, err := e.st.CreateBatch(e.ctx, intake(tasks.Group{
		NeedsNumber: tasks.ReasonAmbiguous,
		Files: []tasks.InputFile{
			video("/media/in/a/unknown.mp4", tasks.RolePrimary, 1),
			video("/media/in/a/unknown-cd2.mp4", tasks.RolePart, 2),
			companion("/media/in/a/unknown.srt", 0),
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	return e.tasksIn(t, res.BatchID)[0]
}

func TestAssignNumbers_PutsTheTaskBackInTheQueueWithThePrimarysNumber(t *testing.T) {
	e := openStore(t)
	task := waitingTask(t, e)

	err := e.st.AssignNumbers(e.ctx, task.ID, []tasks.Fill{
		{Path: "/media/in/a/unknown.mp4", Number: "ABC-123"},
		{Path: "/media/in/a/unknown-cd2.mp4", Number: "ABC-123", IsPart: true},
	})
	if err != nil {
		t.Fatalf("AssignNumbers: %v", err)
	}

	after, err := e.st.Task(e.ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != tasks.StatusQueued {
		t.Errorf("task status = %q, want queued (the scheduler picks it up from parse)", after.Status)
	}
	if after.Number != "ABC-123" {
		t.Errorf("task number = %q, want the primary's", after.Number)
	}
	if after.LastError != "" {
		t.Errorf("last_error = %q, want the reason cleared once a human answered", after.LastError)
	}
	if !allActive(t, e, task.ID) {
		t.Error("a task that is back in the queue must still hold its paths")
	}

	files, err := e.st.TaskFiles(e.ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("files = %d, want 3", len(files))
	}
	if files[0].AssignedNumber != "ABC-123" || files[0].IsPart {
		t.Errorf("primary = %+v", files[0])
	}
	if files[1].AssignedNumber != "ABC-123" || !files[1].IsPart {
		t.Errorf("part = %+v, want the tick remembered", files[1])
	}
	if files[2].AssignedNumber != "" {
		t.Errorf("companion = %+v, want no number", files[2])
	}

	batch, err := e.st.Batch(e.ctx, task.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Status != tasks.BatchRunning || batch.NeedsNumber != 0 {
		t.Errorf("batch = %+v, want running again with needs_number=0", batch)
	}
}

func TestAssignNumbers_RefusesAPartialSubmitAndWritesNothing(t *testing.T) {
	e := openStore(t)
	task := waitingTask(t, e)

	err := e.st.AssignNumbers(e.ctx, task.ID, []tasks.Fill{
		{Path: "/media/in/a/unknown.mp4", Number: "ABC-123"},
	})
	var incomplete *tasks.IncompleteNumbersError
	if !errors.As(err, &incomplete) {
		t.Fatalf("err = %v, want IncompleteNumbersError (422 numbers_incomplete)", err)
	}
	if want := []string{"/media/in/a/unknown-cd2.mp4"}; !slices.Equal(incomplete.Missing, want) {
		t.Errorf("missing = %v, want %v (a companion has no number to type)", incomplete.Missing, want)
	}

	after, err := e.st.Task(e.ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != tasks.StatusNeedsNumber || after.Number != "" {
		t.Errorf("task = %+v, want still waiting (a partial submit stores nothing)", after)
	}
	files, err := e.st.TaskFiles(e.ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.AssignedNumber != "" {
			t.Errorf("%s kept %q after a refused submit", f.Path, f.AssignedNumber)
		}
	}
}

func TestAssignNumbers_RefusesAPathThatIsNotTheTasks(t *testing.T) {
	e := openStore(t)
	task := waitingTask(t, e)

	err := e.st.AssignNumbers(e.ctx, task.ID, []tasks.Fill{
		{Path: "/media/in/a/unknown.mp4", Number: "ABC-123"},
		{Path: "/media/in/a/unknown-cd2.mp4", Number: "ABC-123"},
		{Path: "/media/in/elsewhere.mp4", Number: "XYZ-001"},
	})
	if !errors.Is(err, tasks.ErrUnknownPath) {
		t.Errorf("err = %v, want ErrUnknownPath", err)
	}
}

func TestAssignNumbers_RefusesATaskThatIsNotWaitingForOne(t *testing.T) {
	e := openStore(t)
	task := oneTask(t, e)

	err := e.st.AssignNumbers(e.ctx, task.ID, []tasks.Fill{{Path: "/media/in/a/ABC-123.mp4", Number: "ABC-123"}})
	if !errors.Is(err, tasks.ErrNotWaitingForNumber) {
		t.Errorf("queued task: err = %v, want ErrNotWaitingForNumber", err)
	}
	if err := e.st.AssignNumbers(e.ctx, 9999, nil); !errors.Is(err, tasks.ErrTaskNotFound) {
		t.Errorf("unknown task: err = %v, want ErrTaskNotFound", err)
	}
}
