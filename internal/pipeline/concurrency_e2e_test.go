package pipeline

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/2017fighting/mdc-lite/internal/fetch"
	"github.com/2017fighting/mdc-lite/internal/field"
	"github.com/2017fighting/mdc-lite/internal/source"
)

// httpSource is a source whose two phases go through the real Fetcher, so a test
// can observe both `concurrency.requests` (the Fetcher's process-wide cap) and
// `concurrency.images` (the task's own image budget) on the same wire.
type httpSource struct {
	f      *fetch.Fetcher
	page   string
	images []source.ImageRef
}

func (s *httpSource) ID() string           { return "fixture" }
func (s *httpSource) Fields() []field.Name { return field.All() }

func (s *httpSource) Scrape(ctx context.Context, _ string) (source.Result, error) {
	if _, err := s.f.Do(ctx, fetch.Request{URL: s.page}); err != nil {
		return source.Result{}, err
	}
	title := "a title"
	return source.Result{Found: true, Fields: source.Fields{
		Title:        &title,
		Extrafanarts: s.images,
	}}, nil
}

func (s *httpSource) FetchImage(ctx context.Context, ref source.ImageRef) ([]byte, error) {
	resp, err := s.f.Do(ctx, fetch.Request{URL: ref.URL})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// TestConcurrency_ImagesAndMetadataShareTheFetcherNail is the two-gate assertion on
// one wire: images go through the same Fetcher as metadata (so the process-wide
// `concurrency.requests` cap binds them), and the task's `concurrency.images` budget
// binds the images below that. A peak above either number would mean a second path
// to the network exists.
func TestConcurrency_ImagesAndMetadataShareTheFetcherNail(t *testing.T) {
	for _, tc := range []struct {
		name     string
		images   int
		requests int
		wantPeak int32
	}{
		{name: "the global request cap binds images", images: 4, requests: 3, wantPeak: 3},
		{name: "the per-task image budget binds first", images: 2, requests: 8, wantPeak: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var inflight, peak atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := inflight.Add(1)
				for {
					old := peak.Load()
					if n <= old || peak.CompareAndSwap(old, n) {
						break
					}
				}
				defer inflight.Add(-1)
				if strings.HasPrefix(r.URL.Path, "/img") {
					time.Sleep(30 * time.Millisecond)
					_, _ = w.Write([]byte("\xff\xd8\xff a jpeg"))
					return
				}
				_, _ = w.Write([]byte("a page"))
			}))
			defer srv.Close()

			refs := make([]source.ImageRef, 6)
			for i := range refs {
				refs[i] = source.ImageRef{Role: field.Extrafanarts, Index: i, URL: fmt.Sprintf("%s/img/%d", srv.URL, i)}
			}
			f := fetch.New(fetch.Options{
				MaxFlight: tc.requests,
				Rate:      fetch.Rate{RequestsPerSecond: 1000, Burst: 100},
			})
			src := &httpSource{f: f, page: srv.URL + "/page", images: refs}
			step, err := newScrape(scrapeCfg("fixture"), source.NewRegistry(src), tc.images)
			if err != nil {
				t.Fatal(err)
			}
			st := scrapeState("SSIS-001")
			if err := step.Run(context.Background(), st); err != nil {
				t.Fatal(err)
			}
			if got := peak.Load(); got != tc.wantPeak {
				t.Errorf("peak in-flight requests = %d, want %d", got, tc.wantPeak)
			}
			if got := len(st.Scraped.Items[0].Images); got != len(refs) {
				t.Errorf("kept %d images, want all %d", got, len(refs))
			}
		})
	}
}
