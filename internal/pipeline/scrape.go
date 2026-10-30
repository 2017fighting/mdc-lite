package pipeline

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/2017fighting/mdc-lite/internal/config"
	"github.com/2017fighting/mdc-lite/internal/field"
	"github.com/2017fighting/mdc-lite/internal/meta"
	"github.com/2017fighting/mdc-lite/internal/source"
)

// scrapeStep turns one number into field values and image bytes, by the field-level
// fallback the README describes.
//
// The scheduling is in waves. A field's candidate list is `priority[field]` when the
// pipeline wrote it (a complete replacement of the global order) and
// `scrape.with.sources` otherwise. Wave 1 asks, concurrently, the currently
// preferred source of every field still missing — deduplicated, so one source asked
// for three fields is one request. When a wave is in, every field takes its value
// from the *earliest source in its candidate list that answered and contributed*,
// never from whichever goroutine returned first. If fields are still missing, wave 2
// asks each of them their next candidate, and so on until nothing is missing or no
// candidate is left. A source is asked at most once per number: a failed source is a
// failed source, and trying it twice is the transport's business, not a parser's.
//
// Image fields are two-phase like everything else: `Scrape` returns addresses, and a
// field only counts once the bytes are in hand. Only the source that would win a
// field is asked for its image; a failed fetch means that source contributed
// nothing, so the field falls through to its next candidate exactly like a text
// field would.
//
// `scrape.with.timeout` bounds the whole number — every wave and every image — and
// is not a step failure: the sources it did not reach are recorded as failed
// (reason `timeout`) and the number ends with whatever the others gave. A source's
// own failure is never the step's failure, which is the point of the fallback.
type scrapeStep struct {
	cfg     config.ScrapeWith
	sources source.Registry
	// imageSlots is `concurrency.images`: how many image fetches this task may have
	// in flight at once. nil means uncapped; the Fetcher's process-wide request
	// semaphore still applies underneath.
	imageSlots chan struct{}
	// plans is one field's candidate list, resolved once at build time: the ids
	// this build actually has, in the order the configuration asks for them.
	plans []fieldPlan
}

// fieldPlan is one source-level field's candidate list. The list is the whole
// scheduling state for that field: position 0 is the preferred source, and the
// scheduler only ever moves forward.
type fieldPlan struct {
	name field.Name
	ids  []string
}

func newScrape(w config.ScrapeWith, reg source.Registry, images int) (Step, error) {
	plans := make([]fieldPlan, 0, len(field.All()))
	for _, name := range field.All() {
		ids := w.Sources.IDs()
		if order, ok := w.Priority[string(name)]; ok {
			// A listed field is replaced completely: the global order is not
			// appended as a tail, which is what makes "B,A" mean B then A.
			ids = order
		}
		known := make([]string, 0, len(ids))
		seen := map[string]bool{}
		for _, id := range ids {
			if _, ok := reg.Lookup(id); !ok {
				// A source this build does not have is not a candidate. That is a
				// question about the binary, not about the source's capabilities: the
				// engine never skips a source because of what it declares it can do.
				continue
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			known = append(known, id)
		}
		plans = append(plans, fieldPlan{name: name, ids: known})
	}
	step := &scrapeStep{cfg: w, sources: reg, plans: plans}
	if images > 0 {
		step.imageSlots = make(chan struct{}, images)
	}
	return step, nil
}

// acquireImage takes one of the task's image slots, waiting for one or for the
// caller's cancellation. It is the inner of the two request gates: the Fetcher then
// waits for the domain's own budget before the request leaves.
func (s *scrapeStep) acquireImage(ctx context.Context) error {
	if s.imageSlots == nil {
		return ctx.Err()
	}
	select {
	case s.imageSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *scrapeStep) releaseImage() {
	if s.imageSlots != nil {
		<-s.imageSlots
	}
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
	works := ParsedWorks(st)
	summary := &ScrapeSummary{Items: len(works), Missing: newCounts(), Results: newCounts()}
	for _, work := range works {
		scraped, attempts, err := s.scrapeOne(ctx, work)
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

// scrapedAnswer is one source's answer, kept whole: the scheduler decides a field's
// winner from these, so what matters is that the answer is the source's, not the
// order in which it arrived.
type scrapedAnswer struct {
	status string
	fields source.Fields
	err    error
	reason string
	ms     int64
}

// scrapeOne runs one number through the field-level fallback.
func (s *scrapeStep) scrapeOne(ctx context.Context, work ParsedWork) (ScrapedItem, []SourceAttempt, error) {
	number := work.Number.Normalized
	if s.cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(s.cfg.Timeout))
		defer cancel()
	}
	r := &scrapeRun{
		step:    s,
		number:  number,
		answers: map[string]scrapedAnswer{},
		// attempted is the set of (source, image field) pairs whose fetch has already
		// been tried: a source that offered a cover it could not deliver is asked
		// once, not once per wave.
		attempted: map[field.Name]map[string]bool{},
	}
	md, images := r.run(ctx)

	return ScrapedItem{Group: work.Group, Number: work.Number, Metadata: md, Images: images},
		s.attempts(r, ctx.Err() != nil), nil
}

// scrapeRun is one number's scheduler state: what each source answered, what each
// image field has already tried, and the merged metadata so far.
type scrapeRun struct {
	step      *scrapeStep
	number    string
	answers   map[string]scrapedAnswer
	attempted map[field.Name]map[string]bool

	md     meta.Metadata
	images []ImageBlob
}

func (r *scrapeRun) run(ctx context.Context) (meta.Metadata, []ImageBlob) {
	for ctx.Err() == nil {
		// Harvest first: a field may already have a winner among the answers in
		// hand, including answers of sources asked for a different field.
		r.harvest(ctx)
		wave := r.nextWave()
		if len(wave) == 0 {
			break
		}
		r.ask(ctx, wave)
	}
	// One last harvest after the loop: the wave the budget cut short may still have
	// answered some sources, and what they said is worth what it can settle. With a
	// dead context an image fetch fails at once, which is the same as not asking.
	r.harvest(ctx)
	return r.md, r.images
}

// nextWave is the deduplicated set of sources to ask next: for every field still
// missing, the first candidate nobody has asked yet. Order follows the field list,
// so the same configuration always produces the same wave.
func (r *scrapeRun) nextWave() []string {
	want := map[string]bool{}
	var wave []string
	for _, plan := range r.step.plans {
		if r.md.Has(plan.name) {
			continue
		}
		for _, id := range plan.ids {
			if _, ok := r.answers[id]; ok {
				continue
			}
			if !want[id] {
				want[id] = true
				wave = append(wave, id)
			}
			break
		}
	}
	return wave
}

// ask runs one wave: every source in it at once, because the only reason they are
// in the same wave is that they are independent of each other. Each answer is kept
// by source id; the merge does not care which one came back first.
func (r *scrapeRun) ask(ctx context.Context, wave []string) {
	type outcome struct {
		id  string
		ms  int64
		res source.Result
		err error
	}
	outcomes := make([]outcome, len(wave))
	var wg sync.WaitGroup
	for i, id := range wave {
		src, ok := r.step.sources.Lookup(id)
		if !ok {
			continue
		}
		wg.Add(1)
		go func(i int, id string, src source.Source) {
			defer wg.Done()
			started := time.Now()
			res, err := src.Scrape(ctx, r.number)
			outcomes[i] = outcome{id: id, ms: time.Since(started).Milliseconds(), res: res, err: err}
		}(i, id, src)
	}
	wg.Wait()

	for _, o := range outcomes {
		if o.id == "" {
			continue
		}
		a := scrapedAnswer{ms: o.ms}
		switch {
		case o.err != nil:
			a.status, a.err, a.reason = "failed", o.err, source.ReasonOf(o.err)
			if a.reason == "" && ctx.Err() != nil {
				// The number ran out of budget mid-request; that is a timeout even
				// though the source only saw a cancelled context.
				a.reason = source.ReasonTimeout
			}
		case !o.res.Found:
			a.status = "notfound"
		default:
			a.status, a.fields = "ok", o.res.Fields
		}
		r.answers[o.id] = a
	}
}

// harvest settles every field that has a winner among the answers in hand, fetching
// an image field's bytes the moment its candidate would win.
//
// "Would win" is positional, not temporal: the earliest candidate that answered and
// contributed. A source that answered in wave 2 but sits earlier in the list than
// the wave-1 contributor still wins, which is exactly why the answer's arrival time
// is not part of the merge.
func (r *scrapeRun) harvest(ctx context.Context) {
	for _, plan := range r.step.plans {
		if r.md.Has(plan.name) {
			continue
		}
		for _, id := range plan.ids {
			a, ok := r.answers[id]
			if !ok {
				// Nobody at or after this position has been asked either, so the
				// field waits for the next wave instead of settling for a later one.
				break
			}
			if a.status != "ok" {
				continue
			}
			if isImageField(plan.name) {
				if r.deliver(ctx, plan.name, id, a.fields) {
					break
				}
				continue
			}
			if !contributes(plan.name, a.fields) {
				continue
			}
			mergeField(&r.md, id, plan.name, a.fields)
			break
		}
	}
}

// deliver tries to turn one source's image offer into bytes in hand. It reports
// whether the field is now won by this source. An image field's contribution is its
// bytes, so a fetch that fails (or returns nothing) leaves the source contributing
// nothing for that field and the next candidate gets its chance.
func (r *scrapeRun) deliver(ctx context.Context, name field.Name, id string, f source.Fields) bool {
	if r.attempted[name] == nil {
		r.attempted[name] = map[string]bool{}
	}
	if r.attempted[name][id] {
		// This source's fetch already failed; do not ask it twice.
		return false
	}
	r.attempted[name][id] = true
	refs := imageRefs(f, name)
	if len(refs) == 0 {
		return false
	}
	src, ok := r.step.sources.Lookup(id)
	if !ok {
		return false
	}
	var kept []ImageBlob
	// One field's images are independent of each other, so they are fetched
	// concurrently within the task's `concurrency.images` budget. The results keep
	// the refs' order (Index is the order the site gave), so two runs of the same
	// number produce the same metadata.
	results := make([]*ImageBlob, len(refs))
	var wg sync.WaitGroup
	for i, ref := range refs {
		if err := r.step.acquireImage(ctx); err != nil {
			break
		}
		wg.Add(1)
		go func(i int, ref source.ImageRef) {
			defer wg.Done()
			defer r.step.releaseImage()
			data, err := src.FetchImage(ctx, ref)
			if err != nil || len(data) == 0 {
				return
			}
			results[i] = &ImageBlob{
				Role: ref.Role, Index: ref.Index, Bytes: data, Source: id, URL: ref.URL,
			}
		}(i, ref)
	}
	wg.Wait()
	for _, blob := range results {
		if blob != nil {
			kept = append(kept, *blob)
		}
	}
	if len(kept) == 0 {
		// Nothing arrived: the next candidate for this field gets its chance.
		return false
	}
	// The winning source is taken whole: every image it delivered is kept and there
	// is no cap (胜出源全取、未胜出不取、不封顶).
	if name == field.Extrafanarts {
		r.md.Extrafanarts = urlsOf(kept)
	}
	r.md.Win(name, id)
	r.images = append(r.images, kept...)
	return true
}

// mergeField writes the winning source's value of one field into the metadata. Only
// the field itself is disposed of here; the caller has already decided the winner.
func mergeField(md *meta.Metadata, id string, name field.Name, f source.Fields) {
	switch name {
	case field.Title:
		md.Title = *f.Title
	case field.Plot:
		md.Plot = *f.Plot
	case field.Releasedate:
		md.Releasedate = *f.Releasedate
	case field.Series:
		md.Series = *f.Series
	case field.Studio:
		md.Studio = *f.Studio
	case field.Publisher:
		md.Publisher = *f.Publisher
	case field.Runtime:
		md.Runtime = *f.Runtime
	case field.Rating:
		md.Rating = *f.Rating
	case field.Tags:
		md.Tags = f.Tags
	case field.Director:
		md.Director = f.Director
	case field.Actors:
		md.Actors = f.Actors
	}
	md.Win(name, id)
}

// contributes reports whether a source's answer holds a value for one field. A
// pointer is the source's way of saying "I have this" and nil its way of saying "I
// do not"; a zero rating is the one exception, because `rating` is on a 0–10 scale
// where 0 means "not rated", not "rated zero".
func contributes(name field.Name, f source.Fields) bool {
	switch name {
	case field.Title:
		return f.Title != nil
	case field.Plot:
		return f.Plot != nil
	case field.Releasedate:
		return f.Releasedate != nil
	case field.Series:
		return f.Series != nil
	case field.Studio:
		return f.Studio != nil
	case field.Publisher:
		return f.Publisher != nil
	case field.Runtime:
		return f.Runtime != nil
	case field.Rating:
		return f.Rating != nil && *f.Rating > 0
	case field.Tags:
		return len(f.Tags) > 0
	case field.Director:
		return len(f.Director) > 0
	case field.Actors:
		return len(f.Actors) > 0
	default:
		// Image fields are judged by bytes, not by the address.
		return false
	}
}

// imageRefs lists the addresses a source offered for one image field. A single-image
// field has at most one; extrafanarts is a list and keeps its order.
func imageRefs(f source.Fields, name field.Name) []source.ImageRef {
	switch name {
	case field.Poster:
		if f.Poster != nil {
			return []source.ImageRef{*f.Poster}
		}
	case field.Fanart:
		if f.Fanart != nil {
			return []source.ImageRef{*f.Fanart}
		}
	case field.Extrafanarts:
		return f.Extrafanarts
	}
	return nil
}

// attempts renders this number's source_runs rows, in configuration order.
//
// A source asked in any wave has one row with what it actually contributed. When the
// number's budget ran out, a source nobody reached gets a `failed`/`timeout` row
// instead: "we never got to it" is a fact the detail page has to be able to show. A
// source nobody asked — because no field's candidate list needed it — has no row:
// `source_runs` answers "what happened when we asked", and we did not ask.
func (s *scrapeStep) attempts(r *scrapeRun, timedOut bool) []SourceAttempt {
	out := make([]SourceAttempt, 0, len(s.cfg.Sources))
	for _, entry := range s.cfg.Sources {
		if _, ok := s.sources.Lookup(entry.ID); !ok {
			continue
		}
		a, asked := r.answers[entry.ID]
		if !asked {
			if timedOut {
				out = append(out, SourceAttempt{
					ID:     entry.ID,
					Status: "failed",
					Err:    "not reached: the number's scrape budget ran out",
					Reason: source.ReasonTimeout,
				})
			}
			continue
		}
		row := SourceAttempt{ID: entry.ID, Status: a.status, Reason: a.reason, MS: a.ms}
		if a.err != nil {
			row.Err = a.err.Error()
		}
		if a.status == "ok" {
			// What the source *won*, not what it returned: an answer whose every
			// value lost to an earlier candidate contributed nothing, and a row that
			// listed it would answer "why is this row here" with a lie. Image fields
			// are included only when their bytes arrived, because winning one is
			// exactly that.
			row.Fields = r.contributions(entry.ID)
		}
		out = append(out, row)
	}
	return out
}

// contributions lists the fields one source won, in spec order. It reads the merged
// metadata's provenance, which is the definition of "contributed": the field's value
// is this source's, and for an image field the bytes are in hand (that is the only
// way the field ever gets a winner).
func (r *scrapeRun) contributions(id string) []field.Name {
	var out []field.Name
	for _, name := range field.All() {
		if r.md.Sources[name] == id {
			out = append(out, name)
		}
	}
	return out
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
	// Reason is one of the six machine codes, read off the failure the source or the
	// Fetcher returned. It is empty for `ok` and for `notfound`: "no such work" is an
	// answer, and giving it a reason code would make it look like a failure.
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

// isImageField reports whether a source-level field's value is bytes rather than
// text, i.e. whether it only counts once FetchImage succeeded.
func isImageField(name field.Name) bool {
	for _, f := range imageFields {
		if f == name {
			return true
		}
	}
	return false
}
