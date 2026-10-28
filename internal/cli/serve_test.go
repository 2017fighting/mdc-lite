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
// that is the crash recovery the spec puts in one transaction at startup.
func TestServe_StartupResetsTheWorkTheLastProcessLeft(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	pipelines := filepath.Join(dir, "pipelines")
	if err := os.MkdirAll(pipelines, 0o755); err != nil {
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
				Path: filepath.Join(dir, "ABC-123.mp4"), Kind: tasks.KindVideo,
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

	// Recovery runs before the listener does, so by now the row is reset.
	after, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = after.Close() }()
	st := tasks.New(after, tasks.Config{RetryLimit: 5})
	task, err := st.Task(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != tasks.StatusQueued {
		t.Errorf("task status = %q, want queued after the restart reset", task.Status)
	}
	runs, err := st.Runs(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != tasks.RunInterrupted || runs[0].FinishedAt == nil {
		t.Errorf("runs = %+v, want the dead run marked interrupted", runs)
	}
}
