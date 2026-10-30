package pipeline

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/nikolalohinski/gonja/v2"
	"github.com/nikolalohinski/gonja/v2/config"
	"github.com/nikolalohinski/gonja/v2/exec"
	"github.com/nikolalohinski/gonja/v2/loaders"
)

// This file is the naming side of `output`: two gonja templates (the directory and
// the file name) rendered with escaping OFF, because a path is not XML, and then
// sanitised segment by segment.
//
// The sanitising is what makes user data safe to concatenate into a path: a title
// containing a slash must become part of one directory's name, not a second
// directory. That is why the template itself is split on its literal `/` first and
// each piece is rendered separately — the template's structure decides the depth,
// the rendered values never do.

// maxSegmentBytes is the per-segment ceiling. It keeps one absurd studio name from
// producing a path no tool can open; the cut is on a UTF-8 boundary.
const maxSegmentBytes = 200

// pathRenderer renders the two output templates. Both environments are built with
// their own config.New(): gonja's DefaultConfig is a package-level mutable global,
// and the NFO env needs autoescape while this one must not have it.
type pathRenderer struct {
	dirSegments  []*exec.Template
	fileSegments []*exec.Template
}

// newPathRenderer compiles both templates up front, so a template syntax error is
// a build-time refusal rather than a surprise while writing a task's output.
func newPathRenderer(dirTemplate, fileTemplate string) (*pathRenderer, error) {
	r := &pathRenderer{}
	var err error
	if r.dirSegments, err = compileSegments(dirTemplate); err != nil {
		return nil, fmt.Errorf("dir_template: %w", err)
	}
	if r.fileSegments, err = compileSegments(fileTemplate); err != nil {
		return nil, fmt.Errorf("file_template: %w", err)
	}
	return r, nil
}

// RenderDir renders the directory template into a relative path whose segments are
// already sanitised. Empty segments are impossible: sanitiseSegment falls back to
// `_`.
func (r *pathRenderer) RenderDir(vars map[string]any) (string, error) {
	parts, err := renderSegments(r.dirSegments, vars)
	if err != nil {
		return "", err
	}
	return strings.Join(parts, "/"), nil
}

// RenderFile renders the file-name template into a name whose last segment is the
// file. The extension is the caller's business: the template names the basename.
func (r *pathRenderer) RenderFile(vars map[string]any) (string, error) {
	parts, err := renderSegments(r.fileSegments, vars)
	if err != nil {
		return "", err
	}
	return strings.Join(parts, "/"), nil
}

// compileSegments splits a template on its literal `/` (outside a `{{ }}` or
// `{% %}` tag) and compiles each piece separately.
func compileSegments(tpl string) ([]*exec.Template, error) {
	pieces := splitTemplateSegments(tpl)
	out := make([]*exec.Template, 0, len(pieces))
	for _, piece := range pieces {
		t, err := compileTemplate("path-segment", piece, false)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func renderSegments(segments []*exec.Template, vars map[string]any) ([]string, error) {
	out := make([]string, len(segments))
	for i, t := range segments {
		var buf bytes.Buffer
		// A fresh context per segment: gonja's Execute updates the context it is
		// given, and the next segment must start from the same values.
		if err := t.Execute(&buf, exec.NewContext(copyVars(vars))); err != nil {
			return nil, err
		}
		out[i] = sanitiseSegment(buf.String())
	}
	return out, nil
}

func copyVars(vars map[string]any) map[string]any {
	out := make(map[string]any, len(vars))
	for k, v := range vars {
		out[k] = v
	}
	return out
}

// compileTemplate builds the environment for one template. autoescape is on for
// the NFO and off for a path.
func compileTemplate(id, source string, autoescape bool) (*exec.Template, error) {
	cfg := config.New()
	cfg.AutoEscape = autoescape
	// StrictUndefined stays false: an optional field that no source supplied must
	// render as an empty string, not fail the whole task.

	base, err := loaders.NewFileSystemLoader("")
	if err != nil {
		return nil, err
	}
	loader, err := loaders.NewShiftedLoader(id, strings.NewReader(source), base)
	if err != nil {
		return nil, err
	}
	env := &exec.Environment{
		Context:           gonja.DefaultEnvironment.Context,
		Filters:           gonja.DefaultEnvironment.Filters,
		Tests:             gonja.DefaultEnvironment.Tests,
		ControlStructures: gonja.DefaultEnvironment.ControlStructures,
		Methods:           gonja.DefaultEnvironment.Methods,
	}
	return exec.NewTemplate(id, cfg, loader, env)
}

// splitTemplateSegments splits on `/` that is not inside a template tag, so
// `{{ number }}/{{ studio }}` is two segments while `{{ a | replace("/", "-") }}`
// stays one.
func splitTemplateSegments(tpl string) []string {
	var (
		out  []string
		cur  strings.Builder
		open string
	)
	for i := 0; i < len(tpl); i++ {
		c := tpl[i]
		if open == "" && (c == '{' && i+1 < len(tpl) && (tpl[i+1] == '{' || tpl[i+1] == '%')) {
			open = tpl[i : i+2]
			cur.WriteString(open)
			i++
			continue
		}
		if open != "" && c == '}' && i+1 < len(tpl) {
			closing := "}}"
			if open == "{%" {
				closing = "%}"
			}
			if tpl[i:i+2] == closing {
				cur.WriteString(closing)
				i++
				open = ""
				continue
			}
		}
		if open == "" && c == '/' {
			out = append(out, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	return append(out, cur.String())
}

// illegalSegmentChars is the fixed, non-configurable set from spec §10. It is a
// string test rather than a map or a regexp because it is checked once per byte,
// and a path is short.
const illegalSegmentChars = `/\:*?"<>|`

// sanitiseSegment applies the segment rules: illegal and control characters become
// `_`, leading and trailing spaces and dots are trimmed, the result is capped at
// 200 bytes on a UTF-8 boundary, and an empty value falls back to `_`.
//
// The fallback is per segment rather than per path: a missing studio must leave the
// directory level it occupies in place (`_/ABC-123`), not collapse the layout.
func sanitiseSegment(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f:
			b.WriteByte('_')
		case strings.ContainsRune(illegalSegmentChars, r):
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), " .")
	if out == "" {
		return "_"
	}
	if len(out) > maxSegmentBytes {
		out = out[:maxSegmentBytes]
		for len(out) > 0 && !utf8.ValidString(out) {
			out = out[:len(out)-1]
		}
		out = strings.TrimRight(out, " .")
		if out == "" {
			return "_"
		}
	}
	return out
}

// languageToken matches a subtitle's language suffix: a dot followed by a short
// language name, optionally with a region (`zh`, `zh-cn`, `eng`, `jpn`).
var languageToken = regexp.MustCompile(`(?i)^\.[a-z]{2,3}(?:[-_][a-z0-9]{2,4})?$`)

// splitLanguageToken separates a name's trailing language token from the rest:
// `ABC-123.zh` → (".zh", "ABC-123"). No trailing dot-token, or a token that does
// not look like a language, is left alone: `.2024` and `.cd1` are not languages.
func splitLanguageToken(stem string) (token, base string) {
	i := strings.LastIndexByte(stem, '.')
	if i <= 0 {
		return "", stem
	}
	token = stem[i:]
	if !languageToken.MatchString(token) {
		return "", stem
	}
	return token, stem[:i]
}
