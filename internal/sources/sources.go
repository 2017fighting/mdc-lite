// Package sources is where configuration meets the built-in sources: it turns
// `scrape.with.sources.<id>` into source instances and the process-level settings
// into the one Fetcher they all share.
//
// It exists as its own package so neither side has to know the other: the sources
// take their knobs as a plain Options struct, and the config package stays about
// YAML. It also keeps the import graph a tree — internal/source is the vocabulary,
// internal/source/javdb one implementation, and this package the assembly. The one
// thing that crosses back is `Validate`, which needs both the configuration and the
// sources' capability declarations, and this is the only package that has both.
package sources

import (
	"sort"
	"strings"
	"time"

	"github.com/2017fighting/mdc-lite/internal/config"
	"github.com/2017fighting/mdc-lite/internal/fetch"
	"github.com/2017fighting/mdc-lite/internal/field"
	"github.com/2017fighting/mdc-lite/internal/source"
	"github.com/2017fighting/mdc-lite/internal/source/dmm"
	"github.com/2017fighting/mdc-lite/internal/source/fc2"
	"github.com/2017fighting/mdc-lite/internal/source/javbus"
	"github.com/2017fighting/mdc-lite/internal/source/javdb"
)

// NewFetcher builds the process's single transport. Every source in every pipeline
// must share this instance: the rate limit and the cooldown are per registrable
// domain *per process*, and two Fetchers would double every limit silently.
//
// `concurrency.requests` is the process-wide in-flight cap. It lives on the Fetcher
// because that is the one seam every request crosses — metadata and images, all four
// sources, every pipeline — so "eight requests in flight" cannot be exceeded by
// adding a step.
func NewFetcher(settings config.Settings) *fetch.Fetcher {
	return fetch.New(fetch.Options{
		Proxy:     settings.Proxy,
		MaxFlight: settings.Concurrency.Requests,
	})
}

// factories build one source from its knobs. This map is the whole list of sources
// the binary has, in one place; adding the next source is one entry.
var factories = map[string]func(config.SourceWith, *fetch.Fetcher) source.Source{
	javdb.ID: func(w config.SourceWith, f *fetch.Fetcher) source.Source {
		return javdb.New(f, javdbOptions(w))
	},
	dmm.ID: func(w config.SourceWith, f *fetch.Fetcher) source.Source {
		return dmm.New(f, dmmOptions(w))
	},
	javbus.ID: func(w config.SourceWith, f *fetch.Fetcher) source.Source {
		return javbus.New(f, javbusOptions(w))
	},
	fc2.ID: func(w config.SourceWith, f *fetch.Fetcher) source.Source {
		return fc2.New(f, fc2Options(w))
	},
}

// Build assembles the registry the scrape step dispatches to, from the pipeline's
// `scrape.with.sources` list.
//
// A source id this build does not implement is left out rather than refused: the
// registry is what the pipeline asked for, and `Validate` is what says an id is a
// typo. Keeping the two apart means a build with one source can still run a pipeline
// file written for four.
func Build(w config.ScrapeWith, f *fetch.Fetcher) source.Registry {
	reg := source.Registry{}
	for _, entry := range w.Sources {
		if build, ok := factories[entry.ID]; ok {
			reg[entry.ID] = build(entry.SourceWith, f)
		}
	}
	return reg
}

// For builds one source by id, with the knob object the pipeline wrote for it when
// there is one and with the documented defaults when there is not.
//
// It is what `mdc source test` uses: the command's question is "what does this source
// say now", so a source the pipeline did not select must still be testable. The
// pipeline's knobs still win when it did select it — a test that ignored the exit the
// operator configured would test a different machine than the one that runs tasks.
func For(w config.ScrapeWith, id string, f *fetch.Fetcher) (source.Source, bool) {
	build, ok := factories[id]
	if !ok {
		return nil, false
	}
	for _, entry := range w.Sources {
		if entry.ID == id {
			return build(entry.SourceWith, f), true
		}
	}
	return build(config.SourceWith{}, f), true
}

// Known lists the ids this binary builds, as one sorted comma-separated string.
func Known() string {
	return builtins().Known()
}

// builtins builds every source this binary has, with a nil transport: the capability
// check only asks what a source declares, and never makes it do anything.
func builtins() source.Registry {
	reg := source.Registry{}
	for id, build := range factories {
		reg[id] = build(config.SourceWith{}, nil)
	}
	return reg
}

// Validate refuses a scrape configuration the sources cannot honour.
//
// Two things are checked, and both are configuration mistakes that would otherwise
// show up as "this field is mysteriously always missing":
//
//   - every id in `scrape.with.sources` is a source this binary has (a typo there
//     means the source it was meant to be never runs);
//   - every id in `priority[field]` names a source that declares that field. The
//     declaration is a static capability, so "javdb gives me a poster" is knowledge
//     available at startup, not after a task fails. The engine does not consult the
//     declaration at run time — refusing the configuration is the whole check.
func Validate(p config.Pipeline) error {
	w := ScrapeWith(p)
	known := builtins()
	idx := -1
	for i, st := range p.Steps {
		if st.Type == config.StepScrape {
			idx = i
			break
		}
	}
	knownIDs := known.Known()

	for i, entry := range w.Sources {
		if _, ok := known.Lookup(entry.ID); !ok {
			return config.StepError(p, idx, "sources[%d]: %q is not a source this binary has (known: %s)", i, entry.ID, knownIDs)
		}
	}
	for _, name := range field.All() {
		order, ok := w.Priority[string(name)]
		if !ok {
			continue
		}
		for _, id := range order {
			src, ok := known.Lookup(id)
			if !ok {
				return config.StepError(p, idx, "priority.%s: %q is not a source this binary has (known: %s)", name, id, knownIDs)
			}
			if !declares(src, name) {
				return config.StepError(p, idx, "priority.%s names %q, which does not declare that field (it declares: %s)",
					name, id, declaredList(src))
			}
		}
	}
	return nil
}

func declares(src source.Source, name field.Name) bool {
	for _, declared := range src.Fields() {
		if declared == name {
			return true
		}
	}
	return false
}

func declaredList(src source.Source) string {
	names := make([]string, 0, len(src.Fields()))
	for _, name := range src.Fields() {
		names = append(names, string(name))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// javdbOptions maps the four configured knobs onto the source's options. The
// signature constants are not here: they are code constants.
func javdbOptions(w config.SourceWith) javdb.Options {
	return javdb.Options{
		Proxy:   w.Proxy,
		Domains: w.Domains,
		Rate:    rate(w.Rate),
		Timeout: time.Duration(w.Timeout),
	}
}

// dmmOptions maps the four configured knobs onto the source's options. `proxy` is
// the knob that matters most here: dmm needs a Japanese exit, and pointing one source
// at one is exactly what a per-source proxy is for.
func dmmOptions(w config.SourceWith) dmm.Options {
	return dmm.Options{
		Proxy:   w.Proxy,
		Domains: w.Domains,
		Rate:    rate(w.Rate),
		Timeout: time.Duration(w.Timeout),
	}
}

// fc2Options maps the four configured knobs onto the source's options.
func fc2Options(w config.SourceWith) fc2.Options {
	return fc2.Options{
		Proxy:   w.Proxy,
		Domains: w.Domains,
		Rate:    rate(w.Rate),
		Timeout: time.Duration(w.Timeout),
	}
}

// javbusOptions maps the four configured knobs onto the source's options. UA, the
// age-gate cookie and the image Referer are not here: they are code constants.
func javbusOptions(w config.SourceWith) javbus.Options {
	return javbus.Options{
		Proxy:   w.Proxy,
		Domains: w.Domains,
		Rate:    rate(w.Rate),
		Timeout: time.Duration(w.Timeout),
	}
}

// rate turns the configured budget into the Fetcher's, or nil when the pipeline did
// not configure one (nil means "the process default", which is not the same as zero).
func rate(r *config.Rate) *fetch.Rate {
	if r == nil {
		return nil
	}
	return &fetch.Rate{
		RequestsPerSecond: r.RequestsPerSecond,
		Burst:             r.Burst,
		Cooldown:          time.Duration(r.Cooldown),
	}
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
