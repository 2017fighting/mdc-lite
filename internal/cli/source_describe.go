package cli

import (
	"fmt"
	"strings"

	"github.com/2017fighting/mdc-lite/internal/field"
	"github.com/2017fighting/mdc-lite/internal/source"
)

// describeField renders one field the way `mdc source test` shows it: a scalar as
// its value, a list as a comma-separated line, an actor with the gender the source
// declared, and an image as its address (an image field has contributed nothing
// until the engine fetched it, so what a source can show about it is where the bytes
// would come from).
//
// The second return is whether this source contributed the field at all, which is
// not the same as a non-empty rendering: `rating: 0` contributes, and an empty
// title does not.
//
// It lives here rather than on source.Fields because "how a value is printed" is the
// command's business: the vocabulary package should not know that a runtime is shown
// in minutes.
func describeField(name field.Name, f source.Fields) (string, bool) {
	switch name {
	case field.Title:
		return str(f.Title)
	case field.Plot:
		return str(f.Plot)
	case field.Releasedate:
		return str(f.Releasedate)
	case field.Series:
		return str(f.Series)
	case field.Studio:
		return str(f.Studio)
	case field.Publisher:
		return str(f.Publisher)
	case field.Runtime:
		if f.Runtime == nil {
			return "", false
		}
		return fmt.Sprintf("%d 分钟", *f.Runtime), true
	case field.Rating:
		if f.Rating == nil {
			return "", false
		}
		return fmt.Sprintf("%.2f", *f.Rating), true
	case field.Tags:
		return list(f.Tags)
	case field.Director:
		return list(f.Director)
	case field.Actors:
		if len(f.Actors) == 0 {
			return "", false
		}
		parts := make([]string, len(f.Actors))
		for i, a := range f.Actors {
			parts[i] = fmt.Sprintf("%s (%s)", a.Name, a.Gender)
		}
		return strings.Join(parts, ", "), true
	case field.Poster:
		return image(f.Poster)
	case field.Fanart:
		return image(f.Fanart)
	case field.Extrafanarts:
		if len(f.Extrafanarts) == 0 {
			return "", false
		}
		parts := make([]string, len(f.Extrafanarts))
		for i, ref := range f.Extrafanarts {
			parts[i] = ref.URL
		}
		return strings.Join(parts, ", "), true
	default:
		return "", false
	}
}

func str(p *string) (string, bool) {
	if p == nil {
		return "", false
	}
	return *p, true
}

func list(v []string) (string, bool) {
	if len(v) == 0 {
		return "", false
	}
	return strings.Join(v, ", "), true
}

func image(ref *source.ImageRef) (string, bool) {
	if ref == nil {
		return "", false
	}
	return ref.URL, true
}
