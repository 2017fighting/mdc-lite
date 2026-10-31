package pipeline

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"

	"github.com/2017fighting/mdc-lite/internal/field"
	"github.com/2017fighting/mdc-lite/internal/meta"
)

// MetadataStore looks up what an earlier task already found for a number. `tasks`
// keeps the last successful run's `metadata_json`, so the crawl of a work is a
// one-time cost per number rather than per file (issue #39).
type MetadataStore interface {
	FindMetadataJSON(ctx context.Context, number string) (string, bool, error)
}

// ScrapeOption configures scrapeStep at build time.
type ScrapeOption func(*scrapeStep)

// WithOutputDir sets the resolver that answers "where would this number's
// directory be", which is where a reused work's `movie.nfo` and images live. It
// takes the metadata because `dir_template` may name a field (`{{ studio }}`): the
// directory of a work can only be known once the work is known.
func WithOutputDir(fn func(number string, md meta.Metadata) string) ScrapeOption {
	return func(s *scrapeStep) {
		s.outputDir = fn
	}
}

// WithMetadataStore sets the store metadata reuse falls back to.
func WithMetadataStore(st MetadataStore) ScrapeOption {
	return func(s *scrapeStep) {
		s.metadataStore = st
	}
}

// buildOutputDirResolver renders `output.with.dir_template` the way the output step
// does, so the reuse lookup and the write agree on where a work lives.
func buildOutputDirResolver(root, tmpl string) func(string, meta.Metadata) string {
	if strings.TrimSpace(tmpl) == "" {
		tmpl = "{{ number }}"
	}
	fallback := func(number string) string { return filepath.Join(root, number) }
	renderer, err := compileSegments(tmpl)
	if err != nil {
		return func(number string, _ meta.Metadata) string { return fallback(number) }
	}
	return func(number string, md meta.Metadata) string {
		parts, err := renderSegments(renderer, fileVars(number, "", VersionFlags{}, md))
		if err != nil {
			return fallback(number)
		}
		return filepath.Join(root, filepath.FromSlash(strings.Join(parts, "/")))
	}
}

// reuseSource names where a reused metadata set came from, in the `sources` map's
// vocabulary: "nfo" is the local `movie.nfo`, and the record's own map keeps the
// original source ids when it came from the database. The image fields a directory
// still holds are marked "local", because nobody downloaded them this run.
const (
	reuseFromNFO   = "nfo"
	reuseFromLocal = "local"
)

// tryReuse answers "is this number's work already known here?". Reuse is only
// accepted when the local directory holds what the metadata claims, because
// skipping the network is a promise that the output step can still write a
// complete work: metadata whose images never arrived would turn a partial-but-
// written task into a failed one.
func (s *scrapeStep) tryReuse(ctx context.Context, work ParsedWork) (meta.Metadata, bool) {
	number := work.Number.Normalized
	if strings.TrimSpace(number) == "" {
		return meta.Metadata{}, false
	}

	// The directory of a work depends on its metadata (dir_template may name a
	// field), and the metadata is what we are looking for. So the search is: the
	// fieldless guess first — right for every template that names only the number —
	// and then the guess the database's record makes possible.
	if s.outputDir != nil {
		if md, ok := reuseFromDir(s.outputDir(number, meta.Metadata{}), number); ok {
			return md, true
		}
	}
	if s.metadataStore == nil || s.outputDir == nil {
		return meta.Metadata{}, false
	}
	raw, ok, err := s.metadataStore.FindMetadataJSON(ctx, number)
	if err != nil || !ok {
		return meta.Metadata{}, false
	}
	var md meta.Metadata
	if err := json.Unmarshal([]byte(raw), &md); err != nil {
		return meta.Metadata{}, false
	}
	if len(md.Sources) == 0 {
		// A record with no provenance has no field either: `sources` is what says a
		// value arrived from somewhere.
		return meta.Metadata{}, false
	}
	dir := s.outputDir(number, md)
	if !hasLocalImages(dir) {
		// The record knows the work, but this directory has none of its images: a
		// run that reused it would write a work the media server shows with no
		// cover, and no later step would fetch one. Scrape instead.
		return meta.Metadata{}, false
	}
	// Prefer the directory's own NFO when it is there — it is what the media server
	// is already showing — and only then the record's fields.
	if local, ok := reuseFromDir(dir, number); ok {
		return local, true
	}
	md = withLocalImages(md, dir)
	return md, true
}

// reuseFromDir reads `movie.nfo` in dir and accepts it when the images it implies
// are on disk too.
func reuseFromDir(dir, number string) (meta.Metadata, bool) {
	if !hasLocalImages(dir) {
		return meta.Metadata{}, false
	}
	data, err := os.ReadFile(filepath.Join(dir, "movie.nfo"))
	if err != nil {
		return meta.Metadata{}, false
	}
	md, ok := parseNFO(data, number)
	if !ok {
		return meta.Metadata{}, false
	}
	return withLocalImages(md, dir), true
}

// withLocalImages marks the image fields this directory really holds, and forgets
// the ones it does not: a field's provenance is a claim that the bytes are in hand.
func withLocalImages(md meta.Metadata, dir string) meta.Metadata {
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

// hasLocalImages reports whether dir holds the poster and the fanart. Those two are
// what a work's directory is useless without; the stills are an extra.
func hasLocalImages(dir string) bool {
	return hasImageFile(dir, string(field.Poster)) && hasImageFile(dir, string(field.Fanart))
}

// hasImageFile reports whether dir holds `base` plus any extension both media
// servers read. The name is the one `images` writes, not a guess.
func hasImageFile(dir, base string) bool {
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp", ".gif", ".tbn"} {
		if _, err := os.Stat(filepath.Join(dir, base+ext)); err == nil {
			return true
		}
	}
	return false
}

// localStills lists the stills already in the directory's `extrafanart` folder.
func localStills(dir string) []string {
	entries, err := os.ReadDir(filepath.Join(dir, "extrafanart"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".jpg", ".jpeg", ".png", ".webp", ".gif", ".tbn":
			out = append(out, e.Name())
		}
	}
	return out
}

// nfoDoc is the shape the builtin template writes. Reading it back is how the
// ticket's "已有 movie.nfo 就复用" is honoured without trusting a file we did not
// write: a document that is not a `<movie>` with anything in it is not reused.
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

// parseNFO turns a `movie.nfo` back into metadata. It is the inverse of the builtin
// template, and it loses what an NFO cannot hold: an image field's provenance, the
// stills list, and the derived (translated) values.
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
		switch {
		case strings.HasPrefix(tag, publisherTagPrefix):
			md.Publisher = strings.TrimSpace(strings.TrimPrefix(tag, publisherTagPrefix))
			md.Win(field.Publisher, reuseFromNFO)
		case tag != "":
			md.Tags = append(md.Tags, tag)
		}
	}
	if len(md.Tags) > 0 {
		md.Win(field.Tags, reuseFromNFO)
	}
	for _, a := range doc.Actors {
		name := strings.TrimSpace(a.Name)
		if name == "" {
			continue
		}
		md.Actors = append(md.Actors, meta.Actor{Name: name, Gender: nfoGender(a.Role)})
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

// publisherTagPrefix is how the builtin template writes the publisher, since
// neither server has a field for it.
const publisherTagPrefix = "发行: "

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
