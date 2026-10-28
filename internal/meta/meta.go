// Package meta is the field-value side of a task: the 14 source-level fields, the
// per-field winner (`Sources`), what is still missing, and the derived values that
// the second batch will fill in.
//
// It is separate from internal/field because that package is only the vocabulary
// (the names) and it is separate from internal/source because a source's answer is
// "these fields, or nothing" rather than a complete record: merging several
// sources' answers into one Metadata is the scrape step's job, not a source's.
package meta

import "github.com/2017fighting/mdc-lite/internal/field"

// Gender is an actor's sex as the source declared it. `unknown` is an explicit
// value, never an empty string: "we were not told" and "not applicable" are
// different things and the NFO writes neither the same way.
type Gender string

// The three genders. The NFO renders female as 女优 and male as 男优; unknown
// gets no `<role>` at all.
const (
	Female  Gender = "female"
	Male    Gender = "male"
	Unknown Gender = "unknown"
)

// Actor is one performer. The dedup key is (Name, Gender); the trailing
// whitespace some sites put around a name is trimmed before it lands here.
type Actor struct {
	Name   string `json:"name"`
	Gender Gender `json:"gender"`
}

// Derived are the processed fields. Most of them belong to the second batch
// (translate / normalize); `PosterCrop` is the one exception, because a poster cut
// out of the fanart is, as far as a media server is concerned, the poster itself.
//
// The scheduler cannot reach Derived by type: it is not in `field.All()`, so no
// priority list can name it.
type Derived struct {
	TitleCN    string   `json:"title_cn"`
	PlotCN     string   `json:"plot_cn"`
	TagsCN     []string `json:"tags_cn"`
	ActorsCN   []string `json:"actors_cn"`
	PosterCrop bool     `json:"poster_crop"`
}

// Metadata is what a task knows about its work: the 14 source-level fields plus
// where each value came from and what is still missing.
//
// Image fields (poster / fanart / extrafanarts) are named here with their
// provenance (`Sources`) but their bytes live in the pipeline's image artifact:
// the ledger records files, not payloads.
type Metadata struct {
	Title        string   `json:"title"`
	Plot         string   `json:"plot"`
	Extrafanarts []string `json:"extrafanarts"`
	Tags         []string `json:"tags"`
	Releasedate  string   `json:"releasedate"`
	Actors       []Actor  `json:"actors"`
	Director     []string `json:"director"`
	Runtime      int      `json:"runtime"`
	Rating       float64  `json:"rating"`
	Series       string   `json:"series"`
	Studio       string   `json:"studio"`
	Publisher    string   `json:"publisher"`

	// Sources is the field-level winner: field → the source id that supplied it.
	// The poster crop writes "<id> (自动裁切)" here, the one derived value allowed
	// into this map (ADR-0015).
	Sources map[field.Name]string `json:"sources"`
	// Missing lists the source-level fields nothing supplied, in field order.
	Missing []field.Name `json:"missing"`
	// Partial is true when at least one field is missing.
	Partial bool `json:"partial"`

	Derived Derived `json:"derived"`
}

// Source returns the source id that won a field, or "" when nothing did.
func (m *Metadata) Source(f field.Name) string {
	if m == nil || m.Sources == nil {
		return ""
	}
	return m.Sources[f]
}

// Has reports whether a field has a value: a source won it.
func (m *Metadata) Has(f field.Name) bool {
	return m != nil && m.Sources[f] != ""
}

// Win records that a source won a field. It stores provenance, not the value: the
// caller (the scrape step, or the poster crop) has already put the value where the
// field keeps it, because an image field's value is bytes rather than a string.
// Multi-valued fields are won whole, never appended to.
func (m *Metadata) Win(f field.Name, sourceID string) {
	if m.Sources == nil {
		m.Sources = map[field.Name]string{}
	}
	m.Sources[f] = sourceID
	m.refresh()
}

// Forget drops a field: used when an image's bytes never arrived, which makes the
// field unsatisfied even though a source offered a URL for it.
func (m *Metadata) Forget(f field.Name) {
	delete(m.Sources, f)
	m.refresh()
}

// refresh recomputes Missing and Partial from the field set, in spec order, so two
// runs over the same data produce the same list.
func (m *Metadata) refresh() {
	m.Missing = m.Missing[:0]
	for _, f := range field.All() {
		if !m.Has(f) {
			m.Missing = append(m.Missing, f)
		}
	}
	m.Partial = len(m.Missing) > 0
}

// MissingSet is a lookup view of Missing.
func (m *Metadata) MissingSet() map[field.Name]bool {
	out := make(map[field.Name]bool, len(m.Missing))
	for _, f := range m.Missing {
		out[f] = true
	}
	return out
}
