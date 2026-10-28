package source

import "errors"

// The six machine codes a failed answer carries, and the only values
// `source_runs.reason` accepts. `notfound` is not among them: "the site has no such
// work" is an answer, not a failure, and a code here would make it look like one.
//
// The transport (the Fetcher) produces the first five; a source produces `blocked`
// when it recognises a site's interception page and `parse` when it recognises the
// page but cannot read the fields out of it.
const (
	ReasonEgressUnavailable = "egress_unavailable"
	ReasonTimeout           = "timeout"
	ReasonBlocked           = "blocked"
	ReasonCooldown          = "cooldown"
	ReasonHTTP5xx           = "http_5xx"
	ReasonParse             = "parse"
)

// Reasons lists the six codes in the order the spec names them.
func Reasons() []string {
	return []string{ReasonEgressUnavailable, ReasonTimeout, ReasonBlocked, ReasonCooldown, ReasonHTTP5xx, ReasonParse}
}

// ValidReason reports whether code is one of the six.
func ValidReason(code string) bool {
	for _, r := range Reasons() {
		if r == code {
			return true
		}
	}
	return false
}

// Failure is a failed answer with its reason code. A source returns one when it
// can say *why* it failed; the transport returns its own errors, which implement
// the same Reason method, so the engine reads a reason off either.
//
// Err keeps the human-readable cause: `source_runs` records the code in `reason`
// and the text in `err`, and they answer different questions ("which of the six"
// vs "what happened").
type Failure struct {
	Code string
	Err  error
}

// Fail wraps err as a failure with code. A nil err still names the code.
func Fail(code string, err error) *Failure { return &Failure{Code: code, Err: err} }

// Reason returns the machine code.
func (f *Failure) Reason() string { return f.Code }

// Unwrap exposes the cause, so errors.Is keeps working through the wrapper.
func (f *Failure) Unwrap() error { return f.Err }

// Error renders the cause, prefixed by the code only when there is no cause: the
// code is already a column of its own, so repeating it in the text makes the two
// answer the same question twice.
func (f *Failure) Error() string {
	if f.Err == nil {
		return "source failed: " + f.Code
	}
	return f.Err.Error()
}

// Reasoned is an error that knows which of the six codes it is. Both the Fetcher's
// error and a source's Failure implement it.
type Reasoned interface {
	Reason() string
}

// ReasonOf reads the reason code off err, or "" when err carries none. An error
// nobody classified is left unclassified on purpose: writing a made-up code into
// `source_runs.reason` would be a wrong diagnosis stored forever.
func ReasonOf(err error) string {
	var r Reasoned
	if errors.As(err, &r) {
		code := r.Reason()
		if ValidReason(code) {
			return code
		}
	}
	return ""
}
