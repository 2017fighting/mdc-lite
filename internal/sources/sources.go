// Package sources is where configuration meets the built-in sources: it turns
// `scrape.with.sources.<id>` into source instances and the process-level settings
// into the one Fetcher they all share.
//
// It exists as its own package so neither side has to know the other: the sources
// take their knobs as a plain Options struct, and the config package stays about
// YAML. It also keeps the import graph a tree — internal/source is the vocabulary,
// internal/source/javbus one implementation, and this package the assembly.
package sources

import (
	"time"

	"github.com/2017fighting/mdc-lite/internal/config"
	"github.com/2017fighting/mdc-lite/internal/fetch"
	"github.com/2017fighting/mdc-lite/internal/source"
	"github.com/2017fighting/mdc-lite/internal/source/javbus"
)

// NewFetcher builds the process's single transport. Every source in every pipeline
// must share this instance: the rate limit and the cooldown are per registrable
// domain *per process*, and two Fetchers would double every limit silently.
func NewFetcher(settings config.Settings) *fetch.Fetcher {
	return fetch.New(fetch.Options{Proxy: settings.Proxy})
}

// factories build one source from its knobs. This map is the whole list of sources
// the binary has, in one place; adding the next source is one entry.
var factories = map[string]func(config.SourceWith, *fetch.Fetcher) source.Source{
	javbus.ID: func(w config.SourceWith, f *fetch.Fetcher) source.Source {
		return javbus.New(f, javbusOptions(w))
	},
}

// Build assembles the registry the scrape step dispatches to, from the pipeline's
// `scrape.with.sources` list.
//
// A source id this build does not implement is left out rather than refused: the
// factory config lists all four sources, and the three that arrive later must not
// keep the fourth from running. Asking for one of them at run time is already an
// error that names what the binary does know.
func Build(w config.ScrapeWith, f *fetch.Fetcher) source.Registry {
	reg := source.Registry{}
	for _, entry := range w.Sources {
		if build, ok := factories[entry.ID]; ok {
			reg[entry.ID] = build(entry.SourceWith, f)
		}
	}
	return reg
}

// javbusOptions maps the four configured knobs onto the source's options. UA, the
// age-gate cookie and the image Referer are not here: they are code constants.
func javbusOptions(w config.SourceWith) javbus.Options {
	opts := javbus.Options{
		Proxy:   w.Proxy,
		Domains: w.Domains,
		Timeout: time.Duration(w.Timeout),
	}
	if w.Rate != nil {
		opts.Rate = &fetch.Rate{
			RequestsPerSecond: w.Rate.RequestsPerSecond,
			Burst:             w.Rate.Burst,
			Cooldown:          time.Duration(w.Rate.Cooldown),
		}
	}
	return opts
}

// ScrapeWith returns the scrape settings of a pipeline, or the documented defaults
// when the pipeline has no scrape step (a preview that stops before scraping does
// not need one).
func ScrapeWith(p config.Pipeline) config.ScrapeWith {
	for _, st := range p.Steps {
		if st.Type != config.StepScrape {
			continue
		}
		if w, ok := st.WithValue.(*config.ScrapeWith); ok {
			return *w
		}
	}
	return config.ScrapeDefaults()
}
