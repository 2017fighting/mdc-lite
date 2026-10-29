// Package api serves the HTTP surface: the health probe, the 14 other V1
// endpoints, and the embedded frontend. It is a thin adapter over internal/service
// — the rules live there, and the CLI calls the same code.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/2017fighting/mdc-lite/internal/service"
)

// Store is the part of the database the health endpoint needs.
//
// A nil *store.Store in a Store field is not a nil Store interface: calling a
// method on it panics instead of reporting "no database". New therefore takes the
// interface and the caller must pass an untyped nil, which is why the CLI keeps a
// separate variable rather than assigning a typed nil.
type Store interface {
	Path() string
	Migrations() int64
	Ready(ctx context.Context) error
}

// Server holds what the handlers need.
type Server struct {
	// Version is the build's version string.
	Version string
	// Store may be nil when the database could not be opened at all; health
	// then reports db_unavailable.
	Store Store
	// Service is nil only in a test that exercises health alone: the other endpoints
	// are not registered then, and the /api/ catch-all answers 404 instead.
	Service *service.Service
	// Static serves the embedded frontend.
	Static http.Handler

	started time.Time
}

// New builds a server whose health probe reads st directly. It is the constructor
// for tests and for a server with no application surface.
func New(version string, st Store, static http.Handler) *Server {
	return &Server{Version: version, Store: st, Static: static, started: time.Now()}
}

// NewWithService builds the full server: the service answers the health probe's
// three questions (it owns the database) and every other endpoint.
func NewWithService(version string, svc *service.Service, static http.Handler) *Server {
	s := New(version, svc, static)
	s.Service = svc
	return s
}

// Handler returns the routed handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	// Kubernetes also probes /healthz by convention; aliasing it costs one line
	// and saves every deployment from carrying a rewrite rule.
	mux.HandleFunc("GET /healthz", s.health)

	s.routes(mux)

	// The method-less catch-all answers every unmatched /api/ request with the JSON
	// error shape. A plain 405 from the mux would be the one response the frontend
	// could not parse with the same code as the others.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, service.CodeNotFound, "no such endpoint: "+r.Method+" "+r.URL.Path)
	})
	if s.Static != nil {
		// Registered without a method: the method-less /api/ catch-all above needs a
		// method-less neighbour to compare paths with (a more specific path still wins).
		mux.Handle("/", s.Static)
	}
	return requireJSONContentType(mux)
}

// routes registers the endpoints of V1. The second batch's seven endpoints
// (monitors and the webhook) are shape-frozen but not registered.
func (s *Server) routes(mux *http.ServeMux) {
	if s.Service == nil {
		return
	}
	mux.HandleFunc("GET /api/pipelines", s.pipelines)
	mux.HandleFunc("GET /api/fs/dirs", s.browse)
	mux.HandleFunc("POST /api/scan/preview", s.startPreview)
	mux.HandleFunc("GET /api/scan/previews/{id}", s.preview)
	mux.HandleFunc("POST /api/tasks", s.createTasks)
	mux.HandleFunc("GET /api/tasks", s.listTasks)
	mux.HandleFunc("POST /api/tasks/rerun", s.rerunTasks)
	mux.HandleFunc("GET /api/tasks/{id}", s.taskDetail)
	mux.HandleFunc("POST /api/tasks/{id}/rerun", s.rerunTask)
	mux.HandleFunc("POST /api/tasks/{id}/cancel", s.cancelTask)
	mux.HandleFunc("POST /api/tasks/{id}/numbers", s.fillNumbers)
	mux.HandleFunc("GET /api/tasks/{id}/thumb", s.thumb)
	mux.HandleFunc("GET /api/tasks/{id}/image", s.image)
	mux.HandleFunc("GET /api/batches", s.batches)
}

type healthDB struct {
	OK         bool   `json:"ok"`
	Path       string `json:"path"`
	Migrations int64  `json:"migrations"`
	Error      string `json:"error,omitempty"`
}

type healthBody struct {
	Status   string       `json:"status"`
	Version  string       `json:"version"`
	UptimeMS int64        `json:"uptime_ms"`
	DB       healthDB     `json:"db"`
	Error    *errorDetail `json:"error,omitempty"`
}

// CodeDBUnavailable is the machine code a 503 from /api/health carries.
const CodeDBUnavailable = service.CodeDBUnavailable

// health answers the readiness probe. 200 means "the database opens, the
// migrations ran, and the tables are there"; anything less is 503 db_unavailable,
// because a process that cannot reach its database must not receive traffic.
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	body := healthBody{
		Status:   "ok",
		Version:  s.Version,
		UptimeMS: time.Since(s.started).Milliseconds(),
		DB:       healthDB{OK: true},
	}
	if s.Store == nil {
		body.Status = "unavailable"
		body.DB = healthDB{OK: false, Error: "database is not open"}
		e := newError(CodeDBUnavailable, "the database is not open")
		body.Error = &e.Error
		writeJSON(w, http.StatusServiceUnavailable, body)
		return
	}
	body.DB.Path = s.Store.Path()
	body.DB.Migrations = s.Store.Migrations()
	if err := s.Store.Ready(ctx); err != nil {
		body.Status = "unavailable"
		body.DB.OK = false
		body.DB.Error = err.Error()
		e := newError(CodeDBUnavailable, err.Error())
		body.Error = &e.Error
		writeJSON(w, http.StatusServiceUnavailable, body)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

// fail renders one service error. A *service.Error already knows its status and
// machine code; anything else is an unexpected failure (500 internal) whose message
// is kept because the operator of a single-user process is also its developer.
func (s *Server) fail(w http.ResponseWriter, err error) {
	var se *service.Error
	if errors.As(err, &se) {
		body := newError(se.Code, se.Message)
		if se.Detail != nil {
			body.Error.Detail = se.Detail
		}
		writeJSON(w, se.Status, body)
		return
	}
	writeError(w, http.StatusInternalServerError, service.CodeInternal, err.Error())
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// errorDetail is the shape every error uses: {error:{code, message, detail?}}.
type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  any    `json:"detail,omitempty"`
}

// errorBody wraps errorDetail under the "error" key.
type errorBody struct {
	Error errorDetail `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, newError(code, message))
}

func newError(code, message string) errorBody {
	return errorBody{Error: errorDetail{Code: code, Message: message}}
}
