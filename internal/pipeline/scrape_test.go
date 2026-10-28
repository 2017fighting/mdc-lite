package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/2017fighting/mdc-lite/internal/config"
	"github.com/2017fighting/mdc-lite/internal/field"
	"github.com/2017fighting/mdc-lite/internal/source"
)

// scrapeState is the smallest artifact a scrape run needs: one parsed group.
func scrapeState(number string) *State {
	return &State{Parsed: &Parsed{Items: []ParsedGroup{
		{Number: Number{Raw: number, Normalized: number}},
	}}}
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
	step, err := newScrape(cfg, source.NewRegistry(sources...))
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
