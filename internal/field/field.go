// Package field holds the V1 source-level field vocabulary. The names here are
// the ones a pipeline, a template, the API and the docs all use; see GLOSSARY.md
// ("14 个源级字段") and the V1 spec §5.
package field

// Name is a source-level field name. The zero value is not a valid field.
type Name string

// The 14 source-level fields of V1, in the order the spec lists them.
const (
	Title        Name = "title"
	Plot         Name = "plot"
	Fanart       Name = "fanart"
	Extrafanarts Name = "extrafanarts"
	Tags         Name = "tags"
	Releasedate  Name = "releasedate"
	Actors       Name = "actors"
	Poster       Name = "poster"
	Director     Name = "director"
	Runtime      Name = "runtime"
	Rating       Name = "rating"
	Series       Name = "series"
	Studio       Name = "studio"
	Publisher    Name = "publisher"
)

// Number is the built-in field produced by the parse step. It is not a
// source-level field: no scraper supplies it, and it never appears in Sources
// or Missing.
const Number Name = "number"

var all = []Name{
	Title, Plot, Fanart, Extrafanarts, Tags, Releasedate, Actors,
	Poster, Director, Runtime, Rating, Series, Studio, Publisher,
}

// All returns the 14 source-level fields in spec order.
func All() []Name {
	out := make([]Name, len(all))
	copy(out, all)
	return out
}

// Valid reports whether s names one of the 14 source-level fields.
func Valid(s string) bool {
	for _, f := range all {
		if string(f) == s {
			return true
		}
	}
	return false
}
