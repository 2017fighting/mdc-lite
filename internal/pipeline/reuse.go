package pipeline

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"

	"github.com/2017fighting/mdc-lite/internal/config"
	"github.com/2017fighting/mdc-lite/internal/field"
	"github.com/2017fighting/mdc-lite/internal/meta"
)

// MetadataStore looks up what an earlier task already found for a number. `tasks`
// keeps the last finished run's `metadata_json`, so the crawl of a work is a
// one-time cost per number rather than per file (issue #39).
type MetadataStore interface {
	FindMetadataJSON(ctx context.Context, number string) (string, bool, error)
}

// ReuseKind says where an item's metadata came from this run. It is what a trace
// reads to tell "twenty numbers, zero requests" from a bug: the empty value means
// the sources were asked.
type ReuseKind string

// The provenance ids a reused field carries in `sources`. Neither is a source with an
// id, which is the point: the map answers "who supplied this value", and this run
// nobody did.
const (
	reuseFromNFO   = "nfo"
	reuseFromLocal = "local"
)

const (
	// ReuseNone is a normal scrape.
	ReuseNone ReuseKind = ""
	// ReuseNFO is the output directory's own `movie.nfo`. That file is also the one
	// thing this run must not touch: it is where the values came from, and rendering
	// them back through a lossy round trip would drop whatever the template that
	// wrote it knew and ours does not.
	ReuseNFO ReuseKind = reuseFromNFO
	// ReuseRecord is a prior task's `metadata_json`. Nothing has to be re-rendered
	// away, so the NFO is written as usual — the directory may not have one yet.
	ReuseRecord ReuseKind = "record"
)

// ScrapeOption configures scrapeStep at build time.
type ScrapeOption func(*scrapeStep)

// WithReuse turns metadata reuse on with the questions `output` answers: where a
// work lives, and what a work must supply to be written. A scrape step built
// without it always asks the sources.
func WithReuse(g ReuseGate) ScrapeOption {
	return func(s *scrapeStep) {
		s.gate = g
	}
}

// WithMetadataStore sets the store metadata reuse falls back to.
func WithMetadataStore(st MetadataStore) ScrapeOption {
	return func(s *scrapeStep) {
		s.metadataStore = st
	}
}

// reuseGate derives metadata reuse's questions from the pipeline: the directory rule
// and the required-fields gate are the output step's, and whether images land at all
// is whether the pipeline has an `images` step. Nil means the pipeline has no output
// step to ask (a preview that stops earlier), so there is nothing to reuse for.
func reuseGate(p config.Pipeline) (*ReuseGate, error) {
	var (
		out    config.OutputWith
		hasOut bool
		images bool
	)
	for _, st := range p.Steps {
		switch st.Type {
		case config.StepOutput:
			if ow, ok := st.WithValue.(*config.OutputWith); ok {
				out, hasOut = *ow, true
			}
		case config.StepImages:
			images = true
		}
	}
	if !hasOut || out.Root == "" {
		return nil, nil
	}
	dir, err := reuseDirResolver(out.Root, out.DirTemplate)
	if err != nil {
		return nil, err
	}
	return &ReuseGate{Dir: dir, Required: requiredFields(out.RequiredFields), Images: images}, nil
}

// reuseDirResolver renders `output.with.dir_template` the way the output step does,
// so the lookup and the write agree on where a work lives.
func reuseDirResolver(root, dirTemplate string) (func(string, meta.Metadata) string, error) {
	renderer, err := newDirRenderer(dirTemplate)
	if err != nil {
		return nil, err
	}
	return func(number string, md meta.Metadata) string {
		rel, err := renderer.RenderDir(fileVars(number, "", VersionFlags{}, md))
		if err != nil {
			// The template rendered for `output` at build time, which is why a
			// failure here means one weird value rather than a broken pipeline: the
			// safest answer is "the obvious directory", and a reuse miss costs one
			// scrape.
			return filepath.Join(root, number)
		}
		return filepath.Join(root, filepath.FromSlash(rel))
	}, nil
}

// tryReuse answers "is this number's work already known here?". Two sources, in
// order: the output directory's own `movie.nfo` — what the media server is showing
// right now — and then the database's record of an earlier task.
func (s *scrapeStep) tryReuse(ctx context.Context, work ParsedWork) (meta.Metadata, ReuseKind, bool) {
	number := work.Number.Normalized
	if strings.TrimSpace(number) == "" || s.gate.Dir == nil {
		return meta.Metadata{}, ReuseNone, false
	}

	// The directory of a work depends on its metadata (`dir_template` may name a
	// field) and the metadata is what we are looking for, so the search starts with
	// the fieldless guess — right for every template that names only the number —
	// and only then uses the record's fields to guess again.
	if md, ok := s.reuseFromNFO(s.gate.Dir(number, meta.Metadata{}), number); ok {
		return md, ReuseNFO, true
	}
	if s.metadataStore == nil {
		return meta.Metadata{}, ReuseNone, false
	}
	raw, ok, err := s.metadataStore.FindMetadataJSON(ctx, number)
	if err != nil || !ok {
		return meta.Metadata{}, ReuseNone, false
	}
	var md meta.Metadata
	if err := json.Unmarshal([]byte(raw), &md); err != nil || len(md.Sources) == 0 {
		// A record with no provenance has no field either: `sources` is what says a
		// value arrived from somewhere.
		return meta.Metadata{}, ReuseNone, false
	}
	dir := s.gate.Dir(number, md)
	if local, ok := s.reuseFromNFO(dir, number); ok {
		return local, ReuseNFO, true
	}
	md = s.withLocalImages(md, dir)
	if !s.gatePasses(md) {
		// The record knows the work, but this directory cannot finish it: a required
		// field is missing and no later step would fetch it. Scrape instead.
		return meta.Metadata{}, ReuseNone, false
	}
	return md, ReuseRecord, true
}

// reuseFromNFO parses the directory's `movie.nfo` and accepts it when the work it
// describes can still be written from what is on disk.
func (s *scrapeStep) reuseFromNFO(dir, number string) (meta.Metadata, bool) {
	data, err := os.ReadFile(filepath.Join(dir, "movie.nfo"))
	if err != nil {
		return meta.Metadata{}, false
	}
	md, ok := parseNFO(data, number)
	if !ok {
		return meta.Metadata{}, false
	}
	md = s.withLocalImages(md, dir)
	if !s.gatePasses(md) {
		return meta.Metadata{}, false
	}
	return md, true
}

// gatePasses reports whether a reused work satisfies `output.with.required_fields`.
// The image half is already settled by withLocalImages: a field it found no file
// for is no longer won, so it arrives here as missing.
func (s *scrapeStep) gatePasses(md meta.Metadata) bool {
	for _, f := range s.gate.Required {
		if !md.Has(f) {
			return false
		}
	}
	return true
}

// withLocalImages marks the image fields this directory really holds, and forgets
// the ones it does not: a field's provenance is a claim that the bytes are in hand.
// A pipeline without an `images` step writes no image files at all, so there is
// nothing to check and the record's own claim stands — that shape produces the same
// directory either way.
func (s *scrapeStep) withLocalImages(md meta.Metadata, dir string) meta.Metadata {
	if !s.gate.Images {
		return md
	}
	held := map[field.Name]bool{
		field.Poster:       hasImageFile(dir, string(field.Poster)),
		field.Fanart:       hasImageFile(dir, string(field.Fanart)),
		field.Extrafanarts: len(localStills(dir)) > 0,
	}
	for _, f := range imageFields {
		if held[f] {
			md.Win(f, reuseFromLocal)
			continue
		}
		md.Forget(f)
	}
	return md
}

// hasImageFile reports whether dir holds `base` as a real image: a regular file
// with bytes in it. A directory or an empty file is not a cover, and accepting one
// would let reuse write a work whose image the media server cannot read.
func hasImageFile(dir, base string) bool {
	for _, ext := range imageFileExtensions() {
		info, err := os.Stat(filepath.Join(dir, base+ext))
		if err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			return true
		}
	}
	return false
}

// localStills lists the stills already in the directory's `extrafanart` folder. The
// names are the ones `images` writes (`fanart1.jpg`), so a still that is there is a
// still this run need not fetch.
func localStills(dir string) []string {
	entries, err := os.ReadDir(filepath.Join(dir, "extrafanart"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !isImageExt(filepath.Ext(e.Name())) {
			continue
		}
		out = append(out, e.Name())
	}
	return out
}

// nfoDoc is the shape the builtin template writes. Reading it back is how "已有
// movie.nfo 就复用" is honoured without trusting a file blindly: a document that is
// not a `<movie>` with anything in it is not reused.
type nfoDoc struct {
	XMLName       xml.Name   `xml:"movie"`
	Title         string     `xml:"title"`
	OriginalTitle string     `xml:"originaltitle"`
	Plot          string     `xml:"plot"`
	Premiered     string     `xml:"premiered"`
	ReleaseDate   string     `xml:"releasedate"`
	Runtime       int        `xml:"runtime"`
	Rating        float64    `xml:"rating"`
	Set           nfoSet     `xml:"set"`
	Studio        string     `xml:"studio"`
	Directors     []string   `xml:"director"`
	Tags          []string   `xml:"tag"`
	Actors        []nfoActor `xml:"actor"`
}

type nfoSet struct {
	Name string `xml:"name"`
}

type nfoActor struct {
	Name string `xml:"name"`
	Role string `xml:"role"`
}

// parseNFO turns a `movie.nfo` back into metadata: the inverse of the builtin
// template. It loses what an NFO cannot hold — the image fields' provenance, the
// stills list, the derived (translated) values — which is why a reused NFO is left
// on disk untouched instead of being rendered again from this.
func parseNFO(data []byte, number string) (meta.Metadata, bool) {
	var doc nfoDoc
	if err := xml.Unmarshal(data, &doc); err != nil || doc.XMLName.Local != "movie" {
		return meta.Metadata{}, false
	}
	var md meta.Metadata
	if title := nfoTitle(doc, number); title != "" {
		md.Title = title
		md.Win(field.Title, reuseFromNFO)
	}
	if plot := strings.TrimSpace(doc.Plot); plot != "" {
		md.Plot = plot
		md.Win(field.Plot, reuseFromNFO)
	}
	if date := strings.TrimSpace(doc.ReleaseDate); date != "" {
		md.Releasedate = date
		md.Win(field.Releasedate, reuseFromNFO)
	} else if date := strings.TrimSpace(doc.Premiered); date != "" {
		md.Releasedate = date
		md.Win(field.Releasedate, reuseFromNFO)
	}
	if doc.Runtime > 0 {
		md.Runtime = doc.Runtime
		md.Win(field.Runtime, reuseFromNFO)
	}
	if doc.Rating > 0 {
		md.Rating = doc.Rating
		md.Win(field.Rating, reuseFromNFO)
	}
	if studio := strings.TrimSpace(doc.Studio); studio != "" {
		md.Studio = studio
		md.Win(field.Studio, reuseFromNFO)
	}
	if series := strings.TrimSpace(doc.Set.Name); series != "" {
		md.Series = series
		md.Win(field.Series, reuseFromNFO)
	}
	for _, d := range doc.Directors {
		if d = strings.TrimSpace(d); d != "" {
			md.Director = append(md.Director, d)
		}
	}
	if len(md.Director) > 0 {
		md.Win(field.Director, reuseFromNFO)
	}
	for _, tag := range doc.Tags {
		tag = strings.TrimSpace(tag)
		// The builtin template puts the publisher in a `<tag>`; the prefix is the
		// only thing that tells the two apart on the way back.
		if rest, ok := strings.CutPrefix(tag, publisherTagPrefix); ok {
			md.Publisher = strings.TrimSpace(rest)
			md.Win(field.Publisher, reuseFromNFO)
			continue
		}
		if tag != "" {
			md.Tags = append(md.Tags, tag)
		}
	}
	if len(md.Tags) > 0 {
		md.Win(field.Tags, reuseFromNFO)
	}
	for _, a := range doc.Actors {
		if name := strings.TrimSpace(a.Name); name != "" {
			md.Actors = append(md.Actors, meta.Actor{Name: name, Gender: nfoGender(a.Role)})
		}
	}
	if len(md.Actors) > 0 {
		md.Win(field.Actors, reuseFromNFO)
	}
	if len(md.Sources) == 0 {
		// An NFO with no values is the same as none: the sources may still know this
		// work, so it is not a reuse hit.
		return meta.Metadata{}, false
	}
	return md, true
}

// nfoTitle recovers the title from `<originaltitle>`, which the builtin template
// writes without the number prefix. A document that only has `<title>` carries
// `NUMBER title`, so the prefix is stripped when it is there.
func nfoTitle(doc nfoDoc, number string) string {
	if t := strings.TrimSpace(doc.OriginalTitle); t != "" {
		return t
	}
	t := strings.TrimSpace(doc.Title)
	if number != "" && len(t) > len(number) && strings.EqualFold(t[:len(number)], number) {
		if rest := strings.TrimSpace(t[len(number):]); rest != "" {
			return rest
		}
	}
	return t
}

// nfoGender reads the `<role>` the template wrote back into the domain's gender.
func nfoGender(role string) meta.Gender {
	switch strings.TrimSpace(role) {
	case actorRole(meta.Female):
		return meta.Female
	case actorRole(meta.Male):
		return meta.Male
	default:
		return meta.Unknown
	}
}
