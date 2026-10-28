package tasks_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/2017fighting/mdc-lite/internal/tasks"
)

func TestRecordStep_WritesOneTracePerStepOfItsOwner(t *testing.T) {
	e := openStore(t)
	res, err := e.st.CreateBatch(e.ctx, intake(tasks.Group{
		Number: "ABC-123",
		Files:  []tasks.InputFile{video("/media/in/a/ABC-123.mp4", tasks.RolePrimary, 1)},
	}))
	if err != nil {
		t.Fatal(err)
	}
	task := e.tasksIn(t, res.BatchID)[0]
	run := begin(t, e, task.ID)

	started := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	batchStep, err := e.st.RecordStep(e.ctx, tasks.Step{
		BatchID: res.BatchID, Idx: 3, Type: "group", Status: "ok",
		StartedAt: started, MS: 12, SummaryJSON: `{"groups":2,"companions":1}`,
	})
	if err != nil {
		t.Fatalf("RecordStep (batch): %v", err)
	}
	runStep, err := e.st.RecordStep(e.ctx, tasks.Step{
		TaskRunID: run.ID, Idx: 4, Type: "parse", Status: "ok", MS: 3,
	})
	if err != nil {
		t.Fatalf("RecordStep (run): %v", err)
	}
	if batchStep == runStep {
		t.Errorf("two step runs share the id %d", batchStep)
	}

	steps, err := e.st.Steps(e.ctx, tasks.Owner{BatchID: res.BatchID})
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 {
		t.Fatalf("batch steps = %d, want 1", len(steps))
	}
	scan := steps[0]
	if scan.Type != "group" || scan.Idx != 3 || scan.MS != 12 || scan.SummaryJSON != `{"groups":2,"companions":1}` {
		t.Errorf("batch step = %+v", scan)
	}
	if !scan.StartedAt.Equal(started) {
		t.Errorf("started_at = %v, want %v", scan.StartedAt, started)
	}
	if scan.TaskRunID != 0 || scan.BatchID != res.BatchID {
		t.Errorf("a prefix step belongs to the batch only: %+v", scan)
	}

	steps, err = e.st.Steps(e.ctx, tasks.Owner{TaskRunID: run.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 {
		t.Fatalf("run steps = %d, want 1", len(steps))
	}
	if steps[0].Type != "parse" || steps[0].BatchID != 0 {
		t.Errorf("a suffix step belongs to the run only: %+v", steps[0])
	}
	// An omitted summary is the empty projection, not a NULL the reader has to handle.
	if steps[0].SummaryJSON != "{}" {
		t.Errorf("summary = %q, want {}", steps[0].SummaryJSON)
	}
}

func TestRecordStep_RefusesATraceItCannotAttributeOrRead(t *testing.T) {
	e := openStore(t)

	if _, err := e.st.RecordStep(e.ctx, tasks.Step{Idx: 1, Type: "scan", Status: "ok"}); !errors.Is(err, tasks.ErrStepOwner) {
		t.Errorf("no owner: err = %v, want ErrStepOwner", err)
	}
	if _, err := e.st.RecordStep(e.ctx, tasks.Step{BatchID: 1, TaskRunID: 1, Idx: 1, Type: "scan", Status: "ok"}); !errors.Is(err, tasks.ErrStepOwner) {
		t.Errorf("both owners: err = %v, want ErrStepOwner", err)
	}
	if _, err := e.st.RecordStep(e.ctx, tasks.Step{BatchID: 1, Idx: 1, Status: "ok"}); err == nil {
		t.Error("a step run without a type must be refused")
	}
	if _, err := e.st.RecordStep(e.ctx, tasks.Step{BatchID: 1, Idx: 1, Type: "scan"}); err == nil {
		t.Error("a step run without a status must be refused")
	}
	_, err := e.st.RecordStep(e.ctx, tasks.Step{BatchID: 1, Idx: 1, Type: "scan", Status: "ok", SummaryJSON: `{"walked":`})
	if err == nil || !strings.Contains(err.Error(), "not JSON") {
		t.Errorf("err = %v, want a refusal naming the summary", err)
	}
}

func TestFinishRun_WritesTheLedgerAndTheStatusInOneTransaction(t *testing.T) {
	e := openStore(t)
	task := oneTask(t, e)
	run := begin(t, e, task.ID)
	step, err := e.st.RecordStep(e.ctx, tasks.Step{TaskRunID: run.ID, Idx: 8, Type: "output", Status: "ok"})
	if err != nil {
		t.Fatal(err)
	}

	cleaned := false
	out := tasks.Outcome{
		Status: tasks.RunSucceeded,
		Rows: []tasks.FileRow{{
			StepRunID: step, Kind: "nfo", Role: "nfo", Dst: "/media/lib/ABC-123/movie.nfo",
			Bytes: 512, SHA256: "deadbeef", Action: "created",
		}},
		BeforeCommit: func(context.Context) error {
			// The cleanup runs while the run is still open, and what it may read is
			// what the previous runs committed.
			cleaned = true
			return nil
		},
	}
	if err := e.st.FinishRun(e.ctx, run.ID, out); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	if !cleaned {
		t.Error("the cleanup hook did not run inside the finish")
	}
	rows, err := e.st.FileRows(e.ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("ledger rows = %d, want 1", len(rows))
	}
	got := rows[0]
	if got.TaskID != task.ID || got.TaskRunID != run.ID || got.StepRunID != step || got.Action != "created" || got.SHA256 != "deadbeef" {
		t.Errorf("ledger row = %+v", got)
	}
}

func TestFinishRun_ARefusedCleanupRollsTheWholeFinishBack(t *testing.T) {
	e := openStore(t)
	task := oneTask(t, e)
	run := begin(t, e, task.ID)
	step, err := e.st.RecordStep(e.ctx, tasks.Step{TaskRunID: run.ID, Idx: 8, Type: "output", Status: "ok"})
	if err != nil {
		t.Fatal(err)
	}

	boom := errors.New("cleanup refused")
	err = e.st.FinishRun(e.ctx, run.ID, tasks.Outcome{
		Status: tasks.RunSucceeded,
		Rows: []tasks.FileRow{{
			StepRunID: step, Kind: "nfo", Role: "nfo", Dst: "/media/lib/ABC-123/movie.nfo", Action: "created",
		}},
		BeforeCommit: func(context.Context) error { return boom },
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the cleanup's error", err)
	}

	rows, err := e.st.FileRows(e.ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("ledger rows = %d, want none (the transaction rolled back)", len(rows))
	}
	after, err := e.st.Task(e.ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != tasks.StatusRunning {
		t.Errorf("task status = %q, want running (the finish was rolled back)", after.Status)
	}
	runs, err := e.st.Runs(e.ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if runs[0].Status != tasks.RunRunning || runs[0].FinishedAt != nil {
		t.Errorf("run = %+v, want still running", runs[0])
	}
	if !allActive(t, e, task.ID) {
		t.Error("a rolled-back finish must not release the paths")
	}
}
