package cli_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/2017fighting/mdc-lite/internal/cli"
	"github.com/2017fighting/mdc-lite/internal/store"
	"github.com/2017fighting/mdc-lite/internal/tasks"
)

// serve starts `mdc serve` on a free port and returns its base URL plus a stop
// function. The address is picked by the OS first, so the test never races another
// listener and never waits on a fixed sleep.
func serve(t *testing.T, args ...string) (string, func()) {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	stdout, stdoutW := io.Pipe()
	stderr, stderrW := io.Pipe()
	var logs strings.Builder
	var mu sync.Mutex
	collect := func(r io.Reader) {
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				mu.Lock()
				logs.Write(buf[:n])
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}
	go collect(stdout)
	go collect(stderr)

	done := make(chan int, 1)
	go func() {
		done <- cli.Run(append([]string{"serve", "--listen", addr}, args...), stdoutW, stderrW)
	}()

	stop := func() {
		if p, ok := http.DefaultClient.Transport.(*http.Transport); ok {
			p.CloseIdleConnections()
		}
	}
	t.Cleanup(stop)

	base := "http://" + addr
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/api/health")
		if err == nil {
			_ = resp.Body.Close()
			return base, func() {
				mu.Lock()
				defer mu.Unlock()
				t.Logf("server log:\n%s", logs.String())
			}
		}
		select {
		case code := <-done:
			mu.Lock()
			t.Fatalf("serve exited with %d before listening:\n%s", code, logs.String())
			mu.Unlock()
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("server never listened on %s:\n%s", addr, logs.String())
	return "", nil
}

func getJSON(t *testing.T, url string, v any) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("GET %s: body %q is not JSON: %v", url, body, err)
	}
	return resp.StatusCode
}

// The health endpoint must answer 503 — not panic — when the database never
// opened. This is the seam where the typed-nil mistake showed up: api.Store held a
// nil *store.Store, so the handler's nil check passed and the first method call
// panicked, leaving the probe with no response at all.
func TestServe_UnopenableDatabaseStillAnswersHealth(t *testing.T) {
	dir := t.TempDir()
	pipelines := filepath.Join(dir, "pipelines")
	if err := os.MkdirAll(pipelines, 0o755); err != nil {
		t.Fatal(err)
	}
	// A directory can never be a SQLite file.
	dbPath := filepath.Join(dir, "a-directory")
	if err := os.Mkdir(dbPath, 0o755); err != nil {
		t.Fatal(err)
	}

	base, logf := serve(t, "--config", filepath.Join(dir, "config.yaml"), "--pipelines-dir", pipelines, "--db-path", dbPath)
	defer logf()

	var body struct {
		Status string `json:"status"`
		DB     struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		} `json:"db"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	code := getJSON(t, base+"/api/health", &body)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", code)
	}
	if body.Status != "unavailable" || body.DB.OK {
		t.Errorf("body = %+v", body)
	}
	if body.Error.Code != "db_unavailable" {
		t.Errorf("error.code = %q, want db_unavailable", body.Error.Code)
	}
}

// A healthy process answers 200 and serves the embedded page.
func TestServe_HealthyAnswers200(t *testing.T) {
	dir := t.TempDir()
	pipelines := filepath.Join(dir, "pipelines")
	if err := os.MkdirAll(pipelines, 0o755); err != nil {
		t.Fatal(err)
	}
	base, logf := serve(t, "--config", filepath.Join(dir, "config.yaml"), "--pipelines-dir", pipelines,
		"--db-path", filepath.Join(dir, "mdc.db"))
	defer logf()

	var body struct {
		Status string `json:"status"`
		DB     struct {
			OK         bool  `json:"ok"`
			Migrations int64 `json:"migrations"`
		} `json:"db"`
	}
	if code := getJSON(t, base+"/api/health", &body); code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if body.DB.Migrations < 1 {
		t.Errorf("migrations = %d", body.DB.Migrations)
	}

	resp, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	page, _ := io.ReadAll(resp.Body)
	if !strings.Contains(strings.ToLower(string(page)), "<title>") {
		t.Errorf("the root did not serve a page: %q", page)
	}
}

// A process that was killed leaves runs marked running and tasks nobody will pick
// up. Starting the server again must reset them before it starts serving work:
// that is the crash recovery the spec puts in one transaction at startup. The
// requeued task then runs on the normal pool, which is the other half of the same
// contract: a recovery that did not lead to work would just be a second graveyard.
func TestServe_StartupResetsTheWorkTheLastProcessLeft(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	pipelines := filepath.Join(dir, "pipelines")
	if err := os.MkdirAll(pipelines, 0o755); err != nil {
		t.Fatal(err)
	}
	media := filepath.Join(dir, "ABC-123.mp4")
	if err := os.WriteFile(media, []byte("a one-second clip is not needed: the run only hardlinks it"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A pipeline whose task-level suffix is `output`: the recovered task needs no
	// network and no title, so the test sees the run finish instead of waiting on a
	// scraper. `required_fields: []` is the spec's own way to make such a task end
	// `succeeded` rather than `partial`.
	pipelineFile := filepath.Join(pipelines, "main.yaml")
	if err := os.WriteFile(pipelineFile, []byte(`steps:
  - type: scan
    with: {roots: ["`+dir+`"]}
  - type: filter
    with:
      rules:
        - name: videos
          when: {ext_in: [".mp4"]}
          then: include
        - name: everything-else
          when: {stem_matches: "^.*$"}
          then: exclude
  - type: group
  - type: parse
    with:
      rules:
        - {builtin: default}
  - type: output
    with:
      root: "`+filepath.Join(dir, "out")+`"
      required_fields: []
`), 0o644); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "mdc.db")

	// The previous process: one batch, one task, one run still marked running.
	raw, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	before := tasks.New(raw, tasks.Config{RetryLimit: 5})
	res, err := before.CreateBatch(ctx, tasks.Intake{
		Trigger:    tasks.TriggerMonitor,
		TriggerRef: dir,
		PipelineID: "main",
		Groups: []tasks.Group{{
			Number: "ABC-123",
			Files: []tasks.InputFile{{
				Path: media, Kind: tasks.KindVideo,
				Role: tasks.RolePrimary, PartIndex: 1,
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var taskID int64
	if err := raw.Write().QueryRowContext(ctx,
		`SELECT id FROM tasks WHERE batch_id = ?`, res.BatchID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := before.BeginRun(ctx, taskID); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	base, logf := serve(t, "--config", filepath.Join(dir, "config.yaml"), "--pipelines-dir", pipelines, "--db-path", dbPath)
	defer logf()

	var health struct {
		Status string `json:"status"`
	}
	if code := getJSON(t, base+"/api/health", &health); code != http.StatusOK {
		t.Fatalf("health = %d, want 200", code)
	}

	after, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = after.Close() }()
	st := tasks.New(after, tasks.Config{RetryLimit: 5})

	// The reset runs before the listener, and the scheduler starts on the requeued
	// task immediately. Poll for the terminal state rather than assuming the
	// goroutine has already finished.
	deadline := time.Now().Add(10 * time.Second)
	var task tasks.Task
	for {
		task, err = st.Task(ctx, taskID)
		if err != nil {
			t.Fatal(err)
		}
		if task.Status.Terminal() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task status = %q, want a terminal state after the reset ran it", task.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if task.Status != tasks.StatusSucceeded {
		t.Errorf("task status = %q, want succeeded (%+v)", task.Status, task)
	}
	runs, err := st.Runs(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs = %+v, want the interrupted attempt and the rerun", runs)
	}
	if runs[0].Status != tasks.RunInterrupted || runs[0].FinishedAt == nil {
		t.Errorf("first run = %+v, want the dead run marked interrupted", runs[0])
	}
	if runs[1].Status != tasks.RunSucceeded || runs[1].FinishedAt == nil {
		t.Errorf("second run = %+v, want the requeued task to have run and succeeded", runs[1])
	}
}

// The endpoints are wired to the in-process service, not to a second copy of the
// rules: `mdc serve` answers the pipeline list and files a batch from the same code
// the CLI uses. This is the wiring test — the contract itself is asserted in
// internal/api.
func TestServe_FilesABatchThroughTheService(t *testing.T) {
	dir := t.TempDir()
	pipelines := filepath.Join(dir, "pipelines")
	if err := os.MkdirAll(pipelines, 0o755); err != nil {
		t.Fatal(err)
	}
	media := filepath.Join(dir, "ABC-123.mp4")
	if err := os.WriteFile(media, []byte("a clip"), 0o644); err != nil {
		t.Fatal(err)
	}
	pipelineFile := filepath.Join(pipelines, "main.yaml")
	if err := os.WriteFile(pipelineFile, []byte(`steps:
  - type: scan
    with: {roots: ["`+dir+`"]}
  - type: filter
    with:
      rules:
        - name: videos
          when: {ext_in: [".mp4"]}
          then: include
        - name: everything-else
          when: {stem_matches: "^.*$"}
          then: exclude
  - type: group
  - type: parse
    with:
      rules:
        - {builtin: default}
  - type: output
    with:
      root: "`+filepath.Join(dir, "out")+`"
      required_fields: []
`), 0o644); err != nil {
		t.Fatal(err)
	}
	base, logf := serve(t, "--config", filepath.Join(dir, "config.yaml"),
		"--pipelines-dir", pipelines, "--db-path", filepath.Join(dir, "mdc.db"))
	defer logf()

	var pipelinesBody struct {
		Items []struct {
			ID    string   `json:"id"`
			Steps []string `json:"steps"`
		} `json:"items"`
	}
	if code := getJSON(t, base+"/api/pipelines", &pipelinesBody); code != http.StatusOK {
		t.Fatalf("GET /api/pipelines: %d", code)
	}
	if len(pipelinesBody.Items) != 1 || pipelinesBody.Items[0].ID != "main" {
		t.Fatalf("pipelines = %+v", pipelinesBody.Items)
	}

	body, err := json.Marshal(map[string]any{"pipeline": "main", "roots": []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(base+"/api/tasks", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /api/tasks: %d, body %s", resp.StatusCode, payload)
	}
	var created struct {
		BatchID int64 `json:"batch_id"`
		Created int   `json:"created"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.BatchID == 0 || created.Created != 1 {
		t.Fatalf("created = %+v", created)
	}

	// The scheduler picks the task up immediately; the list endpoint must show it
	// whatever state it reaches, and it must reach a terminal state without network.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var page struct {
			Total int `json:"total"`
			Items []struct {
				Status string `json:"status"`
			} `json:"items"`
		}
		if code := getJSON(t, base+"/api/tasks", &page); code != http.StatusOK {
			t.Fatalf("GET /api/tasks: %d", code)
		}
		if page.Total == 1 && len(page.Items) == 1 &&
			(page.Items[0].Status == "succeeded" || page.Items[0].Status == "partial") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the task never finished: %+v", page)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
