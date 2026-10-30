package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/2017fighting/mdc-lite/internal/config"
	"github.com/2017fighting/mdc-lite/internal/field"
	"github.com/2017fighting/mdc-lite/internal/meta"
	"github.com/2017fighting/mdc-lite/internal/source"
)

// scrapeState is the smallest artifact a scrape run needs: one parsed candidate.
func scrapeState(number string) *State {
	return &State{Parsed: &Parsed{Items: []ParsedCandidate{{
		File:   FileRef{Path: "/media/a/" + number + ".mp4", Ext: ".mp4"},
		Stem:   number,
		Number: Number{Raw: number, Normalized: number},
	}}}}
}

func scrapeCfg(ids ...string) config.ScrapeWith {
	order := make(config.SourceOrder, len(ids))
	for i, id := range ids {
		order[i] = config.SourceEntry{ID: id}
	}
	return config.ScrapeWith{Sources: order, Timeout: config.Duration(time.Minute)}
}

func runScrape(t *testing.T, cfg config.ScrapeWith, sources ...source.Source) (*State, error) {
	t.Helper()
	step, err := newScrape(cfg, source.NewRegistry(sources...), 0)
	if err != nil {
		t.Fatal(err)
	}
	st := scrapeState("SSIS-001")
	if err := step.Run(context.Background(), st); err != nil {
		return st, err
	}
	if st.ScrapeRun == nil || len(st.ScrapeRun.Attempts) == 0 {
		t.Fatal("the scrape step recorded no source attempts")
	}
	return st, nil
}

func TestScrapeRecordsTheReasonCodeOfAFailedSource(t *testing.T) {
	// A source's failure is not the step's failure: the run goes on and the reason
	// lands in source_runs, where the detail page reads it.
	src := newFakeSource()
	src.scrapeErr = source.Fail(source.ReasonBlocked, errors.New("javbus: the age gate answered"))

	st, err := runScrape(t, scrapeCfg("fixture"), src)
	if err != nil {
		t.Fatalf("a source failure must not fail the step: %v", err)
	}
	a := st.ScrapeRun.Attempts[0]
	if a.Status != "failed" || a.Reason != source.ReasonBlocked {
		t.Errorf("attempt = %+v, want failed with reason %q", a, source.ReasonBlocked)
	}
	if len(a.Fields) != 0 {
		t.Errorf("a failed source contributed fields: %v", a.Fields)
	}
	if st.ScrapeRun.Failed != 1 {
		t.Errorf("the summary counted %d failures, want 1", st.ScrapeRun.Failed)
	}
}

func TestScrapeRecordsNotfoundWithoutAReasonCode(t *testing.T) {
	src := newFakeSource()
	src.found = false

	st, err := runScrape(t, scrapeCfg("fixture"), src)
	if err != nil {
		t.Fatal(err)
	}
	a := st.ScrapeRun.Attempts[0]
	if a.Status != "notfound" {
		t.Errorf("status = %q, want notfound", a.Status)
	}
	if a.Reason != "" {
		t.Errorf("notfound carries reason %q: \"no such work\" is an answer, not a failure", a.Reason)
	}
}

func TestScrapeDropsAnImageFieldWhoseBytesNeverArrived(t *testing.T) {
	// The `fields` column answers "which fields did this source actually
	// contribute". An image is contributed when its bytes are in hand, so a cover
	// URL that 403s is no field at all.
	src := newFakeSource()
	title := "a title"
	src.fields = source.Fields{
		Title:  &title,
		Fanart: &source.ImageRef{Role: field.Fanart, URL: "https://site/cover.jpg"},
	}
	src.fetchErr["https://site/cover.jpg"] = true

	st, err := runScrape(t, scrapeCfg("fixture"), src)
	if err != nil {
		t.Fatal(err)
	}
	a := st.ScrapeRun.Attempts[0]
	if got := names(a.Fields); got != "title" {
		t.Errorf("fields = %s, want only the text field", got)
	}
	md := st.Scraped.Items[0].Metadata
	if md.Source(field.Fanart) != "" {
		t.Errorf("fanart has a source (%q) while its bytes never arrived", md.Source(field.Fanart))
	}
	if n := len(st.Scraped.Items[0].Images); n != 0 {
		t.Errorf("the engine kept %d image bytes from a source that delivered none", n)
	}
}

func TestScrapeKeepsAnImageFieldOnceTheBytesAreInHand(t *testing.T) {
	src := newFakeSource()
	src.fields = source.Fields{
		Fanart: &source.ImageRef{Role: field.Fanart, URL: "https://site/cover.jpg"},
	}
	src.images["https://site/cover.jpg"] = []byte("\xff\xd8\xffcover")

	st, err := runScrape(t, scrapeCfg("fixture"), src)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(st.ScrapeRun.Attempts[0].Fields); got != "fanart" {
		t.Errorf("fields = %s, want fanart", got)
	}
	if st.Scraped.Items[0].Metadata.Source(field.Fanart) != "fixture" {
		t.Error("fanart was delivered but has no winning source")
	}
}

// slowSource holds its answer until the context is done: it is how a stalled
// source looks to the step.
type slowSource struct{ id string }

func (s *slowSource) ID() string           { return s.id }
func (s *slowSource) Fields() []field.Name { return field.All() }
func (s *slowSource) FetchImage(context.Context, source.ImageRef) ([]byte, error) {
	return nil, errors.New("slowSource has no images")
}
func (s *slowSource) Scrape(ctx context.Context, _ string) (source.Result, error) {
	<-ctx.Done()
	return source.Result{}, ctx.Err()
}

func TestScrapeBudgetRecordsTheSourcesItNeverReached(t *testing.T) {
	stalled := &slowSource{id: "stalled"}
	next := newFakeSource()
	next.id = "next"

	cfg := scrapeCfg("stalled", "next")
	cfg.Timeout = config.Duration(20 * time.Millisecond)

	st, err := runScrape(t, cfg, stalled, next)
	if err != nil {
		t.Fatalf("a spent budget is not a step failure: %v", err)
	}
	attempts := st.ScrapeRun.Attempts
	if len(attempts) != 2 {
		t.Fatalf("attempts = %+v, want both sources recorded", attempts)
	}
	if attempts[0].Status != "failed" || attempts[0].Reason != source.ReasonTimeout {
		t.Errorf("the stalled source = %+v, want failed/timeout", attempts[0])
	}
	if attempts[1].Status != "failed" || attempts[1].Reason != source.ReasonTimeout {
		t.Errorf("the source that was never asked = %+v, want failed/timeout", attempts[1])
	}
	if next.scrapes != 0 {
		t.Error("a source was asked after the number's budget ran out")
	}
}

func names(fields []field.Name) string {
	out := ""
	for i, f := range fields {
		if i > 0 {
			out += ","
		}
		out += string(f)
	}
	return out
}

func TestScrapeSkipsASourceThisBinaryDoesNotHave(t *testing.T) {
	// The factory order lists javdb, dmm, javbus and fc2, and this build has only
	// some of them. A missing source must not stop the ones that exist: the ticket's
	// whole point is that javbus's fields and images land on disk today.
	src := newFakeSource()
	st, err := runScrape(t, scrapeCfg("javdb", "fixture"), src)
	if err != nil {
		t.Fatalf("an unbuilt source failed the step: %v", err)
	}
	attempts := st.ScrapeRun.Attempts
	if len(attempts) != 1 || attempts[0].ID != "fixture" {
		t.Fatalf("attempts = %+v, want only the source this binary has", attempts)
	}
	if attempts[0].Status != "ok" {
		t.Errorf("the built source did not answer: %+v", attempts[0])
	}
}

// waveSource is the scheduler's test double: it answers what the test set up, counts
// how often it was asked, and can hold its answer long enough for a concurrency
// assertion to see it in flight.
type waveSource struct {
	id       string
	declared []field.Name
	fields   source.Fields
	found    bool
	err      error
	delay    time.Duration

	mu      sync.Mutex
	scrapes int
	fetched []string

	probe *probe
}

func newWaveSource(id string) *waveSource {
	return &waveSource{id: id, declared: field.All(), found: true}
}

func (s *waveSource) ID() string           { return s.id }
func (s *waveSource) Fields() []field.Name { return s.declared }

func (s *waveSource) Scrape(ctx context.Context, _ string) (source.Result, error) {
	s.mu.Lock()
	s.scrapes++
	s.mu.Unlock()
	if s.probe != nil {
		s.probe.enter()
		defer s.probe.leave()
	}
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return source.Result{}, ctx.Err()
		}
	}
	if s.err != nil {
		return source.Result{}, s.err
	}
	return source.Result{Found: s.found, Fields: s.fields}, nil
}

func (s *waveSource) FetchImage(_ context.Context, ref source.ImageRef) ([]byte, error) {
	s.mu.Lock()
	s.fetched = append(s.fetched, ref.URL)
	s.mu.Unlock()
	if strings.Contains(ref.URL, "broken") {
		return nil, errors.New("waveSource: the image is a 403 page")
	}
	return []byte("\xff\xd8\xff" + ref.URL), nil
}

func (s *waveSource) asked() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scrapes
}

func (s *waveSource) fetchedURLs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.fetched...)
}

// probe measures how many scrapes are in flight at once. The peak is what proves a
// wave is concurrent: a counter that only ever reaches 1 is a loop.
type probe struct {
	inflight atomic.Int32
	peak     atomic.Int32
}

func (p *probe) enter() {
	n := p.inflight.Add(1)
	for {
		old := p.peak.Load()
		if n <= old || p.peak.CompareAndSwap(old, n) {
			return
		}
	}
}

func (p *probe) leave() { p.inflight.Add(-1) }

// oneField sets exactly one field's value on a source.
func oneField(src *waveSource, name field.Name, value string) {
	switch name {
	case field.Title:
		src.fields.Title = &value
	case field.Plot:
		src.fields.Plot = &value
	case field.Studio:
		src.fields.Studio = &value
	case field.Series:
		src.fields.Series = &value
	case field.Publisher:
		src.fields.Publisher = &value
	case field.Releasedate:
		src.fields.Releasedate = &value
	case field.Tags:
		src.fields.Tags = []string{value}
	case field.Director:
		src.fields.Director = []string{value}
	}
}

// withPriority adds a per-field order to the step's settings.
func withPriority(cfg config.ScrapeWith, name field.Name, ids ...string) config.ScrapeWith {
	if cfg.Priority == nil {
		cfg.Priority = map[string][]string{}
	}
	cfg.Priority[string(name)] = ids
	return cfg
}

func TestScrapeSendsOneWavePerFieldOrderAndAsksEachSourceOnce(t *testing.T) {
	// Two fields whose first candidate differs are two goroutines in wave 1; the
	// third source is nobody's first candidate, so it waits for wave 2 — and is asked
	// exactly once even though it fronts several of the fields that are still empty.
	a, b, c := newWaveSource("a"), newWaveSource("b"), newWaveSource("c")
	oneField(a, field.Title, "from a")
	oneField(b, field.Plot, "from b")
	oneField(c, field.Tags, "from c")
	p := &probe{}
	a.probe, b.probe = p, p
	// Both members of the wave hold their answer long enough for the probe to see
	// them in flight together; without the delay a trivial answer can finish before
	// the next goroutine starts, which would prove nothing either way.
	a.delay, b.delay = 30*time.Millisecond, 30*time.Millisecond

	cfg := scrapeCfg("a", "b", "c")
	cfg = withPriority(cfg, field.Title, "a")
	cfg = withPriority(cfg, field.Plot, "b")

	st, err := runScrape(t, cfg, a, b, c)
	if err != nil {
		t.Fatal(err)
	}
	if p.peak.Load() < 2 {
		t.Errorf("peak in-flight scrapes = %d, want the wave's sources to run at once", p.peak.Load())
	}
	for _, src := range []*waveSource{a, b, c} {
		if got := src.asked(); got != 1 {
			t.Errorf("source %s was asked %d times; a source is asked once per number", src.id, got)
		}
	}
	md := st.Scraped.Items[0].Metadata
	if md.Source(field.Title) != "a" || md.Source(field.Plot) != "b" || md.Source(field.Tags) != "c" {
		t.Errorf("sources = title:%q plot:%q tags:%q", md.Source(field.Title), md.Source(field.Plot), md.Source(field.Tags))
	}
}

func TestScrapeTakesTheEarliestCandidateNotTheFastestAnswer(t *testing.T) {
	// The slow source is first in the field's order; the fast one answers first. The
	// priority is positional, so the fast answer must not win.
	slow, fast := newWaveSource("slow"), newWaveSource("fast")
	oneField(slow, field.Title, "slow title")
	oneField(fast, field.Title, "fast title")
	slow.delay = 80 * time.Millisecond

	cfg := scrapeCfg("fast", "slow")
	cfg = withPriority(cfg, field.Title, "slow", "fast")

	st, err := runScrape(t, cfg, slow, fast)
	if err != nil {
		t.Fatal(err)
	}
	md := st.Scraped.Items[0].Metadata
	if md.Source(field.Title) != "slow" || md.Title != "slow title" {
		t.Errorf("title = %q from %q, want the earlier candidate's value", md.Title, md.Source(field.Title))
	}
}

func TestScrapeFallsThroughToTheNextCandidateWhenOneSourceFails(t *testing.T) {
	// One site being down only costs its own fields: the field moves to its next
	// candidate, and the step still succeeds.
	broken, healthy := newWaveSource("broken"), newWaveSource("healthy")
	broken.err = source.Fail(source.ReasonBlocked, errors.New("blocked"))
	oneField(healthy, field.Title, "healthy title")

	cfg := scrapeCfg("broken", "healthy")
	st, err := runScrape(t, cfg, broken, healthy)
	if err != nil {
		t.Fatalf("a source failure must not fail the step: %v", err)
	}
	md := st.Scraped.Items[0].Metadata
	if md.Source(field.Title) != "healthy" {
		t.Errorf("title source = %q, want the fallback", md.Source(field.Title))
	}
	attempts := st.ScrapeRun.Attempts
	if len(attempts) != 2 || attempts[0].Status != "failed" || attempts[1].Status != "ok" {
		t.Fatalf("attempts = %+v, want the failed source and then the fallback", attempts)
	}
}

func TestScrapeReadsAPriorityListAsACompleteReplacement(t *testing.T) {
	// A listed field replaces the global order wholesale, so the source that is the
	// global first choice is never asked for it — and when every field names the same
	// source, the global order's sources are not asked at all.
	a, b := newWaveSource("a"), newWaveSource("b")
	oneField(b, field.Title, "from b")
	oneField(b, field.Tags, "from b")

	cfg := scrapeCfg("a", "b")
	for _, name := range field.All() {
		cfg = withPriority(cfg, name, "b")
	}
	st, err := runScrape(t, cfg, a, b)
	if err != nil {
		t.Fatal(err)
	}
	if a.asked() != 0 {
		t.Errorf("a was asked %d times, but no field's order names it", a.asked())
	}
	if got := st.Scraped.Items[0].Metadata.Source(field.Title); got != "b" {
		t.Errorf("title source = %q, want b", got)
	}
}

func TestScrapeFallsBackToTheGlobalOrderForAnUnlistedField(t *testing.T) {
	a, b := newWaveSource("a"), newWaveSource("b")
	oneField(a, field.Tags, "a tags")
	oneField(b, field.Title, "b title")

	cfg := scrapeCfg("a", "b")
	cfg = withPriority(cfg, field.Title, "b")
	st, err := runScrape(t, cfg, a, b)
	if err != nil {
		t.Fatal(err)
	}
	md := st.Scraped.Items[0].Metadata
	if md.Source(field.Title) != "b" {
		t.Errorf("title source = %q, want the listed field's order", md.Source(field.Title))
	}
	if md.Source(field.Tags) != "a" {
		t.Errorf("tags source = %q, want the global order's first candidate", md.Source(field.Tags))
	}
}

func TestScrapeStopsWhenNothingIsMissing(t *testing.T) {
	// The first source answers every field, so the second is never asked: "no gap →
	// stop" is the whole point of the waves.
	full, spare := newWaveSource("full"), newWaveSource("spare")
	for _, name := range field.All() {
		switch name {
		case field.Fanart, field.Poster, field.Extrafanarts:
			continue
		}
		oneField(full, name, "value")
	}
	title := "value"
	runtime, rating := 120, 8.0
	full.fields.Title = &title
	full.fields.Runtime = &runtime
	full.fields.Rating = &rating
	full.fields.Actors = []meta.Actor{{Name: "an actress", Gender: meta.Female}}
	full.fields.Fanart = &source.ImageRef{Role: field.Fanart, URL: "https://site/fanart.jpg"}
	full.fields.Poster = &source.ImageRef{Role: field.Poster, URL: "https://site/poster.jpg"}
	full.fields.Extrafanarts = []source.ImageRef{{Role: field.Extrafanarts, Index: 0, URL: "https://site/extra0.jpg"}}

	cfg := scrapeCfg("full", "spare")
	st, err := runScrape(t, cfg, full, spare)
	if err != nil {
		t.Fatal(err)
	}
	if spare.asked() != 0 {
		t.Errorf("the spare source was asked %d times, but the first source left no gap", spare.asked())
	}
	if md := st.Scraped.Items[0].Metadata; md.Partial {
		t.Errorf("the number is partial (%v) although the first source supplied all 14 fields", md.Missing)
	}
}

func TestScrapeFetchesOnlyTheWinningSourcesImages(t *testing.T) {
	// a wins fanart with its bytes; b is asked later for a field a did not supply,
	// and its own fanart offer must not be fetched: "胜出源全取、未胜出不取".
	a, b := newWaveSource("a"), newWaveSource("b")
	oneField(a, field.Title, "a title")
	a.fields.Fanart = &source.ImageRef{Role: field.Fanart, URL: "https://a/fanart.jpg"}
	b.fields.Fanart = &source.ImageRef{Role: field.Fanart, URL: "https://b/fanart.jpg"}
	oneField(b, field.Plot, "b plot")

	cfg := scrapeCfg("a", "b")
	st, err := runScrape(t, cfg, a, b)
	if err != nil {
		t.Fatal(err)
	}
	md := st.Scraped.Items[0].Metadata
	if md.Source(field.Fanart) != "a" {
		t.Errorf("fanart source = %q, want the winning source", md.Source(field.Fanart))
	}
	if got := strings.Join(b.fetchedURLs(), ","); got != "" {
		t.Errorf("b's images were fetched (%s) although it did not win the field", got)
	}
	if n := len(st.Scraped.Items[0].Images); n != 1 {
		t.Errorf("kept %d images, want one fanart from the winner", n)
	}
}

func TestScrapeFallsThroughWhenTheWinningSourcesImageNeverArrives(t *testing.T) {
	// The bytes are the contribution: a 403 page from the first source means that
	// source gave nothing for the field, so the next candidate is asked.
	a, b := newWaveSource("a"), newWaveSource("b")
	oneField(a, field.Title, "a title")
	a.fields.Fanart = &source.ImageRef{Role: field.Fanart, URL: "https://a/broken.jpg"}
	b.fields.Fanart = &source.ImageRef{Role: field.Fanart, URL: "https://b/fanart.jpg"}

	cfg := scrapeCfg("a", "b")
	st, err := runScrape(t, cfg, a, b)
	if err != nil {
		t.Fatal(err)
	}
	md := st.Scraped.Items[0].Metadata
	if md.Source(field.Fanart) != "b" {
		t.Errorf("fanart source = %q, want the fallback that delivered", md.Source(field.Fanart))
	}
	if b.asked() != 1 {
		t.Errorf("b was asked %d times, want one fallback wave", b.asked())
	}
	// a's row must not claim the field it could not deliver.
	for _, attempt := range st.ScrapeRun.Attempts {
		if attempt.ID != "a" {
			continue
		}
		for _, name := range attempt.Fields {
			if name == field.Fanart {
				t.Error("a's source row claims fanart although its image never arrived")
			}
		}
	}
}

func TestScrapeDoesNotSkipASourceBecauseOfItsDeclaration(t *testing.T) {
	// `Fields()` is a startup check and a UI cross-reference, never a run-time
	// filter: a source listed for a field it does not declare still gets asked, and
	// its answer is used. (The loader refuses such a configuration; the engine does
	// not silently drop it.)
	src := newWaveSource("undeclared")
	src.declared = []field.Name{field.Tags}
	oneField(src, field.Title, "title from an undeclared field")

	cfg := scrapeCfg("undeclared")
	cfg = withPriority(cfg, field.Title, "undeclared")

	st, err := runScrape(t, cfg, src)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Scraped.Items[0].Metadata.Source(field.Title); got != "undeclared" {
		t.Errorf("title source = %q, want the source's own answer", got)
	}
}

func TestScrapeTreatsAZeroRatingAsUnsatisfied(t *testing.T) {
	// 0 on a 0–10 scale means "not rated", so it is not a contribution and the next
	// candidate gets its chance.
	zero, rated := newWaveSource("zero"), newWaveSource("rated")
	zeroRating, goodRating := 0.0, 8.5
	zero.fields.Rating = &zeroRating
	rated.fields.Rating = &goodRating

	cfg := scrapeCfg("zero", "rated")
	st, err := runScrape(t, cfg, zero, rated)
	if err != nil {
		t.Fatal(err)
	}
	md := st.Scraped.Items[0].Metadata
	if md.Source(field.Rating) != "rated" || md.Rating != 8.5 {
		t.Errorf("rating = %v from %q, want the rated source", md.Rating, md.Source(field.Rating))
	}
}

func TestScrapeRecordsAttemptsInConfigurationOrder(t *testing.T) {
	// source_runs is a ledger: two runs of the same number must produce the same
	// rows in the same order, whatever order the answers arrived in.
	slow, fast := newWaveSource("slow"), newWaveSource("fast")
	oneField(slow, field.Plot, "slow")
	oneField(fast, field.Title, "fast")
	slow.delay = 40 * time.Millisecond

	cfg := scrapeCfg("slow", "fast")
	cfg = withPriority(cfg, field.Title, "fast")
	cfg = withPriority(cfg, field.Plot, "slow")

	st, err := runScrape(t, cfg, slow, fast)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(st.ScrapeRun.Attempts))
	for _, a := range st.ScrapeRun.Attempts {
		ids = append(ids, a.ID)
	}
	if got := strings.Join(ids, ","); got != "slow,fast" {
		t.Errorf("attempt order = %s, want configuration order", got)
	}
}

func TestScrapeRecordsOnlyTheFieldsASourceWon(t *testing.T) {
	// A source that answers a field which an earlier candidate wins contributed
	// nothing to the number, so its source_runs row must not claim the field.
	loser, winner := newWaveSource("loser"), newWaveSource("winner")
	oneField(loser, field.Title, "loser title")
	oneField(loser, field.Tags, "loser tags")
	oneField(winner, field.Title, "winner title")
	oneField(winner, field.Tags, "winner tags")

	cfg := scrapeCfg("loser", "winner")
	cfg = withPriority(cfg, field.Title, "winner")
	cfg = withPriority(cfg, field.Tags, "winner")

	st, err := runScrape(t, cfg, loser, winner)
	if err != nil {
		t.Fatal(err)
	}
	fieldsOf := map[string]string{}
	for _, a := range st.ScrapeRun.Attempts {
		fieldsOf[a.ID] = names(a.Fields)
	}
	if fieldsOf["loser"] != "" {
		t.Errorf("the losing source's row claims %q; it won nothing", fieldsOf["loser"])
	}
	if fieldsOf["winner"] != "title,tags" {
		t.Errorf("the winning source's row = %q, want the two fields it won", fieldsOf["winner"])
	}
}

// imageProbeSource serves a list of stills and holds each fetch long enough for the
// concurrency assertion to see them overlap.
type imageProbeSource struct {
	refs  []source.ImageRef
	probe *probe
	hold  time.Duration
}

func (s *imageProbeSource) ID() string           { return "fixture" }
func (s *imageProbeSource) Fields() []field.Name { return field.All() }

func (s *imageProbeSource) Scrape(context.Context, string) (source.Result, error) {
	return source.Result{Found: true, Fields: source.Fields{Extrafanarts: s.refs}}, nil
}

func (s *imageProbeSource) FetchImage(ctx context.Context, ref source.ImageRef) ([]byte, error) {
	s.probe.enter()
	defer s.probe.leave()
	select {
	case <-time.After(s.hold):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return []byte("\xff\xd8\xff" + ref.URL), nil
}

// TestScrapeFetchesOneTaskImagesUpToTheConfiguredLimit is the
// `concurrency.images` nail: one winning source's stills are fetched in parallel,
// but never more than the configured number at once, and the metadata keeps the
// site's order afterwards.
func TestScrapeFetchesOneTaskImagesUpToTheConfiguredLimit(t *testing.T) {
	probe := &probe{}
	refs := make([]source.ImageRef, 6)
	for i := range refs {
		refs[i] = source.ImageRef{Role: field.Extrafanarts, Index: i, URL: fmt.Sprintf("https://site/still-%d.jpg", i)}
	}
	src := &imageProbeSource{refs: refs, probe: probe, hold: 30 * time.Millisecond}
	step, err := newScrape(scrapeCfg("fixture"), source.NewRegistry(src), 2)
	if err != nil {
		t.Fatal(err)
	}
	st := scrapeState("SSIS-001")
	if err := step.Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if got := probe.peak.Load(); got != 2 {
		t.Errorf("peak in-flight image fetches = %d, want the configured 2 (not one, not all six)", got)
	}
	item := st.Scraped.Items[0]
	if len(item.Images) != len(refs) {
		t.Fatalf("images = %d, want all %d", len(item.Images), len(refs))
	}
	for i, blob := range item.Images {
		if blob.Index != i {
			t.Errorf("image %d has index %d: the refs' order must survive concurrent fetching", i, blob.Index)
		}
	}
}

// TestScrapeWithoutAnImageLimitStillKeepsEveryImage: zero means "no per-task cap",
// which is what a pipeline test without a concurrency config gets; the Fetcher's
// process-wide cap still sits underneath in production.
func TestScrapeWithoutAnImageLimitStillKeepsEveryImage(t *testing.T) {
	probe := &probe{}
	refs := []source.ImageRef{
		{Role: field.Extrafanarts, Index: 0, URL: "https://site/a.jpg"},
		{Role: field.Extrafanarts, Index: 1, URL: "https://site/b.jpg"},
	}
	src := &imageProbeSource{refs: refs, probe: probe, hold: 20 * time.Millisecond}
	step, err := newScrape(scrapeCfg("fixture"), source.NewRegistry(src), 0)
	if err != nil {
		t.Fatal(err)
	}
	st := scrapeState("SSIS-001")
	if err := step.Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if got := len(st.Scraped.Items[0].Images); got != 2 {
		t.Errorf("images = %d, want both stills", got)
	}
	if got := probe.peak.Load(); got != 2 {
		t.Errorf("peak in-flight = %d, want both to overlap (no per-task cap)", got)
	}
}
