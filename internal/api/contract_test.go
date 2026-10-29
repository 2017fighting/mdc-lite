package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/2017fighting/mdc-lite/internal/api"
	"github.com/2017fighting/mdc-lite/internal/config"
	"github.com/2017fighting/mdc-lite/internal/meta"
	"github.com/2017fighting/mdc-lite/internal/service"
	"github.com/2017fighting/mdc-lite/internal/store"
	"github.com/2017fighting/mdc-lite/internal/tasks"
)

// The REST contract is tested at the service seam: a real SQLite file, a real
// directory tree, the real handlers through httptest — and no binary to start. Only
// the clock and the network are absent, and V1's endpoints need neither.
//
// Every assertion is about what a client can observe: the status code, the JSON
// shape, the machine code in an error, and (for the image endpoints) the bytes.

// fixture is one test's server: a temp database, a media tree, and the handlers.
type fixture struct {
	t    *testing.T
	svc  *service.Service
	raw  *store.Store
	st   *tasks.Store
	http http.Handler
	// media is the browsable root; out is where a task's products land.
	media, out string
}

// pipelineYAML is the test pipeline. It is the whole chain because a pipeline
// without an `output` step does not validate, but only the prefix through `parse`
// ever runs here: a preview and a manual trigger both stop at parse.
func pipelineYAML(outDir string) string {
	return `name: main
steps:
  - type: scan
    with:
      roots: ["/media"]
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
    with: {}
  - type: parse
    with:
      rules:
        - {builtin: default}
  - type: scrape
    with: {}
  - type: images
    with: {}
  - type: nfo
    with:
      template: default
  - type: output
    with:
      root: "` + outDir + `"
`
}

// newFixture builds the server and the media tree. The corpus is chosen for its
// outcomes: one resolved number, one ambiguous group with two parts, one group with
// no candidate, and one file the filter turns away.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureWith(t, nil)
}

// newFixtureWith is newFixture with a say over the settings: the picker's mount
// labels come from `browse_roots`, so a test of the Deck needs to set them.
func newFixtureWith(t *testing.T, mutate func(*config.Settings)) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{t: t, media: filepath.Join(dir, "media"), out: filepath.Join(dir, "out")}
	writeFile(t, filepath.Join(f.media, "ABC-123.mp4"), []byte("video one"))
	writeFile(t, filepath.Join(f.media, "ABP-123-ABP-124-cd1.mp4"), []byte("video two, part one"))
	writeFile(t, filepath.Join(f.media, "ABP-123-ABP-124-cd2.mp4"), []byte("video two, part two"))
	writeFile(t, filepath.Join(f.media, "nothing here.mp4"), []byte("video three"))
	writeFile(t, filepath.Join(f.media, "notes.txt"), []byte("not a video"))

	pipelinesDir := filepath.Join(dir, "pipelines")
	if err := os.MkdirAll(pipelinesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pipelineFile := filepath.Join(pipelinesDir, "main.yaml")
	writeFile(t, pipelineFile, []byte(pipelineYAML(f.out)))
	pipeline, err := config.LoadPipelineFile(pipelineFile, config.DefaultStepRegistry(), nil)
	if err != nil {
		t.Fatalf("the test pipeline is not valid: %v", err)
	}

	ctx := context.Background()
	raw, err := store.Open(ctx, filepath.Join(dir, "mdc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	settings := config.Defaults()
	settings.PipelinesDir = pipelinesDir
	settings.DBPath = raw.Path()
	settings.BrowseRoots = []config.BrowseRoot{{Path: f.media, Label: "/media"}}
	if mutate != nil {
		mutate(&settings)
	}

	f.raw = raw
	f.st = tasks.New(raw, tasks.Config{RetryLimit: settings.RetryLimit})
	f.svc, err = service.New(service.Config{
		Settings:  settings,
		Pipelines: []config.Pipeline{pipeline},
		DB:        raw,
		Tasks:     f.st,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.http = api.NewWithService("test", f.svc, nil).Handler()
	return f
}

// writeFile creates one file with its parent directories.
func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// do performs one request. A body that is not nil is sent as JSON with the matching
// content type.
func (f *fixture) do(method, path string, body any) (*httptest.ResponseRecorder, []byte) {
	f.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			f.t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	f.http.ServeHTTP(rec, req)
	return rec, rec.Body.Bytes()
}

// doJSON performs one request and decodes a 2xx body into v.
func (f *fixture) doJSON(method, path string, body, v any) {
	f.t.Helper()
	rec, raw := f.do(method, path, body)
	if rec.Code < 200 || rec.Code >= 300 {
		f.t.Fatalf("%s %s: status %d, body %s", method, path, rec.Code, raw)
	}
	if v != nil {
		if err := json.Unmarshal(raw, v); err != nil {
			f.t.Fatalf("%s %s: body %q is not JSON: %v", method, path, raw, err)
		}
	}
}

// errorEnvelope is the shape every failure uses.
type errorEnvelope struct {
	Error struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Detail  json.RawMessage `json:"detail"`
	} `json:"error"`
}

// wantError asserts the status and machine code of one failure.
func (f *fixture) wantError(method, path string, body any, status int, code string) errorEnvelope {
	f.t.Helper()
	rec, raw := f.do(method, path, body)
	if rec.Code != status {
		f.t.Fatalf("%s %s: status %d, want %d (body %s)", method, path, rec.Code, status, raw)
	}
	var env errorEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		f.t.Fatalf("%s %s: body %q is not JSON: %v", method, path, raw, err)
	}
	if env.Error.Code != code {
		f.t.Fatalf("%s %s: error.code = %q, want %q (body %s)", method, path, env.Error.Code, code, raw)
	}
	if env.Error.Message == "" {
		f.t.Errorf("%s %s: the error has no message", method, path)
	}
	return env
}

// createBatch creates the standard batch and returns its result.
func (f *fixture) createBatch() service.CreateBatchResult {
	f.t.Helper()
	var res service.CreateBatchResult
	f.doJSON(http.MethodPost, "/api/tasks", service.CreateBatchRequest{
		Pipeline: "main",
		Roots:    []string{f.media},
	}, &res)
	if res.BatchID == 0 {
		f.t.Fatalf("no batch id in %+v", res)
	}
	return res
}

// listTasks reads one page of tasks.
func (f *fixture) listTasks(query string) service.Page[service.TaskView] {
	f.t.Helper()
	var page service.Page[service.TaskView]
	f.doJSON(http.MethodGet, "/api/tasks"+query, nil, &page)
	return page
}

// waitPreview polls a preview until it leaves `running`. The deadline is generous
// and the loop never sleeps on a fixed count: a slow machine just gets more polls.
func (f *fixture) waitPreview(id string) service.PreviewResult {
	f.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var res service.PreviewResult
		f.doJSON(http.MethodGet, "/api/scan/previews/"+id, nil, &res)
		if res.State != service.PreviewRunning {
			return res
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("preview %s never finished", id)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPipelines_ListsTheStepTypeSequence(t *testing.T) {
	f := newFixture(t)
	var page service.Page[service.PipelineView]
	f.doJSON(http.MethodGet, "/api/pipelines", nil, &page)
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("page = %+v", page)
	}
	got := strings.Join(page.Items[0].Steps, ",")
	if got != "scan,filter,group,parse,scrape,images,nfo,output" {
		t.Errorf("steps = %q", got)
	}
	if page.Items[0].ID != "main" {
		t.Errorf("id = %q", page.Items[0].ID)
	}
}

func TestDirs_ListsOnlyDirectoriesInsideTheRoots(t *testing.T) {
	f := newFixture(t)
	if err := os.MkdirAll(filepath.Join(f.media, "2024", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(f.media, "2024", "a.mp4"), []byte("x"))
	writeFile(t, filepath.Join(f.media, "2024", "b.mkv"), []byte("x"))
	writeFile(t, filepath.Join(f.media, "2024", "c.txt"), []byte("x"))

	var res service.BrowseResult
	f.doJSON(http.MethodGet, "/api/fs/dirs?path="+f.media, nil, &res)
	if len(res.Dirs) != 1 || res.Dirs[0].Name != "2024" {
		t.Fatalf("dirs = %+v (files must not be listed)", res.Dirs)
	}
	if res.Dirs[0].Entries != nil {
		t.Errorf("statistics must be absent without stats=1: %+v", res.Dirs[0])
	}
	if res.Dirs[0].Path != filepath.Join(f.media, "2024") {
		t.Errorf("path = %q", res.Dirs[0].Path)
	}

	f.doJSON(http.MethodGet, "/api/fs/dirs?path="+f.media+"&stats=1", nil, &res)
	if len(res.Dirs) != 1 || res.Dirs[0].Entries == nil || res.Dirs[0].Videos == nil {
		t.Fatalf("stats=1 must add the two counts: %+v", res.Dirs)
	}
	if *res.Dirs[0].Entries != 4 || *res.Dirs[0].Videos != 2 {
		t.Errorf("entries/videos = %d/%d, want 4/2", *res.Dirs[0].Entries, *res.Dirs[0].Videos)
	}
}

// The Deck draws the mount label on the path and one entry per browse root, so the
// picker's answer has to carry both. It is additive: the pre-existing fields are
// unchanged, which is why the endpoint's contract stays `path` + `stats`.
func TestDirs_ReportsEveryRootAndTheMountLabelOfTheCurrentPath(t *testing.T) {
	media := filepath.Join(t.TempDir(), "media")
	other := filepath.Join(t.TempDir(), "second")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	f := newFixtureWith(t, func(s *config.Settings) {
		s.BrowseRoots = []config.BrowseRoot{
			{Path: media, Label: "媒体库"},
			{Path: other, Label: "VR 专区"},
		}
	})
	sub := filepath.Join(media, "2024")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	var res service.BrowseResult
	f.doJSON(http.MethodGet, "/api/fs/dirs?path="+sub, nil, &res)
	if res.Path != sub {
		t.Errorf("path = %q", res.Path)
	}
	if res.Label != "媒体库" {
		t.Errorf("label = %q, want the mount label of the containing root", res.Label)
	}
	if len(res.Roots) != 2 {
		t.Fatalf("roots = %+v, want both browse_roots", res.Roots)
	}
	if res.Roots[0].Path != media || res.Roots[0].Label != "媒体库" {
		t.Errorf("roots[0] = %+v", res.Roots[0])
	}
	if res.Roots[1].Path != other || res.Roots[1].Label != "VR 专区" {
		t.Errorf("roots[1] = %+v", res.Roots[1])
	}
}

func TestDirs_OutOfRootPathIs400RootNotBrowsable(t *testing.T) {
	f := newFixture(t)
	outside := t.TempDir()
	f.wantError(http.MethodGet, "/api/fs/dirs?path="+outside, nil, http.StatusBadRequest, "root_not_browsable")
	f.wantError(http.MethodGet, "/api/fs/dirs?path="+filepath.Join(f.media, "nope"), nil, http.StatusNotFound, "not_found")
}

// Without a path the picker answers the whitelist itself: the Deck's entry points
// have to come from somewhere, and it must not list a directory nobody named.
func TestDirs_WithoutAPathAnswersTheEntryPointsOnly(t *testing.T) {
	f := newFixture(t)
	var res service.BrowseResult
	f.doJSON(http.MethodGet, "/api/fs/dirs", nil, &res)
	if res.Path != "" || len(res.Dirs) != 0 {
		t.Errorf("bootstrap listed something: path=%q dirs=%+v", res.Path, res.Dirs)
	}
	if len(res.Roots) != 1 || res.Roots[0].Path != f.media {
		t.Errorf("roots = %+v, want the one browse root", res.Roots)
	}
}

func TestPreview_Is202ThenPollableWithTheTableShape(t *testing.T) {
	f := newFixture(t)
	rec, raw := f.do(http.MethodPost, "/api/scan/preview", service.PreviewRequest{
		Pipeline: "main",
		Roots:    []string{f.media},
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", rec.Code, raw)
	}
	var started struct {
		PreviewID string `json:"preview_id"`
	}
	if err := json.Unmarshal(raw, &started); err != nil || started.PreviewID == "" {
		t.Fatalf("body %s: %v", raw, err)
	}
	if loc := rec.Header().Get("Location"); loc != "/api/scan/previews/"+started.PreviewID {
		t.Errorf("Location = %q", loc)
	}

	res := f.waitPreview(started.PreviewID)
	if res.State != service.PreviewSucceeded {
		t.Fatalf("state = %q (error %+v)", res.State, res.Error)
	}
	// Two videos and notes.txt are walked; the filter keeps the four videos and
	// turns away notes.txt. `matched` is the survivors, not the rejections.
	if res.WalkedFiles != 5 || res.Matched != 4 {
		t.Errorf("walked/matched = %d/%d, want 5/4", res.WalkedFiles, res.Matched)
	}
	if len(res.Groups) != 3 {
		t.Fatalf("groups = %d, want 3 (resolved, ambiguous, no_candidate): %+v", len(res.Groups), res.Groups)
	}
	var resolved *service.PreviewGroup
	var ambiguous *service.PreviewGroup
	for i := range res.Groups {
		switch res.Groups[i].Number {
		case "ABC-123":
			resolved = &res.Groups[i]
		default:
			if len(res.Groups[i].Candidates) == 2 {
				ambiguous = &res.Groups[i]
			}
		}
	}
	if resolved == nil || len(resolved.Parts) != 1 || resolved.Occupied {
		t.Errorf("resolved group = %+v", resolved)
	}
	if ambiguous == nil {
		t.Fatalf("no ambiguous group in %+v", res.Groups)
	}
	if len(ambiguous.Parts) != 2 {
		t.Errorf("ambiguous parts = %v", ambiguous.Parts)
	}
	if strings.Join(ambiguous.Candidates, ",") != "ABP-123,ABP-124" {
		t.Errorf("candidates = %v", ambiguous.Candidates)
	}

	// `res` is not part of the contract: V1 probes no video stream.
	var shape struct {
		Groups []map[string]json.RawMessage `json:"groups"`
	}
	f.doJSON(http.MethodGet, "/api/scan/previews/"+started.PreviewID, nil, &shape)
	for _, g := range shape.Groups {
		if _, ok := g["res"]; ok {
			t.Errorf("a preview group carries `res`: %v", g)
		}
		for _, key := range []string{"primary", "parts", "number", "candidates", "marker", "dir", "size", "occupied"} {
			if _, ok := g[key]; !ok {
				t.Errorf("a preview group is missing %q: %v", key, g)
			}
		}
	}

	// The scan page folds the files a rule kept out below the table, each with the
	// rule's name. The corpus' `notes.txt` is the one `everything-else` rejects.
	if res.FilteredTotal != 1 || len(res.Filtered) != 1 {
		t.Fatalf("filtered = %+v (total %d), want notes.txt once", res.Filtered, res.FilteredTotal)
	}
	if filepath.Base(res.Filtered[0].Path) != "notes.txt" || res.Filtered[0].Rule != "everything-else" {
		t.Errorf("filtered[0] = %+v", res.Filtered[0])
	}
	var filteredShape struct {
		Filtered []map[string]json.RawMessage `json:"filtered"`
	}
	f.doJSON(http.MethodGet, "/api/scan/previews/"+started.PreviewID, nil, &filteredShape)
	if len(filteredShape.Filtered) != 1 {
		t.Fatalf("filtered shape = %+v", filteredShape.Filtered)
	}
	for _, key := range []string{"path", "rule"} {
		if _, ok := filteredShape.Filtered[0][key]; !ok {
			t.Errorf("a filtered file is missing %q: %v", key, filteredShape.Filtered[0])
		}
	}
}

func TestPreview_RejectsBadRequestsBefore202(t *testing.T) {
	f := newFixture(t)
	f.wantError(http.MethodPost, "/api/scan/preview", service.PreviewRequest{
		Pipeline: "nope", Roots: []string{f.media},
	}, http.StatusBadRequest, "unknown_pipeline")
	f.wantError(http.MethodPost, "/api/scan/preview", service.PreviewRequest{
		Pipeline: "main",
	}, http.StatusBadRequest, "empty_selection")
	f.wantError(http.MethodPost, "/api/scan/preview", service.PreviewRequest{
		Pipeline: "main", Roots: []string{f.media}, Exclude: []string{t.TempDir()},
	}, http.StatusBadRequest, "exclude_not_under_root")
}

func TestPreview_UnknownIDIs404(t *testing.T) {
	f := newFixture(t)
	f.wantError(http.MethodGet, "/api/scan/previews/p999", nil, http.StatusNotFound, "not_found")
}

func TestCreateTasks_ReturnsTheBatchAndTheSkippedPaths(t *testing.T) {
	f := newFixture(t)
	res := f.createBatch()
	if res.Created != 3 {
		t.Errorf("created = %d, want 3 (one resolved, one ambiguous, one without a candidate)", res.Created)
	}
	var filtered *service.SkippedPath
	for i := range res.Skipped {
		if res.Skipped[i].Reason == "filtered" {
			filtered = &res.Skipped[i]
		}
	}
	if filtered == nil || !strings.HasSuffix(filtered.Path, "notes.txt") {
		t.Errorf("skipped = %+v, want notes.txt as filtered", res.Skipped)
	}
}

// The scan page ticks groups, not files, because a work's parts must travel
// together. That selection has to reach the batch: a body naming one primary creates
// exactly that group's task, and a selection naming nothing in the walk is refused
// rather than quietly creating an empty batch.
func TestCreateTasks_HonoursTheScanPagesGroupSelection(t *testing.T) {
	f := newFixture(t)
	var res service.CreateBatchResult
	f.doJSON(http.MethodPost, "/api/tasks", service.CreateBatchRequest{
		Pipeline: "main",
		Roots:    []string{f.media},
		Groups:   []string{filepath.Join(f.media, "ABC-123.mp4")},
	}, &res)
	if res.Created != 1 {
		t.Fatalf("created = %d, want the one selected group", res.Created)
	}
	page := f.listTasks("")
	if page.Total != 1 || page.Items[0].Number != "ABC-123" {
		t.Errorf("tasks = %+v, want only ABC-123", page.Items)
	}

	f.wantError(http.MethodPost, "/api/tasks", service.CreateBatchRequest{
		Pipeline: "main",
		Roots:    []string{f.media},
		Groups:   []string{filepath.Join(f.media, "gone.mp4")},
	}, http.StatusBadRequest, "empty_selection")
}

func TestCreateTasks_RefusesWhatCannotBecomeABatch(t *testing.T) {
	f := newFixture(t)
	f.wantError(http.MethodPost, "/api/tasks", service.CreateBatchRequest{Pipeline: "main"},
		http.StatusBadRequest, "empty_selection")
	f.wantError(http.MethodPost, "/api/tasks", service.CreateBatchRequest{Pipeline: "nope", Roots: []string{f.media}},
		http.StatusBadRequest, "unknown_pipeline")
	gone := filepath.Join(f.media, "gone")
	f.wantError(http.MethodPost, "/api/tasks", service.CreateBatchRequest{Pipeline: "main", Roots: []string{gone}},
		http.StatusBadRequest, "empty_selection")
}

func TestTasks_ListFiltersAndPages(t *testing.T) {
	f := newFixture(t)
	f.createBatch()

	all := f.listTasks("")
	if all.Total != 3 || len(all.Items) != 3 || all.Page != 1 || all.PerPage != 20 {
		t.Fatalf("page = %+v", all)
	}
	for i := 1; i < len(all.Items); i++ {
		prev, err := time.Parse(time.RFC3339Nano, all.Items[i-1].CreatedAt)
		if err != nil {
			t.Fatal(err)
		}
		cur, err := time.Parse(time.RFC3339Nano, all.Items[i].CreatedAt)
		if err != nil {
			t.Fatal(err)
		}
		if cur.After(prev) {
			t.Errorf("items are not created_at DESC: %s before %s", all.Items[i-1].CreatedAt, all.Items[i].CreatedAt)
		}
	}
	for _, item := range all.Items {
		if item.BatchID == 0 || item.Trigger != "manual" || item.TriggerRef == "" {
			t.Errorf("item = %+v", item)
		}
		if item.Status != "needs_number" && item.Files != nil {
			t.Errorf("only a needs_number item carries files: %+v", item)
		}
	}

	waiting := f.listTasks("?status=needs_number")
	if waiting.Total != 2 {
		t.Errorf("needs_number total = %d, want 2", waiting.Total)
	}
	for _, item := range waiting.Items {
		if item.Status != "needs_number" || len(item.Files) == 0 {
			t.Errorf("item = %+v", item)
		}
		if item.LastError != "ambiguous" && item.LastError != "no_candidate" {
			t.Errorf("last_error = %q", item.LastError)
		}
	}

	resolved := f.listTasks("?number=abc")
	if resolved.Total != 1 || resolved.Items[0].Number != "ABC-123" {
		t.Errorf("number search = %+v", resolved)
	}
	if got := f.listTasks("?number=abc&trigger=monitor"); got.Total != 0 || len(got.Items) != 0 {
		t.Errorf("a trigger filter must be a join: %+v", got)
	}
	byTrigger := f.listTasks("?trigger=manual&status=queued&status=needs_number")
	if byTrigger.Total != 3 {
		t.Errorf("multi-status total = %d", byTrigger.Total)
	}

	// A page past the end keeps the total and returns no items.
	past := f.listTasks("?page=9")
	if past.Total != 3 || len(past.Items) != 0 {
		t.Errorf("page 9 = %+v", past)
	}
	f.wantError(http.MethodGet, "/api/tasks?per_page=30", nil, http.StatusBadRequest, "bad_request")
	f.wantError(http.MethodGet, "/api/tasks?page=0", nil, http.StatusBadRequest, "bad_request")
	f.wantError(http.MethodGet, "/api/tasks?status=bogus", nil, http.StatusBadRequest, "bad_request")
	f.wantError(http.MethodGet, "/api/tasks?created_after=yesterday", nil, http.StatusBadRequest, "bad_request")
}

// finishTask takes the task out of the queue, records one step run and closes the run
// with the given outcome and ledger rows.
func (f *fixture) finishTask(taskID int64, step string, summary string, md meta.Metadata, rows []tasks.FileRow) tasks.Run {
	f.t.Helper()
	ctx := context.Background()
	run, err := f.st.BeginRun(ctx, taskID)
	if err != nil {
		f.t.Fatalf("BeginRun: %v", err)
	}
	stepID, err := f.st.RecordStep(ctx, tasks.Step{
		TaskRunID:   run.ID,
		Idx:         5,
		Type:        step,
		Status:      "ok",
		SummaryJSON: summary,
	})
	if err != nil {
		f.t.Fatalf("RecordStep: %v", err)
	}
	for i := range rows {
		rows[i].StepRunID = stepID
	}
	data, err := json.Marshal(md)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.st.FinishRun(ctx, run.ID, tasks.Outcome{
		Status:       tasks.RunPartial,
		MetadataJSON: string(data),
		Rows:         rows,
	}); err != nil {
		f.t.Fatalf("FinishRun: %v", err)
	}
	return run
}

func TestTaskDetail_GivesAllFourTabsForTheLatestRunOnly(t *testing.T) {
	f := newFixture(t)
	f.createBatch()
	page := f.listTasks("?status=queued")
	if len(page.Items) != 1 {
		t.Fatalf("queued tasks = %d", len(page.Items))
	}
	task := page.Items[0]

	// The first run's traces must not leak into the detail: only the latest run's.
	f.finishTask(task.ID, "scrape", `{"sources":1}`, meta.Metadata{}, nil)

	poster := filepath.Join(f.out, "ABC-123", "poster.png")
	writeFile(t, poster, pngBytes(t, 100, 150))
	sum := sha256.Sum256(pngBytes(t, 100, 150))
	video := filepath.Join(f.out, "ABC-123", "ABC-123.mp4")
	writeFile(t, video, []byte("landed"))
	md := meta.Metadata{Title: "A title"}
	md.Win("title", "javdb")
	md.Win("fanart", "dmm")
	f.finishTask(task.ID, "output", `{"wrote":2}`, md, []tasks.FileRow{
		{Kind: "video", Role: "video", Dst: video, Action: "created", Bytes: 6},
		{Kind: "image", Role: "poster", Dst: poster, Action: "created",
			Bytes: int64(len(pngBytes(t, 100, 150))), SHA256: hex.EncodeToString(sum[:]), Src: "https://example.test/poster.jpg"},
	})

	var detail service.TaskDetail
	f.doJSON(http.MethodGet, "/api/tasks/"+strconv.FormatInt(task.ID, 10), nil, &detail)
	if detail.ID != task.ID || detail.Status != "partial" {
		t.Errorf("detail = %+v", detail.TaskView)
	}
	if detail.Title != "A title" || detail.OutDir != filepath.Dir(video) {
		t.Errorf("derived = title %q out_dir %q", detail.Title, detail.OutDir)
	}
	if detail.Metadata.Title != "A title" || detail.Metadata.Sources["title"] != "javdb" {
		t.Errorf("metadata = %+v", detail.Metadata)
	}
	if detail.Metadata.Poster != "https://example.test/poster.jpg" {
		t.Errorf("metadata.poster = %q, want the ledger's source url", detail.Metadata.Poster)
	}
	if len(detail.Metadata.Missing) == 0 || !detail.Metadata.Partial {
		t.Errorf("missing/partial = %v/%v", detail.Metadata.Missing, detail.Metadata.Partial)
	}
	if strings.Contains(string(mustJSON(t, detail)), `"derived"`) {
		t.Error("Derived must not reach the API")
	}
	if len(detail.StepRuns) != 1 || detail.StepRuns[0].Type != "output" {
		t.Errorf("step_runs = %+v (only the latest run's steps may be shown)", detail.StepRuns)
	}
	if len(detail.FileRows) != 2 || detail.FileRows[0].TaskRunID == 0 {
		t.Errorf("file_rows = %+v", detail.FileRows)
	}
	if len(detail.Images) != 1 || detail.Images[0].Role != "poster" ||
		!strings.Contains(detail.Images[0].URL, "/api/tasks/"+strconv.FormatInt(task.ID, 10)+"/image") {
		t.Errorf("images = %+v", detail.Images)
	}
	if len(detail.TaskFiles) != 1 || detail.TaskFiles[0].Role != "primary" || detail.TaskFiles[0].Candidates == nil {
		t.Errorf("task_files = %+v", detail.TaskFiles)
	}
	f.wantError(http.MethodGet, "/api/tasks/9999", nil, http.StatusNotFound, "not_found")
}

func TestTaskDetail_ShowsTheSixReasonCodesOfTheSources(t *testing.T) {
	f := newFixture(t)
	f.createBatch()
	page := f.listTasks("?status=queued")
	task := page.Items[0]
	ctx := context.Background()
	run, err := f.st.BeginRun(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	stepID, err := f.st.RecordStep(ctx, tasks.Step{
		TaskRunID: run.ID, Idx: 5, Type: "scrape", Status: "partial", SummaryJSON: `{}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range []tasks.SourceRun{
		{StepRunID: stepID, SourceID: "javdb", Status: "ok", FieldsJSON: `{"fields":["title"]}`},
		{StepRunID: stepID, SourceID: "dmm", Status: "failed", Reason: "timeout", Err: "deadline"},
	} {
		if err := f.st.RecordSource(ctx, src); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.st.FinishRun(ctx, run.ID, tasks.Outcome{Status: tasks.RunPartial}); err != nil {
		t.Fatal(err)
	}

	var detail service.TaskDetail
	f.doJSON(http.MethodGet, "/api/tasks/"+strconv.FormatInt(task.ID, 10), nil, &detail)
	if len(detail.SourceRuns) != 2 {
		t.Fatalf("source_runs = %+v", detail.SourceRuns)
	}
	if detail.SourceRuns[1].Reason != "timeout" || detail.SourceRuns[1].SourceID != "dmm" {
		t.Errorf("source run = %+v", detail.SourceRuns[1])
	}
}

func TestRerunAndCancel(t *testing.T) {
	f := newFixture(t)
	f.createBatch()
	page := f.listTasks("")
	var resolvedID, waitingID int64
	for _, item := range page.Items {
		if item.Status == "queued" {
			resolvedID = item.ID
		}
		if item.Status == "needs_number" {
			waitingID = item.ID
		}
	}
	if resolvedID == 0 || waitingID == 0 {
		t.Fatalf("corpus is missing a queued or a needs_number task: %+v", page.Items)
	}

	// Rerunning a queued task is a no-op that says so, not an error.
	rec, raw := f.do(http.MethodPost, "/api/tasks/"+strconv.FormatInt(resolvedID, 10)+"/rerun", struct{}{})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("rerun queued: status %d, body %s", rec.Code, raw)
	}

	// A running task cannot be requeued or cancelled as if it were idle.
	ctx := context.Background()
	run, err := f.st.BeginRun(ctx, resolvedID)
	if err != nil {
		t.Fatal(err)
	}
	f.wantError(http.MethodPost, "/api/tasks/"+strconv.FormatInt(resolvedID, 10)+"/rerun", struct{}{},
		http.StatusConflict, "task_running")
	f.wantError(http.MethodPost, "/api/tasks/"+strconv.FormatInt(resolvedID, 10)+"/cancel", struct{}{},
		http.StatusConflict, "task_running")
	if err := f.st.FinishRun(ctx, run.ID, tasks.Outcome{Status: tasks.RunSucceeded}); err != nil {
		t.Fatal(err)
	}

	// Skipping a number is cancelling the task: the paths are released.
	var cancelled struct {
		Status string `json:"status"`
	}
	f.doJSON(http.MethodPost, "/api/tasks/"+strconv.FormatInt(waitingID, 10)+"/cancel", struct{}{}, &cancelled)
	if cancelled.Status != "cancelled" {
		t.Errorf("status = %q", cancelled.Status)
	}
	f.wantError(http.MethodPost, "/api/tasks/"+strconv.FormatInt(waitingID, 10)+"/cancel", struct{}{},
		http.StatusConflict, "task_terminal")

	// A terminal task can be rerun; an unknown one is 404.
	if rec, raw := f.do(http.MethodPost, "/api/tasks/"+strconv.FormatInt(waitingID, 10)+"/rerun", struct{}{}); rec.Code != http.StatusAccepted {
		t.Fatalf("rerun terminal: status %d, body %s", rec.Code, raw)
	}
	f.wantError(http.MethodPost, "/api/tasks/9999/rerun", struct{}{}, http.StatusNotFound, "not_found")
}

func TestRerunMany_ReportsEachTaskSeparately(t *testing.T) {
	f := newFixture(t)
	f.createBatch()
	page := f.listTasks("")
	var queued, waiting []int64
	for _, item := range page.Items {
		if item.Status == "queued" {
			queued = append(queued, item.ID)
		} else {
			waiting = append(waiting, item.ID)
		}
	}
	ids := append(append([]int64{}, queued...), waiting...)
	ids = append(ids, 9999)
	var results []service.BatchRerunItem
	f.doJSON(http.MethodPost, "/api/tasks/rerun", map[string]any{"ids": ids}, &results)
	if len(results) != len(ids) {
		t.Fatalf("results = %+v", results)
	}
	if !results[0].OK || results[len(results)-1].OK {
		t.Errorf("results = %+v", results)
	}
	if results[len(results)-1].Error != "not_found" {
		t.Errorf("last error = %q", results[len(results)-1].Error)
	}
}

func TestNumbers_RequireEveryVideoAndRequeueTheTask(t *testing.T) {
	f := newFixture(t)
	f.createBatch()
	page := f.listTasks("?status=needs_number")
	var ambiguous *service.TaskView
	for i := range page.Items {
		if page.Items[i].LastError == "ambiguous" {
			ambiguous = &page.Items[i]
		}
	}
	if ambiguous == nil || len(ambiguous.Files) != 2 {
		t.Fatalf("no ambiguous two-part task: %+v", page.Items)
	}
	if len(ambiguous.Files[0].Candidates) != 2 {
		t.Errorf("the panel needs the parse candidates as chips: %+v", ambiguous.Files[0])
	}

	path := "/api/tasks/" + strconv.FormatInt(ambiguous.ID, 10) + "/numbers"
	env := f.wantError(http.MethodPost, path, map[string]any{
		"files": []map[string]any{{"path": ambiguous.Files[0].Path, "assigned_number": "ABP-123"}},
	}, http.StatusUnprocessableEntity, "numbers_incomplete")
	var detail struct {
		MissingPaths []string `json:"missing_paths"`
	}
	if err := json.Unmarshal(env.Error.Detail, &detail); err != nil {
		t.Fatalf("detail = %s: %v", env.Error.Detail, err)
	}
	if len(detail.MissingPaths) != 1 || detail.MissingPaths[0] != ambiguous.Files[1].Path {
		t.Errorf("missing_paths = %v", detail.MissingPaths)
	}

	files := make([]map[string]any, 0, len(ambiguous.Files))
	for i, file := range ambiguous.Files {
		files = append(files, map[string]any{
			"path": file.Path, "assigned_number": "abp-123", "is_part": i > 0,
		})
	}
	var res service.NumbersResult
	f.doJSON(http.MethodPost, path, map[string]any{"files": files}, &res)
	if res.Status != "queued" {
		t.Errorf("status = %q", res.Status)
	}
	after := f.listTasks("?number=ABP-123")
	if after.Total != 1 || after.Items[0].Status != "queued" {
		t.Errorf("the task did not requeue with its number: %+v", after.Items)
	}
	f.wantError(http.MethodPost, path, map[string]any{"files": files},
		http.StatusBadRequest, "bad_request")
}

func TestWritesRequireJSONContentType(t *testing.T) {
	f := newFixture(t)
	req := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(`{"pipeline":"main"}`))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	f.http.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", rec.Code)
	}
	var env errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Error.Code != "unsupported_media_type" {
		t.Errorf("error.code = %q", env.Error.Code)
	}
}

func TestThumbAndImage_ETagAndWidths(t *testing.T) {
	f := newFixture(t)
	f.createBatch()
	page := f.listTasks("?status=queued")
	taskID := page.Items[0].ID
	taskPath := "/api/tasks/" + strconv.FormatInt(taskID, 10)

	original := pngBytes(t, 100, 150)
	poster := filepath.Join(f.out, "ABC-123", "poster.png")
	writeFile(t, poster, original)
	sum := sha256.Sum256(original)
	f.finishTask(taskID, "output", `{}`, meta.Metadata{}, []tasks.FileRow{
		{Kind: "image", Role: "poster", Dst: poster, Action: "created", Bytes: int64(len(original)),
			SHA256: hex.EncodeToString(sum[:])},
	})

	// thumb: 40 by default, scaled.
	rec, body := f.do(http.MethodGet, taskPath+"/thumb", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("thumb: status %d, body %s", rec.Code, body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q", got)
	}
	etag := rec.Header().Get("ETag")
	if etag != `"`+hex.EncodeToString(sum[:])+`"` {
		t.Errorf("ETag = %q", etag)
	}
	if w := decodeWidth(t, body); w != 40 {
		t.Errorf("thumb width = %d, want 40", w)
	}

	// A matching validator answers 304 with no body.
	req := httptest.NewRequest(http.MethodGet, taskPath+"/thumb", nil)
	req.Header.Set("If-None-Match", etag)
	rec304 := httptest.NewRecorder()
	f.http.ServeHTTP(rec304, req)
	if rec304.Code != http.StatusNotModified || rec304.Body.Len() != 0 {
		t.Errorf("If-None-Match: status %d, body %d bytes", rec304.Code, rec304.Body.Len())
	}
	if got := rec304.Header().Get("ETag"); got != etag {
		t.Errorf("304 ETag = %q", got)
	}

	// image: the original unless a width is named.
	rec, body = f.do(http.MethodGet, taskPath+"/image", nil)
	if rec.Code != http.StatusOK || !bytes.Equal(body, original) {
		t.Errorf("image default must be the original bytes (status %d, %d bytes)", rec.Code, len(body))
	}
	rec, body = f.do(http.MethodGet, taskPath+"/image?w=320", nil)
	if rec.Code != http.StatusOK || decodeWidth(t, body) != 320 {
		t.Errorf("w=320: status %d", rec.Code)
	}

	f.wantError(http.MethodGet, taskPath+"/image?w=41", nil, http.StatusBadRequest, "bad_request")
	f.wantError(http.MethodGet, taskPath+"/image?role=nfo", nil, http.StatusBadRequest, "bad_request")
	f.wantError(http.MethodGet, taskPath+"/image?idx=3", nil, http.StatusNotFound, "image_missing")
}

func TestBatches_FeedTheFilterDropdown(t *testing.T) {
	f := newFixture(t)
	created := f.createBatch()
	var page service.Page[service.BatchView]
	f.doJSON(http.MethodGet, "/api/batches?limit=5", nil, &page)
	if page.Total != 1 || len(page.Items) != 1 || page.PerPage != 5 {
		t.Fatalf("page = %+v", page)
	}
	if page.Items[0].ID != created.BatchID || page.Items[0].StartedAt == "" {
		t.Errorf("batch = %+v (the time label is started_at)", page.Items[0])
	}
	if page.Items[0].Tasks != 3 || page.Items[0].Skipped != 1 {
		t.Errorf("counters = %+v", page.Items[0])
	}
	f.wantError(http.MethodGet, "/api/batches?limit=0", nil, http.StatusBadRequest, "bad_request")
}

// pngBytes makes a solid-colour PNG of the given size.
func pngBytes(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// decodeWidth reports the pixel width of an encoded PNG.
func decodeWidth(t *testing.T, data []byte) int {
	t.Helper()
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("the response is not a decodable image: %v", err)
	}
	return img.Bounds().Dx()
}

// mustJSON renders a value for assertions that inspect keys.
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestTaskDetail_WithoutARunIsAllEmptyArrays(t *testing.T) {
	f := newFixture(t)
	f.createBatch()
	page := f.listTasks("?status=queued")
	task := page.Items[0]

	rec, body := f.do(http.MethodGet, "/api/tasks/"+strconv.FormatInt(task.ID, 10), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"step_runs", "source_runs", "file_rows", "images"} {
		if got := string(raw[key]); got != "[]" {
			t.Errorf("%s = %s, want []", key, got)
		}
	}
	var detail service.TaskDetail
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Stage != "" || detail.OutDir != "" || detail.Title != "" {
		t.Errorf("an unrun task has no derived values: %+v", detail.TaskView)
	}
	if detail.Metadata.Sources == nil || detail.Metadata.Missing == nil || detail.Metadata.Extrafanarts == nil {
		t.Errorf("metadata lists must be empty, not null: %+v", detail.Metadata)
	}
}

func TestTaskDetail_StageIsTheLatestStepType(t *testing.T) {
	f := newFixture(t)
	f.createBatch()
	task := f.listTasks("?status=queued").Items[0]
	f.finishTask(task.ID, "nfo", `{}`, meta.Metadata{}, nil)

	var detail service.TaskDetail
	f.doJSON(http.MethodGet, "/api/tasks/"+strconv.FormatInt(task.ID, 10), nil, &detail)
	if detail.Stage != "nfo" {
		t.Errorf("stage = %q, want the latest step's type", detail.Stage)
	}
}

func TestImage_MissingFileOnDiskIs404(t *testing.T) {
	f := newFixture(t)
	f.createBatch()
	taskID := f.listTasks("?status=queued").Items[0].ID
	poster := filepath.Join(f.out, "ABC-123", "poster.png")
	writeFile(t, poster, pngBytes(t, 100, 150))
	f.finishTask(taskID, "output", `{}`, meta.Metadata{}, []tasks.FileRow{
		{Kind: "image", Role: "poster", Dst: poster, Action: "created"},
	})
	if err := os.Remove(poster); err != nil {
		t.Fatal(err)
	}
	f.wantError(http.MethodGet, "/api/tasks/"+strconv.FormatInt(taskID, 10)+"/image", nil,
		http.StatusNotFound, "image_missing")
}

func TestNoDatabase_RowEndpointsAnswer503(t *testing.T) {
	dir := t.TempDir()
	pipelineFile := filepath.Join(dir, "main.yaml")
	writeFile(t, pipelineFile, []byte(pipelineYAML(filepath.Join(dir, "out"))))
	pipeline, err := config.LoadPipelineFile(pipelineFile, config.DefaultStepRegistry(), nil)
	if err != nil {
		t.Fatal(err)
	}
	settings := config.Defaults()
	settings.PipelinesDir = dir
	settings.BrowseRoots = []config.BrowseRoot{{Path: dir, Label: "tmp"}}
	svc, err := service.New(service.Config{Settings: settings, Pipelines: []config.Pipeline{pipeline}})
	if err != nil {
		t.Fatal(err)
	}
	h := api.NewWithService("test", svc, nil).Handler()

	for _, path := range []string{"/api/tasks", "/api/batches", "/api/tasks/1"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("GET %s: status %d, want 503", path, rec.Code)
		}
		var env errorEnvelope
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		if env.Error.Code != "db_unavailable" {
			t.Errorf("GET %s: error.code = %q", path, env.Error.Code)
		}
	}

	// The endpoints that do not need rows keep working: the picker and the pipeline
	// list are what a process with a broken database can still answer.
	for _, path := range []string{"/api/fs/dirs?path=" + dir, "/api/pipelines"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: status %d, body %s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestTasks_FilterByBatchAndCreationWindow(t *testing.T) {
	f := newFixture(t)
	first := f.createBatch()

	inBatch := f.listTasks("?batch=" + strconv.FormatInt(first.BatchID, 10))
	if inBatch.Total != 3 {
		t.Errorf("batch filter total = %d, want 3", inBatch.Total)
	}
	if got := f.listTasks("?batch=" + strconv.FormatInt(first.BatchID+1, 10)); got.Total != 0 {
		t.Errorf("an unknown batch must select nothing: %+v", got)
	}

	// The window is inclusive on both ends, so a task's own timestamp finds it.
	page := f.listTasks("")
	at := page.Items[len(page.Items)-1].CreatedAt
	if got := f.listTasks("?created_before=" + at); got.Total != 3 {
		t.Errorf("created_before: total = %d, want 3", got.Total)
	}
	after := "2100-01-01T00:00:00Z"
	if got := f.listTasks("?created_after=" + after); got.Total != 0 {
		t.Errorf("created_after in the future: total = %d, want 0", got.Total)
	}
	if got := f.listTasks("?created_before=" + after); got.Total != 3 {
		t.Errorf("created_before in the future: total = %d, want 3", got.Total)
	}
}

func TestCreateTasks_HonoursTheScopeLedgerExclude(t *testing.T) {
	f := newFixture(t)
	var res service.CreateBatchResult
	f.doJSON(http.MethodPost, "/api/tasks", service.CreateBatchRequest{
		Pipeline: "main",
		Roots:    []string{f.media},
		Exclude:  []string{filepath.Join(f.media, "skip")},
	}, &res)
	// The walk itself is asserted by the next test; this one proves the request shape
	// is accepted and that an exclusion outside every root is refused.
	if res.Created != 3 {
		t.Errorf("created = %d, want 3", res.Created)
	}
	f.wantError(http.MethodPost, "/api/tasks", service.CreateBatchRequest{
		Pipeline: "main",
		Roots:    []string{f.media},
		Exclude:  []string{t.TempDir()},
	}, http.StatusBadRequest, "exclude_not_under_root")
}

func TestCreateTasks_ExcludeSkipsTheSubtree(t *testing.T) {
	f := newFixture(t)
	skipped := filepath.Join(f.media, "skip")
	writeFile(t, filepath.Join(skipped, "XYZ-999.mp4"), []byte("excluded"))
	var res service.CreateBatchResult
	f.doJSON(http.MethodPost, "/api/tasks", service.CreateBatchRequest{
		Pipeline: "main",
		Roots:    []string{f.media},
		Exclude:  []string{skipped},
	}, &res)
	page := f.listTasks("?number=XYZ-999")
	if page.Total != 0 {
		t.Errorf("the excluded subtree was walked: %+v", page.Items)
	}
	if res.Created != 3 {
		t.Errorf("created = %d, want the three files outside the exclusion", res.Created)
	}
}

func TestRoots_MustNotOverlap(t *testing.T) {
	f := newFixture(t)
	nested := filepath.Join(f.media, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	f.wantError(http.MethodPost, "/api/tasks", service.CreateBatchRequest{
		Pipeline: "main",
		Roots:    []string{f.media, nested},
	}, http.StatusBadRequest, "bad_request")
	f.wantError(http.MethodPost, "/api/scan/preview", service.PreviewRequest{
		Pipeline: "main",
		Roots:    []string{f.media, f.media},
	}, http.StatusBadRequest, "bad_request")
}

func TestBatches_TotalIsNotThePageSize(t *testing.T) {
	f := newFixture(t)
	f.createBatch()
	f.createBatch()
	var page service.Page[service.BatchView]
	f.doJSON(http.MethodGet, "/api/batches?limit=1", nil, &page)
	if len(page.Items) != 1 {
		t.Fatalf("items = %d, want the limit", len(page.Items))
	}
	if page.Total != 2 {
		t.Errorf("total = %d, want every batch (the page is a limit)", page.Total)
	}
}

func TestStepSummaries_SpeakSnakeCase(t *testing.T) {
	f := newFixture(t)
	created := f.createBatch()
	steps, err := f.st.Steps(context.Background(), tasks.Owner{BatchID: created.BatchID})
	if err != nil {
		t.Fatal(err)
	}
	var parse *tasks.Step
	for i := range steps {
		if steps[i].Type == "parse" {
			parse = &steps[i]
		}
	}
	if parse == nil {
		t.Fatalf("the batch has no parse trace: %+v", steps)
	}
	var summary map[string]json.RawMessage
	if err := json.Unmarshal([]byte(parse.SummaryJSON), &summary); err != nil {
		t.Fatalf("summary %q: %v", parse.SummaryJSON, err)
	}
	for _, key := range []string{"groups", "parts", "resolved", "unresolved", "candidates"} {
		if _, ok := summary[key]; !ok {
			t.Errorf("summary is missing the snake_case key %q: %s", key, parse.SummaryJSON)
		}
	}
	if _, ok := summary["Groups"]; ok {
		t.Errorf("summary leaks a Go field name: %s", parse.SummaryJSON)
	}
}
