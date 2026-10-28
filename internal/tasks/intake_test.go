package tasks_test

import (
	"testing"

	"github.com/2017fighting/mdc-lite/internal/tasks"
)

func TestCreateBatch_FansOutOneTaskPerGroupAndCountsThem(t *testing.T) {
	e := openStore(t)

	res, err := e.st.CreateBatch(e.ctx, intake(
		tasks.Group{
			Number: "ABC-123",
			Files: []tasks.InputFile{
				video("/media/in/a/ABC-123.mp4", tasks.RolePrimary, 1),
				video("/media/in/a/ABC-123-cd2.mp4", tasks.RolePart, 2),
				companion("/media/in/a/ABC-123.srt", 0),
			},
		},
		tasks.Group{
			NeedsNumber: tasks.ReasonNoCandidate,
			Files:       []tasks.InputFile{video("/media/in/b/unknown.mp4", tasks.RolePrimary, 1)},
		},
	))
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	if res.Created != 2 {
		t.Errorf("created = %d, want 2", res.Created)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("skipped = %v, want none", res.Skipped)
	}

	b, err := e.st.Batch(e.ctx, res.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	// total counts the paths handed over, tasks counts what became of them.
	if b.Total != 4 || b.Tasks != 2 || b.Skipped != 0 {
		t.Errorf("batch counters = total %d tasks %d skipped %d, want 4/2/0", b.Total, b.Tasks, b.Skipped)
	}
	if b.NeedsNumber != 1 || b.Succeeded != 0 || b.Partial != 0 || b.Failed != 0 {
		t.Errorf("batch status counters = needs_number %d succeeded %d partial %d failed %d",
			b.NeedsNumber, b.Succeeded, b.Partial, b.Failed)
	}
	if b.Status != tasks.BatchRunning {
		t.Errorf("batch status = %q, want running (one task is queued)", b.Status)
	}
	if b.Trigger != tasks.TriggerManual || b.TriggerRef != "/media/in" || b.PipelineID != "main" {
		t.Errorf("batch = %+v", b)
	}

	tasksOfBatch := e.tasksIn(t, res.BatchID)
	if len(tasksOfBatch) != 2 {
		t.Fatalf("tasks in batch = %d, want 2", len(tasksOfBatch))
	}
	queued, waiting := tasksOfBatch[0], tasksOfBatch[1]
	if queued.Status != tasks.StatusQueued || queued.Number != "ABC-123" || queued.LastError != "" {
		t.Errorf("resolved task = %+v", queued)
	}
	if waiting.Status != tasks.StatusNeedsNumber || waiting.Number != "" || waiting.LastError != tasks.ReasonNoCandidate {
		t.Errorf("needs-number task = %+v", waiting)
	}

	files, err := e.st.TaskFiles(e.ctx, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("files = %d, want 3", len(files))
	}
	wantRoles := []tasks.FileRole{tasks.RolePrimary, tasks.RolePart, tasks.RoleCompanion}
	for i, f := range files {
		if f.Role != wantRoles[i] {
			t.Errorf("file %d role = %q, want %q (parts first, companions last)", i, f.Role, wantRoles[i])
		}
		if !f.Active {
			t.Errorf("file %d (%s) must hold its path while the task is queued", i, f.Path)
		}
	}
	if files[1].PartIndex != 2 {
		t.Errorf("second part index = %d, want 2", files[1].PartIndex)
	}
}

func TestCreateBatch_RefusesAnIntakeThatWouldLeaveAnUnreadableTask(t *testing.T) {
	e := openStore(t)

	for name, in := range map[string]tasks.Intake{
		"no groups": intake(),
		"no primary": intake(tasks.Group{
			Number: "ABC-123",
			Files:  []tasks.InputFile{video("/media/in/ABC-123.mp4", tasks.RolePart, 1)},
		}),
		"two primaries": intake(tasks.Group{
			Number: "ABC-123",
			Files: []tasks.InputFile{
				video("/media/in/a.mp4", tasks.RolePrimary, 1),
				video("/media/in/b.mp4", tasks.RolePrimary, 2),
			},
		}),
		"number and reason": intake(tasks.Group{
			Number:      "ABC-123",
			NeedsNumber: tasks.ReasonAmbiguous,
			Files:       []tasks.InputFile{video("/media/in/a.mp4", tasks.RolePrimary, 1)},
		}),
		"unknown reason": intake(tasks.Group{
			NeedsNumber: "because",
			Files:       []tasks.InputFile{video("/media/in/a.mp4", tasks.RolePrimary, 1)},
		}),
		"unknown pipeline-less": {Trigger: tasks.TriggerManual, Groups: intake(tasks.Group{
			Files: []tasks.InputFile{video("/media/in/a.mp4", tasks.RolePrimary, 1)},
		}).Groups},
	} {
		if _, err := e.st.CreateBatch(e.ctx, in); err == nil {
			t.Errorf("%s: CreateBatch must refuse the intake", name)
		}
	}
}
