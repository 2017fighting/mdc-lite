// Package api serves the HTTP surface. V1 opens one endpoint here — the health
// probe — because that is what a k8s deployment needs before anything else can
// be trusted; the rest arrive with their own tickets.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
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
	// Static serves the embedded frontend.
	Static http.Handler

	started time.Time
}

// New builds a server.
func New(version string, st Store, static http.Handler) *Server {
	return &Server{Version: version, Store: st, Static: static, started: time.Now()}
}

// Handler returns the routed handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	// Kubernetes also probes /healthz by convention; aliasing it costs one line
	// and saves every deployment from carrying a rewrite rule.
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
	})
	if s.Static != nil {
		mux.Handle("GET /", s.Static)
	}
	return mux
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
const CodeDBUnavailable = "db_unavailable"

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
