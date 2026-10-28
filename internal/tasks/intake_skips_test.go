package tasks_test

import (
	"testing"

	"github.com/2017fighting/mdc-lite/internal/tasks"
)

func TestCreateBatch_PathsThatNeverBecameAGroupStillLeaveABatch(t *testing.T) {
	e := openStore(t)
	in := intake()
	in.Skipped = []tasks.SkippedPath{
		{Path: "/media/in/a/too-small.mp4", Reason: tasks.ReasonFiltered},
		{Path: "/media/in/a/gone.mp4", Reason: tasks.ReasonNotFound},
	}

	res, err := e.st.CreateBatch(e.ctx, in)
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	if res.Created != 0 {
		t.Errorf("created = %d, want 0", res.Created)
	}
	if len(res.Skipped) != 2 || res.Skipped[0].Reason != tasks.ReasonFiltered {
		t.Errorf("skipped = %+v, want the two intake paths with their reasons", res.Skipped)
	}

	batch, err := e.st.Batch(e.ctx, res.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Total != 2 || batch.Tasks != 0 || batch.Skipped != 2 {
		t.Errorf("batch = total %d tasks %d skipped %d, want 2/0/2", batch.Total, batch.Tasks, batch.Skipped)
	}
	if batch.Status != tasks.BatchSucceeded || batch.FinishedAt == nil {
		t.Errorf("batch = %q finished %v, want closed as succeeded", batch.Status, batch.FinishedAt)
	}
}

func TestCreateBatch_TheBatchesSkippedListNamesEveryPathThatGotNoTask(t *testing.T) {
	e := openStore(t)
	held := tasks.Group{
		Number: "ABC-123",
		Files: []tasks.InputFile{
			video("/media/in/a/ABC-123.mp4", tasks.RolePrimary, 1),
			companion("/media/in/a/ABC-123.srt", 0),
		},
	}
	if _, err := e.st.CreateBatch(e.ctx, intake(held)); err != nil {
		t.Fatal(err)
	}

	in := intake(held)
	in.Skipped = []tasks.SkippedPath{{Path: "/media/in/a/too-small.mp4", Reason: tasks.ReasonFiltered}}
	res, err := e.st.CreateBatch(e.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 0 {
		t.Fatalf("created = %d, want 0 (the group is still held)", res.Created)
	}
	reasons := map[string]string{}
	for _, s := range res.Skipped {
		reasons[s.Path] = s.Reason
	}
	want := map[string]string{
		"/media/in/a/too-small.mp4": tasks.ReasonFiltered,
		"/media/in/a/ABC-123.mp4":   tasks.ReasonPathOccupied,
		"/media/in/a/ABC-123.srt":   tasks.ReasonPathOccupied,
	}
	if len(reasons) != len(want) {
		t.Fatalf("skipped = %+v, want %+v", reasons, want)
	}
	for path, reason := range want {
		if reasons[path] != reason {
			t.Errorf("%s skipped as %q, want %q", path, reasons[path], reason)
		}
	}

	batch, err := e.st.Batch(e.ctx, res.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	// Three paths went in (the filtered one and the held group's two), none became
	// a task in this batch, all three are skipped.
	if batch.Total != 3 || batch.Tasks != 0 || batch.Skipped != 3 {
		t.Errorf("batch = total %d tasks %d skipped %d, want 3/0/3", batch.Total, batch.Tasks, batch.Skipped)
	}
}

func TestCreateBatch_RefusesASkippedPathTheStoreCannotExplain(t *testing.T) {
	e := openStore(t)
	in := intake()
	in.Skipped = []tasks.SkippedPath{{Path: "/media/in/a/x.mp4", Reason: "because"}}
	if _, err := e.st.CreateBatch(e.ctx, in); err == nil {
		t.Fatal("a skipped path with an unknown reason must be refused")
	}
}
