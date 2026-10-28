package tasks_test

import (
	"testing"

	"github.com/2017fighting/mdc-lite/internal/tasks"
)

func TestFinishRun_BatchIsAProjectionOfItsTasks(t *testing.T) {
	e := openStore(t)

	res, err := e.st.CreateBatch(e.ctx, intake(
		tasks.Group{Number: "AAA-001", Files: []tasks.InputFile{video("/media/in/a/AAA-001.mp4", tasks.RolePrimary, 1)}},
		tasks.Group{Number: "BBB-002", Files: []tasks.InputFile{video("/media/in/a/BBB-002.mp4", tasks.RolePrimary, 1)}},
		tasks.Group{Number: "CCC-003", Files: []tasks.InputFile{video("/media/in/a/CCC-003.mp4", tasks.RolePrimary, 1)}},
		tasks.Group{Number: "DDD-004", Files: []tasks.InputFile{video("/media/in/a/DDD-004.mp4", tasks.RolePrimary, 1)}},
	))
	if err != nil {
		t.Fatal(err)
	}
	all := e.tasksIn(t, res.BatchID)
	if len(all) != 4 {
		t.Fatalf("tasks = %d, want 4", len(all))
	}

	// Mixed outcomes: the batch is running until nothing is pending, then the worst
	// outcome wins — failed beats partial beats succeeded.
	outcomes := map[int]tasks.Outcome{
		0: {Status: tasks.RunSucceeded},
		1: {Status: tasks.RunPartial},
		2: {Status: tasks.RunFailed, Error: "boom"},
		3: {Status: tasks.RunCancelled},
	}
	wantAfter := []tasks.BatchStatus{
		tasks.BatchRunning, // three still to go
		tasks.BatchRunning,
		tasks.BatchRunning,
		tasks.BatchFailed, // everything is in
	}
	for i, one := range all {
		run := begin(t, e, one.ID)
		if err := e.st.FinishRun(e.ctx, run.ID, outcomes[i]); err != nil {
			t.Fatalf("FinishRun %d: %v", i, err)
		}
		batch, err := e.st.Batch(e.ctx, res.BatchID)
		if err != nil {
			t.Fatal(err)
		}
		if batch.Status != wantAfter[i] {
			t.Errorf("after task %d the batch is %q, want %q", i+1, batch.Status, wantAfter[i])
		}
	}

	batch, err := e.st.Batch(e.ctx, res.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Tasks != 4 || batch.Succeeded != 1 || batch.Partial != 1 || batch.Failed != 1 || batch.NeedsNumber != 0 {
		t.Errorf("counters = tasks %d succeeded %d partial %d failed %d needs_number %d, want 4/1/1/1/0",
			batch.Tasks, batch.Succeeded, batch.Partial, batch.Failed, batch.NeedsNumber)
	}
	if batch.FinishedAt == nil {
		t.Error("a batch whose tasks are all finished must carry finished_at")
	}
}
