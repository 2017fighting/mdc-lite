package tasks_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/2017fighting/mdc-lite/internal/store"
	"github.com/2017fighting/mdc-lite/internal/tasks"
)

// env is one test's database: the real store (real file, real migrations) plus the
// state machine over it.
type env struct {
	raw *store.Store
	st  *tasks.Store
	ctx context.Context
}

// openStore gives each test its own database file. The state machine is about
// rows, so a fake database would be testing itself.
func openStore(t *testing.T) *env {
	t.Helper()
	return openStoreWith(t, 5)
}

// openStoreWith is the same store with a different `retry_limit`.
func openStoreWith(t *testing.T, retryLimit int) *env {
	t.Helper()
	ctx := context.Background()
	raw, err := store.Open(ctx, filepath.Join(t.TempDir(), "mdc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	return &env{raw: raw, st: tasks.New(raw, tasks.Config{RetryLimit: retryLimit}), ctx: ctx}
}

// db exposes the store's write handle for the few reads the task API does not
// carry yet (the batch list query belongs to the REST ticket).
func (e *env) db() *sql.DB { return e.raw.Write() }

func video(path string, role tasks.FileRole, part int) tasks.InputFile {
	return tasks.InputFile{Path: path, Kind: tasks.KindVideo, Role: role, PartIndex: part, Size: 1024}
}

func companion(path string, follows int) tasks.InputFile {
	return tasks.InputFile{Path: path, Kind: tasks.KindCompanion, Role: tasks.RoleCompanion, FollowsPart: follows}
}

func intake(groups ...tasks.Group) tasks.Intake {
	return tasks.Intake{
		Trigger:    tasks.TriggerManual,
		TriggerRef: "/media/in",
		PipelineID: "main",
		Groups:     groups,
	}
}

// tasksIn reads a batch's tasks in the order the fan-out wrote them.
func (e *env) tasksIn(t *testing.T, batchID int64) []tasks.Task {
	t.Helper()
	rows, err := e.db().Query(`SELECT id FROM tasks WHERE batch_id = ? ORDER BY id`, batchID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []tasks.Task
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		one, err := e.st.Task(e.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, one)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// oneTask creates a batch with a single resolved group and returns its task.
func oneTask(t *testing.T, e *env) tasks.Task {
	t.Helper()
	res, err := e.st.CreateBatch(e.ctx, intake(tasks.Group{
		Number: "ABC-123",
		Files: []tasks.InputFile{
			video("/media/in/a/ABC-123.mp4", tasks.RolePrimary, 1),
			companion("/media/in/a/ABC-123.srt", 0),
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	return e.tasksIn(t, res.BatchID)[0]
}

// begin is the scheduler's half of the lifecycle: take the task out of the queue.
func begin(t *testing.T, e *env, taskID int64) tasks.Run {
	t.Helper()
	run, err := e.st.BeginRun(e.ctx, taskID)
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	return run
}

func allActive(t *testing.T, e *env, taskID int64) bool {
	t.Helper()
	files, err := e.st.TaskFiles(e.ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if !f.Active {
			return false
		}
	}
	return true
}
