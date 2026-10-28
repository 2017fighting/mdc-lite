package source

import "context"

// Capture is one raw answer, byte for byte, as `mdc source test --dump` writes it
// into the fixture directory. The bytes are what the program's own Fetcher got;
// they are never edited afterwards, because a fixture that has been touched can no
// longer falsify the selectors it exists to falsify.
type Capture struct {
	// URL is the address that answered, after redirects.
	URL string
	// Status is the HTTP status of that answer.
	Status int
	Body   []byte
}

// Dumper is implemented by a source that can hand back the page it reads, so a
// fixture comes out of the same Fetcher the product uses — the same User-Agent,
// cookie and rate limit — rather than out of curl on somebody's laptop.
//
// Dump is a second request on purpose: a Source keeps no per-call state ("goroutine
// safe"), so the raw bytes the parse just read are not left anywhere to be picked
// up afterwards.
type Dumper interface {
	Dump(ctx context.Context, number string) (Capture, error)
}

// CaptureInfo is one fixture's manifest row: where the bytes came from, what the
// site answered, how big they were, when they were captured, and their sha256 — so a
// fixture can be re-verified against the page it claims to be, long after the site
// has moved on.
//
// It is in this package because both the writer (`mdc source test --dump`) and the
// tests that verify a fixture directory need the same shape; two copies of it would
// be two chances to disagree about what a fixture is.
type CaptureInfo struct {
	Path     string `json:"path"`
	URL      string `json:"url"`
	Status   int    `json:"status"`
	Bytes    int    `json:"bytes"`
	Captured string `json:"captured"`
	SHA256   string `json:"sha256"`
}
