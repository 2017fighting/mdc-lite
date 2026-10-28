package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/2017fighting/mdc-lite/internal/api"
	"github.com/2017fighting/mdc-lite/internal/store"
	"github.com/2017fighting/mdc-lite/internal/web"
)

func newServer(t *testing.T) (*api.Server, *store.Store) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "mdc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return api.New("test", st, web.Handler(), nil), st
}

type health struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	DB      struct {
		OK         bool   `json:"ok"`
		Path       string `json:"path"`
		Migrations int64  `json:"migrations"`
		Error      string `json:"error"`
	} `json:"db"`
}

func getHealth(t *testing.T, h http.Handler) (int, health) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	var body health
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	return rec.Code, body
}

func TestHealth_ReadyIs200(t *testing.T) {
	s, st := newServer(t)
	code, body := getHealth(t, s.Handler())
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %+v)", code, body)
	}
	if body.Status != "ok" || !body.DB.OK {
		t.Errorf("body = %+v", body)
	}
	if body.Version != "test" {
		t.Errorf("version = %q", body.Version)
	}
	if body.DB.Path != st.Path() {
		t.Errorf("db.path = %q, want %q", body.DB.Path, st.Path())
	}
	if body.DB.Migrations < 1 {
		t.Errorf("db.migrations = %d", body.DB.Migrations)
	}
}

func TestHealth_NotReadyIs503DbUnavailable(t *testing.T) {
	// No store at all: the process came up but its database did not.
	s := api.New("test", nil, nil, nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	var body struct {
		Status string `json:"status"`
		DB     struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		} `json:"db"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "unavailable" || body.DB.OK {
		t.Errorf("body = %+v", body)
	}
	if body.DB.Error == "" {
		t.Error("the 503 must say why the database is unavailable")
	}
	if body.Error.Code != api.CodeDBUnavailable {
		t.Errorf("error.code = %q, want %q", body.Error.Code, api.CodeDBUnavailable)
	}
}

func TestHealth_IncompleteSchemaIs503(t *testing.T) {
	s, st := newServer(t)
	if _, err := st.Write().Exec(`DROP TABLE tasks`); err != nil {
		t.Fatal(err)
	}
	code, body := getHealth(t, s.Handler())
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", code)
	}
	if body.DB.OK {
		t.Errorf("db.ok must be false: %+v", body)
	}
	if body.DB.Error == "" {
		t.Error("the 503 must carry the reason")
	}
}

func TestUnknownAPIPathIs404WithTheErrorShape(t *testing.T) {
	s, _ := newServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	if body.Error.Code != "not_found" || body.Error.Message == "" {
		t.Errorf("body = %+v", body)
	}
}

func TestRootServesTheEmbeddedPlaceholder(t *testing.T) {
	s, _ := newServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got == "" {
		t.Errorf("no Content-Type")
	}
	if rec.Body.Len() == 0 {
		t.Error("the embedded page is empty")
	}
}

func TestDeepLinkFallsBackToIndex(t *testing.T) {
	s, _ := newServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/tasks/12", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("a deep link must boot the app, got %d", rec.Code)
	}
}

// errStore is a store whose readiness check fails.
type errStore struct{ err error }

func (e errStore) Path() string      { return "/config/mdc.db" }
func (e errStore) Migrations() int64 { return 0 }
func (e errStore) Ready(context.Context) error {
	return e.err
}

func TestHealth_ReportsTheReadinessError(t *testing.T) {
	s := api.New("test", errStore{err: errors.New("migrations failed: no such table")}, nil, nil)
	code, body := getHealth(t, s.Handler())
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", code)
	}
	if body.DB.Error != "migrations failed: no such table" {
		t.Errorf("db.error = %q", body.DB.Error)
	}
}

func TestHealthzAlias(t *testing.T) {
	s, _ := newServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
