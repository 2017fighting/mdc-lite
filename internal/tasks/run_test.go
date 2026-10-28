package tasks_test

import (
	"errors"
	"testing"

	"github.com/2017fighting/mdc-lite/internal/tasks"
)

func TestBeginRun_StartsTheNextRunAndKeepsThePaths(t *testing.T) {
	e := openStore(t)
	task := oneTask(t, e)

	run, err := e.st.BeginRun(e.ctx, task.ID)
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if run.No != 1 {
		t.Errorf("run_no = %d, want 1 (it starts at 1)", run.No)
	}
	if run.Status != tasks.RunRunning {
		t.Errorf("run status = %q, want running", run.Status)
	}

	after, err := e.st.Task(e.ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != tasks.StatusRunning {
		t.Errorf("task status = %q, want running", after.Status)
	}
	if !allActive(t, e, task.ID) {
		t.Error("a running task must still hold its paths")
	}
	runs, err := e.st.Runs(e.ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != tasks.RunRunning || runs[0].FinishedAt != nil {
		t.Errorf("runs = %+v, want one open running run", runs)
	}
}

func TestFinishRun_TaskTakesTheRunStatusAndReleasesThePaths(t *testing.T) {
	e := openStore(t)
	task := oneTask(t, e)
	run := begin(t, e, task.ID)

	if err := e.st.FinishRun(e.ctx, run.ID, tasks.Outcome{Status: tasks.RunSucceeded}); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	after, err := e.st.Task(e.ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != tasks.StatusSucceeded {
		t.Errorf("task status = %q, want succeeded (the run's status)", after.Status)
	}
	if after.LastError != "" {
		t.Errorf("last_error = %q, want empty on a success", after.LastError)
	}
	if allActive(t, e, task.ID) {
		t.Error("a terminal task must release every path (active=0)")
	}

	runs, err := e.st.Runs(e.ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if runs[0].Status != tasks.RunSucceeded || runs[0].FinishedAt == nil {
		t.Errorf("run = %+v, want succeeded and finished", runs[0])
	}

	batch, err := e.st.Batch(e.ctx, task.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Succeeded != 1 || batch.Tasks != 1 || batch.Status != tasks.BatchSucceeded {
		t.Errorf("batch = %+v, want succeeded=1 and status succeeded", batch)
	}
	if batch.FinishedAt == nil {
		t.Error("a finished batch must carry finished_at")
	}
}

func TestFinishRun_InterruptedPutsTheTaskBackInTheQueue(t *testing.T) {
	e := openStore(t)
	task := oneTask(t, e)
	run := begin(t, e, task.ID)

	// A run is interrupted when the process dies; the run says so, the task goes
	// back to waiting, and the path stays held so a rescan cannot race in.
	if err := e.st.FinishRun(e.ctx, run.ID, tasks.Outcome{Status: tasks.RunInterrupted}); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	after, err := e.st.Task(e.ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != tasks.StatusQueued {
		t.Errorf("task status = %q, want queued (interrupted belongs to the run only)", after.Status)
	}
	if !allActive(t, e, task.ID) {
		t.Error("a requeued task must keep holding its paths")
	}
	runs, err := e.st.Runs(e.ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if runs[0].Status != tasks.RunInterrupted {
		t.Errorf("run status = %q, want interrupted", runs[0].Status)
	}
	batch, err := e.st.Batch(e.ctx, task.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Status != tasks.BatchRunning {
		t.Errorf("batch status = %q, want running again", batch.Status)
	}
}

func TestFinishRun_NeedsNumberClosesTheBatchButKeepsThePath(t *testing.T) {
	e := openStore(t)
	task := oneTask(t, e)
	run := begin(t, e, task.ID)

	err := e.st.FinishRun(e.ctx, run.ID, tasks.Outcome{Status: tasks.RunNeedsNumber, Reason: tasks.ReasonAmbiguous})
	if err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	after, err := e.st.Task(e.ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != tasks.StatusNeedsNumber || after.LastError != tasks.ReasonAmbiguous {
		t.Errorf("task = %+v, want needs_number with last_error ambiguous", after)
	}
	if !allActive(t, e, task.ID) {
		t.Error("a task waiting for a number must keep holding its paths")
	}
	batch, err := e.st.Batch(e.ctx, task.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Status != tasks.BatchPartial || batch.NeedsNumber != 1 {
		t.Errorf("batch = %+v, want partial with needs_number=1", batch)
	}
	if batch.FinishedAt == nil {
		t.Error("a batch whose only task waits for a human has nothing left to do: it must be closed")
	}
}

func TestFinishRun_RefusesAnOutcomeARunCannotHave(t *testing.T) {
	e := openStore(t)
	task := oneTask(t, e)
	run := begin(t, e, task.ID)

	for name, out := range map[string]tasks.Outcome{
		"still running":                 {Status: tasks.RunRunning},
		"queued is a task":              {Status: tasks.RunStatus(tasks.StatusQueued)},
		"needs_number without a reason": {Status: tasks.RunNeedsNumber},
		"needs_number with a made-up reason": {
			Status: tasks.RunNeedsNumber, Reason: "because",
		},
	} {
		if err := e.st.FinishRun(e.ctx, run.ID, out); !errors.Is(err, tasks.ErrBadOutcome) {
			t.Errorf("%s: err = %v, want ErrBadOutcome", name, err)
		}
	}
	if err := e.st.FinishRun(e.ctx, 9999, tasks.Outcome{Status: tasks.RunSucceeded}); !errors.Is(err, tasks.ErrRunNotFound) {
		t.Errorf("unknown run: err = %v, want ErrRunNotFound", err)
	}
}

func TestBeginRun_RefusesATaskThatCannotRunNow(t *testing.T) {
	e := openStore(t)

	res, err := e.st.CreateBatch(e.ctx, intake(tasks.Group{
		NeedsNumber: tasks.ReasonAmbiguous,
		Files:       []tasks.InputFile{video("/media/in/a/x.mp4", tasks.RolePrimary, 1)},
	}))
	if err != nil {
		t.Fatal(err)
	}
	waiting := e.tasksIn(t, res.BatchID)[0]
	if _, err := e.st.BeginRun(e.ctx, waiting.ID); !errors.Is(err, tasks.ErrNotQueued) {
		t.Errorf("a needs_number task: err = %v, want ErrNotQueued", err)
	}

	runningTask := oneTask(t, e)
	if _, err := e.st.BeginRun(e.ctx, runningTask.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.BeginRun(e.ctx, runningTask.ID); !errors.Is(err, tasks.ErrNotQueued) {
		t.Errorf("a running task: err = %v, want ErrNotQueued (409 task_running)", err)
	}
	if _, err := e.st.BeginRun(e.ctx, 9999); !errors.Is(err, tasks.ErrTaskNotFound) {
		t.Errorf("unknown task: err = %v, want ErrTaskNotFound", err)
	}
}

func TestBeginRun_RerunTakesThePathsBackAndNumbersTheRuns(t *testing.T) {
	e := openStore(t)
	task := oneTask(t, e)
	first := begin(t, e, task.ID)
	if err := e.st.FinishRun(e.ctx, first.ID, tasks.Outcome{Status: tasks.RunFailed, Error: "boom"}); err != nil {
		t.Fatal(err)
	}
	if allActive(t, e, task.ID) {
		t.Fatal("a failed task released its paths")
	}

	second, err := e.st.BeginRun(e.ctx, task.ID)
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if second.No != 2 {
		t.Errorf("run_no = %d, want 2 (history is kept, run_no increments)", second.No)
	}
	if !allActive(t, e, task.ID) {
		t.Error("a rerun must claim the paths again")
	}
	after, err := e.st.Task(e.ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != tasks.StatusRunning || after.LastError != "" {
		t.Errorf("task = %+v, want running with the old error cleared", after)
	}
}

func TestFinishRun_ARunHasExactlyOneEnding(t *testing.T) {
	e := openStore(t)
	task := oneTask(t, e)
	run := begin(t, e, task.ID)
	step, err := e.st.RecordStep(e.ctx, tasks.Step{TaskRunID: run.ID, Idx: 4, Type: "parse", Status: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	row := tasks.FileRow{StepRunID: step, Kind: "nfo", Role: "nfo", Dst: "/media/lib/ABC-123/movie.nfo", Action: "created"}
	if err := e.st.FinishRun(e.ctx, run.ID, tasks.Outcome{Status: tasks.RunSucceeded, Rows: []tasks.FileRow{row}}); err != nil {
		t.Fatal(err)
	}

	// A second finish would write the ledger twice and could pull a finished task
	// back out of its terminal state, so it is refused.
	err = e.st.FinishRun(e.ctx, run.ID, tasks.Outcome{Status: tasks.RunFailed, Error: "late"})
	if !errors.Is(err, tasks.ErrRunFinished) {
		t.Fatalf("err = %v, want ErrRunFinished", err)
	}
	rows, err := e.st.FileRows(e.ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Errorf("ledger rows = %d, want 1 (the second finish wrote nothing)", len(rows))
	}
	after, err := e.st.Task(e.ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != tasks.StatusSucceeded {
		t.Errorf("task status = %q, want succeeded (unchanged)", after.Status)
	}
}

func TestFinishRun_ABatchWhoseTasksWereAllCancelledIsCancelled(t *testing.T) {
	e := openStore(t)
	res, err := e.st.CreateBatch(e.ctx, intake(
		tasks.Group{Number: "AAA-001", Files: []tasks.InputFile{video("/media/in/a/AAA-001.mp4", tasks.RolePrimary, 1)}},
		tasks.Group{Number: "BBB-002", Files: []tasks.InputFile{video("/media/in/a/BBB-002.mp4", tasks.RolePrimary, 1)}},
	))
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range e.tasksIn(t, res.BatchID) {
		run := begin(t, e, task.ID)
		if err := e.st.FinishRun(e.ctx, run.ID, tasks.Outcome{Status: tasks.RunCancelled}); err != nil {
			t.Fatal(err)
		}
	}

	batch, err := e.st.Batch(e.ctx, res.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Status != tasks.BatchCancelled {
		t.Errorf("batch status = %q, want cancelled (nothing failed, everything was called off)", batch.Status)
	}
	if batch.Partial != 0 || batch.Failed != 0 || batch.Succeeded != 0 {
		t.Errorf("batch counters = %+v, want none of succeeded/partial/failed", batch)
	}
}

func TestFinishRun_WritesMetadataAndKeepsItWhenARerunHasNone(t *testing.T) {
	e := openStore(t)
	task := oneTask(t, e)

	run := begin(t, e, task.ID)
	metadata := `{"title":"ABC-123","sources":{"title":"javbus"}}`
	if err := e.st.FinishRun(e.ctx, run.ID, tasks.Outcome{Status: tasks.RunSucceeded, MetadataJSON: metadata}); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	after, err := e.st.Task(e.ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.MetadataJSON != metadata {
		t.Errorf("metadata_json = %s, want the run's", after.MetadataJSON)
	}

	// A failed rerun that collected nothing must not erase what the last good run
	// found: the field values are the task's, not the run's.
	run = begin(t, e, task.ID)
	if err := e.st.FinishRun(e.ctx, run.ID, tasks.Outcome{Status: tasks.RunFailed, Error: "boom"}); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	after, err = e.st.Task(e.ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.MetadataJSON != metadata {
		t.Errorf("metadata_json = %s, want the previous run's", after.MetadataJSON)
	}
}
