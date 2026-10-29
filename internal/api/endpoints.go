package api

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/2017fighting/mdc-lite/internal/service"
)

// This file is the HTTP adapter: parse the request, call the service, render the
// answer. Every rule (what a page size may be, what a status means, where an image
// lives) belongs to the service; the only decisions here are HTTP's own — status
// codes, headers and the JSON shape.

// maxBodyBytes caps a request body. The largest legitimate body is a numbers
// submission for a many-part group; a megabyte is generous and keeps a stray upload
// from becoming an allocation.
const maxBodyBytes = 1 << 20

// pipelines answers `GET /api/pipelines`.
func (s *Server) pipelines(w http.ResponseWriter, _ *http.Request) {
	items := s.Service.Pipelines()
	writeJSON(w, http.StatusOK, service.Page[service.PipelineView]{
		Items: items, Total: len(items), Page: 1, PerPage: len(items),
	})
}

// browse answers `GET /api/fs/dirs?path=&stats=`.
func (s *Server) browse(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	stats := false
	switch q.Get("stats") {
	case "", "0":
	case "1":
		stats = true
	default:
		s.fail(w, service.BadRequest("stats must be 1 or absent"))
		return
	}
	res, err := s.Service.Browse(r.Context(), q.Get("path"), stats)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// startPreview answers `POST /api/scan/preview` with 202.
func (s *Server) startPreview(w http.ResponseWriter, r *http.Request) {
	var req service.PreviewRequest
	if !decodeBody(w, r, &req) {
		return
	}
	id, err := s.Service.StartPreview(r.Context(), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Location", "/api/scan/previews/"+id)
	writeJSON(w, http.StatusAccepted, map[string]string{"preview_id": id})
}

// preview answers `GET /api/scan/previews/{id}`.
func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	res, err := s.Service.Preview(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// createTasks answers `POST /api/tasks`.
func (s *Server) createTasks(w http.ResponseWriter, r *http.Request) {
	var req service.CreateBatchRequest
	if !decodeBody(w, r, &req) {
		return
	}
	res, err := s.Service.CreateBatch(r.Context(), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// listTasks answers `GET /api/tasks`.
func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	q, ok := taskQuery(w, r)
	if !ok {
		return
	}
	page, err := s.Service.ListTasks(r.Context(), q)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// taskQuery parses the query string. An unparseable number or timestamp is a 400
// here, before the service sees a zero it would have to interpret as "absent".
func taskQuery(w http.ResponseWriter, r *http.Request) (service.TaskQuery, bool) {
	q := r.URL.Query()
	out := service.TaskQuery{Statuses: q["status"], Number: q.Get("number")}
	var ok bool
	batch, ok := intParam(w, q.Get("batch"), "batch")
	if !ok {
		return service.TaskQuery{}, false
	}
	out.BatchID = int64(batch)
	out.Trigger = q.Get("trigger")
	if out.Page, ok = intParam(w, q.Get("page"), "page"); !ok {
		return service.TaskQuery{}, false
	}
	if raw := q.Get("page"); raw != "" && out.Page < 1 {
		writeError(w, http.StatusBadRequest, service.CodeBadRequest, "page must be >= 1, got "+raw)
		return service.TaskQuery{}, false
	}
	if out.PerPage, ok = intParam(w, q.Get("per_page"), "per_page"); !ok {
		return service.TaskQuery{}, false
	}
	if out.CreatedAfter, ok = timeParam(w, q.Get("created_after"), "created_after"); !ok {
		return service.TaskQuery{}, false
	}
	if out.CreatedBefore, ok = timeParam(w, q.Get("created_before"), "created_before"); !ok {
		return service.TaskQuery{}, false
	}
	return out, true
}

// taskDetail answers `GET /api/tasks/{id}`.
func (s *Server) taskDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	detail, err := s.Service.TaskDetail(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// rerunTask answers `POST /api/tasks/{id}/rerun` with 202.
func (s *Server) rerunTask(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok || !decodeBody(w, r, &struct{}{}) {
		return
	}
	res, err := s.Service.Rerun(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, res)
}

// cancelTask answers `POST /api/tasks/{id}/cancel`.
func (s *Server) cancelTask(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok || !decodeBody(w, r, &struct{}{}) {
		return
	}
	status, err := s.Service.Cancel(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": status})
}

// rerunTasks answers `POST /api/tasks/rerun`: one result per id, never one status
// for the batch of them.
func (s *Server) rerunTasks(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if len(req.IDs) == 0 {
		s.fail(w, service.BadRequest("ids must name at least one task"))
		return
	}
	writeJSON(w, http.StatusOK, s.Service.RerunMany(r.Context(), req.IDs))
}

// fillNumbers answers `POST /api/tasks/{id}/numbers`.
func (s *Server) fillNumbers(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	var req struct {
		Files []service.NumberFill `json:"files"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	res, err := s.Service.FillNumbers(r.Context(), id, req.Files)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// thumb answers `GET /api/tasks/{id}/thumb`: the list's small poster.
func (s *Server) thumb(w http.ResponseWriter, r *http.Request) {
	s.serveImage(w, r, service.DefaultThumbWidth)
}

// image answers `GET /api/tasks/{id}/image`: the original unless a width is named.
func (s *Server) image(w http.ResponseWriter, r *http.Request) {
	s.serveImage(w, r, 0)
}

// serveImage is both image endpoints: they differ only in the default width.
func (s *Server) serveImage(w http.ResponseWriter, r *http.Request, defaultWidth int) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	idx := 0
	if raw := q.Get("idx"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			s.fail(w, service.BadRequest("idx must be an integer, got "+raw))
			return
		}
		idx = parsed
	}
	width := defaultWidth
	if raw := q.Get("w"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			s.fail(w, service.BadRequest("w must be an integer, got "+raw))
			return
		}
		width = parsed
	}
	img, err := s.Service.Image(r.Context(), service.ImageRequest{
		TaskID:      id,
		Role:        q.Get("role"),
		Idx:         idx,
		Width:       width,
		IfNoneMatch: r.Header.Get("If-None-Match"),
	})
	if err != nil {
		s.fail(w, err)
		return
	}
	h := w.Header()
	h.Set("ETag", `"`+img.ETag+`"`)
	h.Set("Cache-Control", "private, max-age=31536000, immutable")
	if img.NotModified {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", img.ContentType)
	h.Set("Content-Length", strconv.Itoa(len(img.Bytes)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(img.Bytes)
}

// batches answers `GET /api/batches?limit=`.
func (s *Server) batches(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("limit")
	limit, ok := intParam(w, raw, "limit")
	if !ok {
		return
	}
	if raw != "" && limit < 1 {
		writeError(w, http.StatusBadRequest, service.CodeBadRequest, "limit must be >= 1, got "+raw)
		return
	}
	page, err := s.Service.Batches(r.Context(), limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// decodeBody reads one JSON request body. An empty body is allowed (a POST with
// nothing to say still had to declare its content type), but a body that is not the
// expected shape is a 400 the caller sees immediately.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return true
		}
		writeError(w, http.StatusBadRequest, service.CodeBadRequest, "the request body is not valid JSON: "+err.Error())
		return false
	}
	return true
}

// idParam reads the {id} path segment.
func idParam(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := r.PathValue("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, service.CodeBadRequest, "the task id must be a positive integer, got "+raw)
		return 0, false
	}
	return id, true
}

// intParam parses an optional integer query parameter. An empty value is zero (the
// "absent" the service defaults), anything unparseable is a 400 that names it.
func intParam(w http.ResponseWriter, raw, name string) (int, bool) {
	if raw == "" {
		return 0, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, service.CodeBadRequest, name+" must be an integer, got "+raw)
		return 0, false
	}
	return n, true
}

// timeParam parses an optional RFC 3339 timestamp.
func timeParam(w http.ResponseWriter, raw, name string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, true
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, service.CodeBadRequest, name+" must be an RFC 3339 timestamp, got "+raw)
		return time.Time{}, false
	}
	return t, true
}

// requireJSONContentType refuses a write that did not declare JSON. It also keeps a
// form-based cross-site post from reaching a handler at all, which is why the rule is
// enforced on every write rather than only on bodies that are read.
func requireJSONContentType(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/") && !isJSONContentType(r.Header.Get("Content-Type")) {
			writeError(w, http.StatusUnsupportedMediaType, service.CodeUnsupportedMediaType,
				"write endpoints take application/json")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isJSONContentType reports whether the header names JSON, parameters allowed
// (`application/json; charset=utf-8`).
func isJSONContentType(header string) bool {
	if header == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(header)
	if err != nil {
		return false
	}
	return mediaType == "application/json"
}
