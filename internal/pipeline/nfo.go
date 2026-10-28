package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nikolalohinski/gonja/v2/exec"

	"github.com/2017fighting/mdc-lite/internal/config"
	"github.com/2017fighting/mdc-lite/internal/field"
	"github.com/2017fighting/mdc-lite/internal/meta"
)

// nfoStep renders one `movie.nfo` per group. It is a pure function of the
// artifacts: the text leaves here, the file is written by `output`.
//
// The template environment has autoescape ON. A source's plot containing `&` must
// come out as `&amp;`, otherwise the whole XML is invalid and both media servers
// silently apply none of it.
type nfoStep struct {
	cfg   config.NfoWith
	tmpl  *exec.Template
	src   string
	label string
}

func newNfo(w config.NfoWith, templatesDir string) (Step, error) {
	label, body, err := resolveTemplate(w.Template, templatesDir)
	if err != nil {
		return nil, err
	}
	tmpl, err := compileTemplate(label, body, true)
	if err != nil {
		return nil, err
	}
	return &nfoStep{cfg: w, tmpl: tmpl, src: body, label: label}, nil
}

func (s *nfoStep) Type() string { return config.StepNfo }

func (s *nfoStep) Name() string { return "" }

func (s *nfoStep) Requires() []string { return []string{config.ArtifactScraped} }

func (s *nfoStep) Produces() []string { return []string{config.ArtifactNfoDoc} }

func (s *nfoStep) Run(_ context.Context, st *State) error {
	items, err := nfoInput(st)
	if err != nil {
		return err
	}
	out := &NfoDoc{}
	summary := &NfoSummary{Items: len(items), Template: s.label}
	for _, item := range items {
		vars := templateVars(item.Number.Normalized, groupPart(item.Group), item.Metadata)
		var buf bytes.Buffer
		if err := s.tmpl.Execute(&buf, exec.NewContext(vars)); err != nil {
			return fmt.Errorf("%s: %s: %w", item.Number.Normalized, s.label, err)
		}
		out.Items = append(out.Items, NfoItem{
			Group:    item.Group,
			Number:   item.Number,
			Metadata: item.Metadata,
			Content:  buf.String(),
		})
		summary.Rendered++
	}
	st.NfoDoc = out
	st.NfoRun = summary
	return nil
}

// nfoInput is the images artifact when the pipeline has an `images` step, and the
// scrape result otherwise: `images` is optional, and an NFO of the raw fields is
// still a valid NFO.
func nfoInput(st *State) ([]ImagesItem, error) {
	items := stateItems(st, false)
	if items == nil {
		return nil, fmt.Errorf("no Images or Scraped artifact to render an NFO from")
	}
	return items, nil
}

// groupPart is the `part` a group's single NFO renders with: the primary's part
// number when the work is split, and nothing for a single file (one `movie.nfo`
// covers the whole work, so "part 1 of 1" would be noise).
func groupPart(g Group) any {
	if len(g.Parts) > 1 {
		return g.Parts[0].Index()
	}
	return ""
}

// resolveTemplate applies the three-way selector: `@name` is a file under
// templates_dir, anything containing `{{` or a newline is inline, and anything else
// is a builtin name. A file selector was already checked for `..` and absolute
// paths at startup; this repeats the check because a path is a path.
func resolveTemplate(sel, templatesDir string) (label, body string, err error) {
	switch {
	case sel == "" || sel == "default":
		return "builtin:default", builtinNfoTemplate, nil
	case strings.HasPrefix(sel, "@"):
		name := strings.TrimPrefix(sel, "@")
		if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
			return "", "", fmt.Errorf("template %q: a file name under templates_dir is required", sel)
		}
		path := filepath.Join(templatesDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return "", "", fmt.Errorf("template %q: %w", sel, err)
		}
		return "@" + name, string(data), nil
	case strings.Contains(sel, "{{") || strings.Contains(sel, "\n"):
		return "inline", sel, nil
	default:
		return "", "", fmt.Errorf("template %q is not a builtin name (only \"default\")", sel)
	}
}

// builtinNfoTemplate is the `default` template. Its mapping is fixed by spec §9:
// title carries the number prefix, plot never becomes <outline>, releasedate is
// written twice (premiered + releasedate, because the two servers disagree about
// which one is authoritative), publisher has no slot of its own and lands in a
// <tag>, and the actor's sex goes into <role> because neither server has an
// "actress" person kind.
const builtinNfoTemplate = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<movie>
  <title>{{ number }} {{ title }}</title>
  <originaltitle>{{ title }}</originaltitle>
  <sorttitle>{{ title }}{% if part %} cd{{ part }}{% endif %}</sorttitle>
  <plot>{{ plot }}</plot>
{% if releasedate %}  <premiered>{{ releasedate }}</premiered>
  <releasedate>{{ releasedate }}</releasedate>
{% endif %}{% if runtime %}  <runtime>{{ runtime }}</runtime>
{% endif %}{% if rating %}  <rating>{{ rating }}</rating>
{% endif %}{% if series %}  <set>
    <name>{{ series }}</name>
  </set>
{% endif %}{% if studio %}  <studio>{{ studio }}</studio>
{% endif %}{% if publisher %}  <tag>发行: {{ publisher }}</tag>
{% endif %}{% for d in director %}  <director>{{ d }}</director>
{% endfor %}{% for t in tags %}  <tag>{{ t }}</tag>
{% endfor %}{% for a in actors %}  <actor>
    <name>{{ a.name }}</name>
{% if a.role %}    <role>{{ a.role }}</role>
{% endif %}  </actor>
{% endfor %}</movie>
`

// templateVars flattens the metadata into the one vocabulary a template sees: the
// 14 field names plus `number` and `part`, and the derived keys. The NFO and the
// path templates share it, so a user learns one set of names.
func templateVars(number string, part any, md meta.Metadata) map[string]any {
	actors := make([]map[string]any, 0, len(md.Actors))
	for _, a := range md.Actors {
		actors = append(actors, map[string]any{
			"name":   a.Name,
			"gender": string(a.Gender),
			"role":   actorRole(a.Gender),
		})
	}
	return map[string]any{
		"number":                   number,
		"part":                     part,
		string(field.Title):        md.Title,
		string(field.Plot):         md.Plot,
		string(field.Extrafanarts): md.Extrafanarts,
		string(field.Tags):         md.Tags,
		string(field.Releasedate):  md.Releasedate,
		string(field.Actors):       actors,
		string(field.Director):     md.Director,
		string(field.Runtime):      md.Runtime,
		string(field.Rating):       md.Rating,
		string(field.Series):       md.Series,
		string(field.Studio):       md.Studio,
		string(field.Publisher):    md.Publisher,
		// The image fields (poster, fanart, extrafanarts) are deliberately absent:
		// they have bytes, not text, and images are written by path, never by
		// template. Substituting a source id or a URL here would be a value the
		// template context does not promise.
		"title_cn":    md.Derived.TitleCN,
		"plot_cn":     md.Derived.PlotCN,
		"tags_cn":     md.Derived.TagsCN,
		"actors_cn":   md.Derived.ActorsCN,
		"poster_crop": md.Derived.PosterCrop,
	}
}

// actorRole is the only place the sex reaches the NFO: 女优 for female, 男优 for
// male, and nothing for unknown (a wrong guess is worse than an empty role).
func actorRole(g meta.Gender) string {
	switch g {
	case meta.Female:
		return "女优"
	case meta.Male:
		return "男优"
	default:
		return ""
	}
}

// NfoSummary is the nfo step's summary projection.
type NfoSummary struct {
	Items    int    `json:"items"`
	Rendered int    `json:"rendered"`
	Template string `json:"template"`
}

// String renders the nfo summary as one line.
func (s NfoSummary) String() string {
	return fmt.Sprintf("%s with template %s", plural(s.Rendered, "movie.nfo", "movie.nfo"), s.Template)
}
