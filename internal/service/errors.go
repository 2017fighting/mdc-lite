package service

import "fmt"

// The machine codes the API's error body carries. They are a closed set: the
// payload only ever transports these, and the Chinese wording lives in the
// frontend's local table.
const (
	// CodeBadRequest is the fallback 400: a malformed body, a bad page size, an
	// unwritable parameter.
	CodeBadRequest = "bad_request"
	// CodeEmptySelection means a trigger named no path at all.
	CodeEmptySelection = "empty_selection"
	// CodeRootNotBrowsable means the directory picker was asked for a path outside
	// `browse_roots`.
	CodeRootNotBrowsable = "root_not_browsable"
	// CodeExcludeNotUnderRoot means an exclusion is not a true child of one of the
	// trigger's roots.
	CodeExcludeNotUnderRoot = "exclude_not_under_root"
	// CodeMonitorRootNested is the nested-roots refusal of a monitor. V1 has no
	// monitor endpoint; the code is here because the error table is.
	CodeMonitorRootNested = "monitor_root_nested"
	// CodeUnknownPipeline means the named pipeline is not loaded.
	CodeUnknownPipeline = "unknown_pipeline"
	// CodeNotFound is anything addressed by an id that does not exist.
	CodeNotFound = "not_found"
	// CodeImageMissing means the ledger has no such image, or the file behind it is
	// gone from disk.
	CodeImageMissing = "image_missing"
	// CodeTaskRunning means the task a slot owns is already running.
	CodeTaskRunning = "task_running"
	// CodeTaskTerminal means the task already ended, so there is nothing to cancel.
	CodeTaskTerminal = "task_terminal"
	// CodeUnsupportedMediaType is a write that did not send JSON, which also keeps a
	// form-based CSRF from reaching a handler.
	CodeUnsupportedMediaType = "unsupported_media_type"
	// CodeNumbersIncomplete means a numbers submission left a video without a
	// number; detail.missing_paths says which.
	CodeNumbersIncomplete = "numbers_incomplete"
	// CodeInternal is an unexpected failure. Its message is kept in the response
	// because the operator of a single-user process is also its developer; it says
	// nothing they would have to unlearn.
	CodeInternal = "internal"
	// CodeDBUnavailable means the database is not usable, so the endpoint cannot
	// read or write rows.
	CodeDBUnavailable = "db_unavailable"
	// CodeUnauthorized is shape only. V1 authenticates nothing in-process (that is
	// the reverse proxy's job), so no handler returns it.
	CodeUnauthorized = "unauthorized"
)

// Error is a failure with the HTTP status and the machine code the response needs.
// A handler that gets anything else answers 500 internal.
type Error struct {
	Status  int
	Code    string
	Message string
	// Detail is the optional structured half: `numbers_incomplete` carries
	// `{"missing_paths": [...]}`.
	Detail any
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// badRequest is a 400 with the given code.
func badRequest(code, message string) *Error {
	return &Error{Status: 400, Code: code, Message: message}
}

// badRequestf is badRequest with a formatted message.
func badRequestf(code, format string, args ...any) *Error {
	return badRequest(code, fmt.Sprintf(format, args...))
}

// notFound is the 404 for an id that does not exist.
func notFound(format string, args ...any) *Error {
	return &Error{Status: 404, Code: CodeNotFound, Message: fmt.Sprintf(format, args...)}
}

// conflict is the 409 for a task a human cannot act on right now.
func conflict(code, message string) *Error {
	return &Error{Status: 409, Code: code, Message: message}
}

// BadRequest is the exported 400, for a caller (the HTTP layer) whose message is
// about the request itself rather than about a domain rule.
func BadRequest(message string) *Error {
	return badRequest(CodeBadRequest, message)
}
