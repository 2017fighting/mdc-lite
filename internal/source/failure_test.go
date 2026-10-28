package source

import (
	"errors"
	"testing"
)

// coded is a non-source error that knows a reason code, like the transport's.
type coded struct{ code string }

func (c *coded) Error() string  { return "transport failed" }
func (c *coded) Reason() string { return c.code }

func TestReasonOfReadsTheCodeOffAnyReasonedError(t *testing.T) {
	// The transport's error and a source's Failure both answer Reason(), so the
	// engine reads a code without importing the transport.
	if got := ReasonOf(&coded{code: ReasonCooldown}); got != ReasonCooldown {
		t.Errorf("transport error = %q, want %q", got, ReasonCooldown)
	}
	if got := ReasonOf(Fail(ReasonParse, errors.New("no title"))); got != ReasonParse {
		t.Errorf("source failure = %q, want %q", got, ReasonParse)
	}
	// Wrapped, which is what a source's error looks like after fmt.Errorf("%w").
	wrapped := errors.Join(errors.New("while scraping"), Fail(ReasonBlocked, nil))
	if got := ReasonOf(wrapped); got != ReasonBlocked {
		t.Errorf("wrapped failure = %q, want %q", got, ReasonBlocked)
	}
}

func TestReasonOfLeavesAnUnclassifiedErrorUnclassified(t *testing.T) {
	// A made-up code in `source_runs.reason` is a diagnosis stored forever, so an
	// error nobody classified stays empty.
	if got := ReasonOf(errors.New("something else")); got != "" {
		t.Errorf("plain error = %q, want empty", got)
	}
	if got := ReasonOf(&coded{code: "made_up"}); got != "" {
		t.Errorf("unknown code = %q, want empty", got)
	}
	if got := ReasonOf(nil); got != "" {
		t.Errorf("nil = %q", got)
	}
}

func TestFailureKeepsItsCause(t *testing.T) {
	cause := errors.New("the page answered 403")
	f := Fail(ReasonCooldown, cause)
	if !errors.Is(f, cause) {
		t.Error("the Failure does not unwrap to its cause")
	}
	if f.Error() != cause.Error() {
		t.Errorf("Error() = %q, want the cause's text (the code is a column of its own)", f.Error())
	}
	if bare := Fail(ReasonTimeout, nil); bare.Error() == "" {
		t.Error("a Failure without a cause printed nothing")
	}
}

func TestReasonSetIsTheSixCodes(t *testing.T) {
	want := []string{"egress_unavailable", "timeout", "blocked", "cooldown", "http_5xx", "parse"}
	got := Reasons()
	if len(got) != len(want) {
		t.Fatalf("Reasons() = %v", got)
	}
	for i, code := range want {
		if got[i] != code || !ValidReason(code) {
			t.Errorf("Reasons()[%d] = %q, want %q", i, got[i], code)
		}
	}
	if ValidReason("notfound") {
		t.Error("notfound must not be a reason code")
	}
}
