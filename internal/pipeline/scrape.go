package pipeline

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/2017fighting/mdc-lite/internal/config"
	"github.com/2017fighting/mdc-lite/internal/field"
	"github.com/2017fighting/mdc-lite/internal/meta"
	"github.com/2017fighting/mdc-lite/internal/source"
)

// scrapeStep turns one number into field values and image bytes.
//
// The scheduling is deliberately the simplest thing that satisfies the contract:
// the sources run in `scrape.with.sources` order, a field is won by the first
// source that contributes it, and an image field only counts once its bytes are in
// hand — a source that offers a URL it cannot deliver has contributed nothing, and
// the next source that offers the field gets its chance. The wave scheduling and
// `priority[field]` of the field-level fallback land on top of this seam without
// changing the artifact.
type scrapeStep struct {
	cfg     config.ScrapeWith
	sources source.Registry
}

func newScrape(w config.ScrapeWith, reg source.Registry) (Step, error) {
	return &scrapeStep{cfg: w, sources: reg}, nil
}

func (s *scrapeStep) Type() string { return config.StepScrape }

func (s *scrapeStep) Name() string { return "" }

func (s *scrapeStep) Requires() []string { return []string{config.ArtifactParsed} }

func (s *scrapeStep) Produces() []string { return []string{config.ArtifactScraped} }

func (s *scrapeStep) Run(ctx context.Context, st *State) error {
	if st.Parsed == nil {
		return fmt.Errorf("no Parsed artifact to scrape")
	}
	out := &Scraped{}
	summary := &ScrapeSummary{Items: len(st.Parsed.Items), Missing: newCounts(), Results: newCounts()}
	for _, item := range st.Parsed.Items {
		scraped, attempts, err := s.scrapeOne(ctx, item)
		if err != nil {
			return err
		}
		summary.Attempts = append(summary.Attempts, attempts...)
		for _, a := range attempts {
			summary.Results[a.ID+":"+a.Status]++
			if a.Status == "failed" {
				summary.Failed++
			}
			if a.Status == "notfound" {
				summary.NotFound++
			}
		}
		for _, f := range scraped.Metadata.Missing {
			summary.Missing[string(f)]++
		}
		summary.Images += len(scraped.Images)
		out.Items = append(out.Items, scraped)
	}
	st.Scraped = out
	st.ScrapeRun = summary
	return nil
}

// contribution is one source's answer, kept so the image pass can retry a field a
// later source may deliver.
type contribution struct {
	id     string
	fields source.Fields
}

func (s *scrapeStep) scrapeOne(ctx context.Context, item ParsedGroup) (ScrapedItem, []SourceAttempt, error) {
	number := item.Number.Normalized
	md := meta.Metadata{}
	var (
		attempts []SourceAttempt
		contribs []contribution
	)
	for _, entry := range s.cfg.Sources {
		src, ok := s.sources.Lookup(entry.ID)
		if !ok {
			return ScrapedItem{}, nil, fmt.Errorf("scrape: source %q is not built into this binary (known: %s)", entry.ID, s.sources.Known())
		}
		started := time.Now()
		res, err := src.Scrape(ctx, number)
		a := SourceAttempt{ID: entry.ID, MS: time.Since(started).Milliseconds()}
		switch {
		case err != nil:
			a.Status, a.Err = "failed", err.Error()
			attempts = append(attempts, a)
			continue
		case !res.Found:
			a.Status = "notfound"
			attempts = append(attempts, a)
			continue
		}
		a.Status = "ok"
		a.Fields = res.Fields.Names()
		attempts = append(attempts, a)
		contribs = append(contribs, contribution{id: entry.ID, fields: res.Fields})
		mergeFields(&md, entry.ID, res.Fields)
	}

	images := s.fetchImages(ctx, contribs, &md)
	return ScrapedItem{Group: item.Group, Number: item.Number, Metadata: md, Images: images}, attempts, nil
}

// mergeFields takes the first contribution of each scalar field. Multi-valued
// fields are taken whole, from one source: mixing two sources' tag lists would make
// the field's provenance a lie.
func mergeFields(md *meta.Metadata, id string, f source.Fields) {
	take := func(name field.Name, ok bool, set func()) {
		if ok && !md.Has(name) {
			set()
			md.Win(name, id)
		}
	}
	take(field.Title, f.Title != nil, func() { md.Title = *f.Title })
	take(field.Plot, f.Plot != nil, func() { md.Plot = *f.Plot })
	take(field.Releasedate, f.Releasedate != nil, func() { md.Releasedate = *f.Releasedate })
	take(field.Series, f.Series != nil, func() { md.Series = *f.Series })
	take(field.Studio, f.Studio != nil, func() { md.Studio = *f.Studio })
	take(field.Publisher, f.Publisher != nil, func() { md.Publisher = *f.Publisher })
	take(field.Runtime, f.Runtime != nil, func() { md.Runtime = *f.Runtime })
	take(field.Rating, f.Rating != nil, func() { md.Rating = *f.Rating })
	take(field.Tags, len(f.Tags) > 0, func() { md.Tags = f.Tags })
	take(field.Director, len(f.Director) > 0, func() { md.Director = f.Director })
	take(field.Actors, len(f.Actors) > 0, func() { md.Actors = f.Actors })
	// Images are not marked here: their exit is the bytes arriving, which happens
	// in fetchImages.
}

// fetchImages fetches the image fields, in source order, and only for the field
// that source offered. A failed fetch makes that source's contribution vanish and
// the next one is tried.
func (s *scrapeStep) fetchImages(ctx context.Context, contribs []contribution, md *meta.Metadata) []ImageBlob {
	var out []ImageBlob
	for _, name := range []field.Name{field.Poster, field.Fanart} {
		for _, c := range contribs {
			ref := singleRef(c.fields, name)
			if ref == nil {
				continue
			}
			src, ok := s.sources.Lookup(c.id)
			if !ok {
				continue
			}
			data, err := src.FetchImage(ctx, *ref)
			if err != nil || len(data) == 0 {
				continue // contributed nothing: the next source may not fail
			}
			md.Win(name, c.id)
			out = append(out, ImageBlob{
				Role:   ref.Role,
				Index:  ref.Index,
				Bytes:  data,
				Source: c.id,
				URL:    ref.URL,
			})
			// The image's extension is sniffed by the engine, not the source, and it
			// is named later by `images`; keep the raw bytes here.
			break
		}
	}
	// extrafanarts is one field built of many images: the first source that
	// delivers at least one of them wins the whole field, and every image it
	// delivered is kept ("胜出源全取、不封顶").
	for _, c := range contribs {
		if len(c.fields.Extrafanarts) == 0 {
			continue
		}
		src, ok := s.sources.Lookup(c.id)
		if !ok {
			continue
		}
		var kept []ImageBlob
		for _, ref := range c.fields.Extrafanarts {
			data, err := src.FetchImage(ctx, ref)
			if err != nil || len(data) == 0 {
				continue
			}
			kept = append(kept, ImageBlob{
				Role:   ref.Role,
				Index:  ref.Index,
				Bytes:  data,
				Source: c.id,
				URL:    ref.URL,
			})
		}
		if len(kept) > 0 {
			md.Win(field.Extrafanarts, c.id)
			md.Extrafanarts = urlsOf(kept)
			out = append(out, kept...)
			break
		}
	}
	return out
}

func singleRef(f source.Fields, name field.Name) *source.ImageRef {
	switch name {
	case field.Poster:
		return f.Poster
	case field.Fanart:
		return f.Fanart
	default:
		return nil
	}
}

func urlsOf(blobs []ImageBlob) []string {
	out := make([]string, len(blobs))
	for i, b := range blobs {
		out[i] = b.URL
	}
	return out
}

// SourceAttempt is one source's try, kept for `source_runs`: which source, how it
// ended, and which fields it actually contributed.
type SourceAttempt struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Err    string `json:"err,omitempty"`
	// Reason is one of the six machine codes. It is the transport's verdict (a
	// cooldown, a 5xx, a blocked page), so this step leaves it empty: the source
	// reports the failure, the Fetcher classifies it.
	Reason string       `json:"reason,omitempty"`
	Fields []field.Name `json:"fields,omitempty"`
	MS     int64        `json:"ms"`
}

// ScrapeSummary is the scrape step's summary projection: counts only. The per-source
// detail lands in `source_runs`, which is what the detail page reads.
type ScrapeSummary struct {
	Items    int            `json:"items"`
	Failed   int            `json:"failed"`
	NotFound int            `json:"notfound"`
	Images   int            `json:"images"`
	Missing  map[string]int `json:"missing"`
	// Results counts by "id:status", so the summary answers "how did javdb do"
	// without repeating source_runs.
	Results map[string]int `json:"results"`
	// Attempts is not part of the JSON projection: it is what the caller writes to
	// source_runs.
	Attempts []SourceAttempt `json:"-"`
}

// String renders the scrape summary as one line.
func (s ScrapeSummary) String() string {
	parts := []string{
		plural(s.Items, "number", "numbers"),
		fmt.Sprintf("%d images", s.Images),
	}
	if s.Failed > 0 {
		parts = append(parts, fmt.Sprintf("%d source(s) failed", s.Failed))
	}
	if s.NotFound > 0 {
		parts = append(parts, fmt.Sprintf("%d source(s) had no such work", s.NotFound))
	}
	for _, f := range field.All() {
		if s.Missing[string(f)] > 0 {
			parts = append(parts, fmt.Sprintf("missing %s ×%d", f, s.Missing[string(f)]))
		}
	}
	return strings.Join(parts, ", ")
}
