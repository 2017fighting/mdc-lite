package tasks_test

import (
	"strconv"
	"testing"
)

// The status sets are a schema fact as much as a state-machine one: each table owns
// only the values it can really hold, and the row cannot say two things at once
// about who it belongs to. These are the constraints the API and the frontend
// count on, so they are asserted against the real database rather than assumed.
func TestSchema_EachTableOwnsOnlyItsOwnStatuses(t *testing.T) {
	e := openStore(t)
	task := oneTask(t, e)
	run := begin(t, e, task.ID)

	// A run is never queued: it exists because the scheduler took the task out of
	// the queue.
	if _, err := e.db().Exec(
		`INSERT INTO task_runs (task_id, run_no, status, started_at) VALUES (?, 2, 'queued', '2026-01-01T00:00:00.000000000Z')`,
		task.ID,
	); err == nil {
		t.Error("a run with status queued must be refused")
	}
	// A task is never interrupted: the process was, not the task.
	if _, err := e.db().Exec(`UPDATE tasks SET status = 'interrupted' WHERE id = ?`, task.ID); err == nil {
		t.Error("a task with status interrupted must be refused")
	}
	if _, err := e.db().Exec(`UPDATE batches SET status = 'queued' WHERE id = ?`, task.BatchID); err == nil {
		t.Error("a batch with status queued must be refused")
	}
	// Every status a table does own is accepted.
	for _, status := range []string{"queued", "running", "succeeded", "partial", "failed", "needs_number", "cancelled"} {
		if _, err := e.db().Exec(`UPDATE tasks SET status = ? WHERE id = ?`, status, task.ID); err != nil {
			t.Errorf("task status %q is refused: %v", status, err)
		}
	}
	for _, status := range []string{"succeeded", "partial", "failed", "needs_number", "interrupted", "cancelled"} {
		if _, err := e.db().Exec(`UPDATE task_runs SET status = ? WHERE id = ?`, status, run.ID); err != nil {
			t.Errorf("run status %q is refused: %v", status, err)
		}
	}
	for _, status := range []string{"running", "succeeded", "partial", "failed", "cancelled"} {
		if _, err := e.db().Exec(`UPDATE batches SET status = ? WHERE id = ?`, status, task.BatchID); err != nil {
			t.Errorf("batch status %q is refused: %v", status, err)
		}
	}

	// A step run belongs to exactly one owner; the row cannot say both or neither.
	for name, stmt := range map[string]string{
		"no owner": `INSERT INTO step_runs (idx, type, status, started_at) VALUES (1, 'scan', 'ok', '2026-01-01T00:00:00.000000000Z')`,
		"both owners": `INSERT INTO step_runs (idx, type, status, started_at, batch_id, task_run_id)
		                VALUES (1, 'scan', 'ok', '2026-01-01T00:00:00.000000000Z', ` +
			strconv.FormatInt(task.BatchID, 10) + `, ` + strconv.FormatInt(run.ID, 10) + `)`,
	} {
		if _, err := e.db().Exec(stmt); err == nil {
			t.Errorf("%s: the step run must be refused", name)
		}
	}
}
