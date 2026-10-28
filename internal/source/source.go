// Package source declares the scraping seam: what one site answers for one number,
// and what the engine may ask it for afterwards.
//
// The interface is deliberately two-phase. `Scrape` answers with field values and
// with *addresses* for the images; whether those images are worth fetching is
// decided by the engine, which only fetches the fields that won. A source never
// decides its own priority, and it never retries: retries, rate limits, cooldowns
// and proxies belong to the transport (the Fetcher), not to four parsers.
package source

import (
	"context"
	"sort"
	"strings"

	"github.com/2017fighting/mdc-lite/internal/field"
	"github.com/2017fighting/mdc-lite/internal/meta"
)

// ImageRef is an image's address plus the headers one has to send to get it. It
// is not the bytes: a source that offered a URL has contributed nothing until the
// engine fetches it.
type ImageRef struct {
	// Role is the image field this URL belongs to: poster, fanart or extrafanarts.
	Role  field.Name
	Index int
	URL   string
	// Headers are the request headers this particular image needs (javbus wants a
	// Referer). They are code constants inside the source, not configuration.
	Headers map[string]string
}

// Fields is one source's answer about the 14 source-level fields.
//
// A scalar is a pointer and a list is a slice, so "the source said nothing" (nil)
// stays distinct from "the source said zero" (a pointer to "" or 0 — which is a
// real contribution for `rating`, where 0 is nevertheless treated as unsatisfied
// by the source's own normalisation).
type Fields struct {
	Title       *string
	Plot        *string
	Releasedate *string
	Series      *string
	Studio      *string
	Publisher   *string
	Runtime     *int
	Rating      *float64

	Tags     []string
	Director []string
	Actors   []meta.Actor

	// Image fields are addresses, not bytes. `extrafanarts` is a list: one source
	// may offer several stills, and the engine takes them all from the winner.
	Poster       *ImageRef
	Fanart       *ImageRef
	Extrafanarts []ImageRef
}

// Empty reports whether the source contributed no field at all.
func (f Fields) Empty() bool {
	return f.Title == nil && f.Plot == nil && f.Releasedate == nil && f.Series == nil &&
		f.Studio == nil && f.Publisher == nil && f.Runtime == nil && f.Rating == nil &&
		len(f.Tags) == 0 && len(f.Director) == 0 && len(f.Actors) == 0 &&
		f.Poster == nil && f.Fanart == nil && len(f.Extrafanarts) == 0
}

// Names lists the fields a Fields value actually contributes, in spec order.
func (f Fields) Names() []field.Name {
	var out []field.Name
	add := func(name field.Name, ok bool) {
		if ok {
			out = append(out, name)
		}
	}
	add(field.Title, f.Title != nil)
	add(field.Plot, f.Plot != nil)
	add(field.Fanart, f.Fanart != nil)
	add(field.Extrafanarts, len(f.Extrafanarts) > 0)
	add(field.Tags, len(f.Tags) > 0)
	add(field.Releasedate, f.Releasedate != nil)
	add(field.Actors, len(f.Actors) > 0)
	add(field.Poster, f.Poster != nil)
	add(field.Director, len(f.Director) > 0)
	add(field.Runtime, f.Runtime != nil)
	add(field.Rating, f.Rating != nil)
	add(field.Series, f.Series != nil)
	add(field.Studio, f.Studio != nil)
	add(field.Publisher, f.Publisher != nil)
	return out
}

// Result is one source's three-state answer: `err != nil` is failed,
// `err == nil && !Found` is notfound, and `Found` with any subset of fields is ok.
type Result struct {
	Found  bool
	Fields Fields
}

// Source is one site. An implementation must be safe for concurrent use: one
// instance serves every task at once, so it may keep no per-call state.
type Source interface {
	// ID is the name configuration refers to it by.
	ID() string
	// Fields is the static capability declaration: which of the 14 fields this
	// source may ever contribute. It is checked at startup and shown to the
	// operator; the engine does not skip a source at run time because of it.
	Fields() []field.Name
	// Scrape asks one site about one normalized number.
	Scrape(ctx context.Context, number string) (Result, error)
	// FetchImage fetches one image the source offered. The decoder stays inside the
	// source (javdb's XOR, dmm's CDN); the engine only sniffs the magic bytes.
	FetchImage(ctx context.Context, ref ImageRef) ([]byte, error)
}

// Registry is the set of built-in sources, keyed by id. There is no plugin
// mechanism: a source is a Go type compiled into the binary.
type Registry map[string]Source

// NewRegistry builds a registry from the given sources.
func NewRegistry(sources ...Source) Registry {
	out := make(Registry, len(sources))
	for _, s := range sources {
		out[s.ID()] = s
	}
	return out
}

// Known is the registry's ids as one sorted, comma-separated string, for a message
// that names what the binary does know when configuration names something it does
// not.
func (r Registry) Known() string {
	out := make([]string, 0, len(r))
	for id := range r {
		out = append(out, id)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// Lookup returns the source with this id.
func (r Registry) Lookup(id string) (Source, bool) {
	s, ok := r[id]
	return s, ok
}
