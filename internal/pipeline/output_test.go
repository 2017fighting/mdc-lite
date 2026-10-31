package pipeline

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2017fighting/mdc-lite/internal/config"
	"github.com/2017fighting/mdc-lite/internal/field"
	"github.com/2017fighting/mdc-lite/internal/meta"
)

// This file pins the naming contract of ticket #38: `{number}[-cd{part}][-中文][-破解]`
// — the number first, then the part, then the version. The order is the rule's, not
// the source name's, so `ABC-123-UC.mp4` and `ABC-123-CU.mp4` are one name, and a
// work's name never depends on how its source file happened to be spelled.

// defaultOutputStep is the step a default-configured pipeline builds.
func defaultOutputStep(t *testing.T, root string, tweak func(*config.OutputWith)) *outputStep {
	t.Helper()
	w := config.OutputDefaults()
	w.Root = root
	if tweak != nil {
		tweak(&w)
	}
	step, err := newOutput(w, config.OnErrorFail)
	if err != nil {
		t.Fatalf("newOutput: %v", err)
	}
	return step.(*outputStep)
}

// completeMetadata satisfies the default required_fields, so the naming tests never
// trip the gate on their way to the name.
func completeMetadata() meta.Metadata {
	return meta.Metadata{Sources: map[field.Name]string{
		field.Title:  "fixture",
		field.Fanart: "fixture",
		field.Poster: "fixture",
	}}
}

// partOf is one part of a work as `parse` and `group` hand it to `output`.
func partOf(dir, name string, marker Marker, flags VersionFlags) Part {
	ext := strings.ToLower(filepath.Ext(name))
	return Part{
		File: FileRef{
			Path: filepath.Join(filepath.FromSlash(dir), name),
			Ext:  ext,
			Kind: KindVideo,
		},
		Stem:   strings.TrimSuffix(name, ext),
		Marker: marker,
		Flags:  flags,
	}
}

// groupOf is one work: the number it parsed to and the parts that carry it.
func groupOf(number string, parts ...Part) Group {
	grp := Group{
		Number: Number{Raw: number, Normalized: number, Family: "generic"},
		Parts:  parts,
	}
	grp.Primary = parts[0].File
	grp.PrimaryStem = parts[0].Stem
	grp.Dir = filepath.Dir(parts[0].File.Path)
	return grp
}

// planOutput runs the output step over groups as `scrape` would hand them over and
// returns the plan it made.
func planOutput(t *testing.T, step *outputStep, groups ...Group) *Plan {
	t.Helper()
	st := &State{
		Parsed:  &Parsed{},
		Scraped: &Scraped{},
	}
	for _, grp := range groups {
		st.Scraped.Items = append(st.Scraped.Items, ScrapedItem{
			Group:    grp,
			Number:   grp.Number,
			Metadata: completeMetadata(),
		})
	}
	if err := step.Run(context.Background(), st); err != nil {
		t.Fatalf("output: %v", err)
	}
	return st.Plan
}

// videoNames is the plan's video names, in plan order, without their directory.
func videoNames(plan *Plan) []string {
	var out []string
	for _, w := range plan.Writes() {
		if w.Kind == WriteVideo {
			out = append(out, filepath.Base(w.Dst))
		}
	}
	return out
}

func TestOutput_NameIsPartThenVersion(t *testing.T) {
	cases := []struct {
		name   string
		source string
		marker Marker
		flags  VersionFlags
		want   string
	}{
		{
			name:   "a single file keeps the number and nothing else",
			source: "ABC-123.mp4",
			want:   "ABC-123.mp4",
		},
		{
			name:   "an explicit part marker follows the number",
			source: "ABC-123-cd1.mp4",
			marker: Marker{Text: "cd1", Index: 1, Confident: true},
			want:   "ABC-123-cd1.mp4",
		},
		{
			name:   "a chinese copy says 中文",
			source: "ABC-123-C.mp4",
			flags:  VersionFlags{IsChinese: true},
			want:   "ABC-123-中文.mp4",
		},
		{
			name:   "an uncensored copy says 破解",
			source: "ABC-123-U.mp4",
			flags:  VersionFlags{IsUncensored: true},
			want:   "ABC-123-破解.mp4",
		},
		{
			name:   "a cracked chinese copy says 中文 then 破解",
			source: "ABC-123-UC.mp4",
			flags:  VersionFlags{IsChinese: true, IsUncensored: true},
			want:   "ABC-123-中文-破解.mp4",
		},
		{
			name:   "a marked chinese part puts the part first",
			source: "ABC-123-cd2-C.mp4",
			marker: Marker{Text: "cd2", Index: 2, Confident: true},
			flags:  VersionFlags{IsChinese: true},
			want:   "ABC-123-cd2-中文.mp4",
		},
		{
			// The version order is the rule's, not the name's: both spellings of
			// "chinese and uncensored" are one file in the library.
			name:   "the version order does not depend on how the name spells it",
			source: "ABC-123-CU.mp4",
			flags:  VersionFlags{IsChinese: true, IsUncensored: true},
			want:   "ABC-123-中文-破解.mp4",
		},
		{
			// The name comes from the number, never from the source's own spelling:
			// that is what makes two downloads of one work one entry.
			name:   "the source's spelling does not reach the library",
			source: "abc00123-cd1.mp4",
			marker: Marker{Text: "cd1", Index: 1, Confident: true},
			want:   "ABC-123-cd1.mp4",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			step := defaultOutputStep(t, t.TempDir(), nil)
			grp := groupOf("ABC-123", partOf("/media", c.source, c.marker, c.flags))
			got := videoNames(planOutput(t, step, grp))
			if len(got) != 1 || got[0] != c.want {
				t.Fatalf("name = %v, want [%s]", got, c.want)
			}
		})
	}
}

// TestOutput_AnUnmarkedPartKeepsTheBareName is the other half of the part rule: a
// `-cd{n}` appears only when the name said so. `ABC-123.mp4` next to
// `ABC-123-cd2.mp4` is part 1 of one work, and it is still `ABC-123.mp4` — the same
// name it would have had on its own.
func TestOutput_AnUnmarkedPartKeepsTheBareName(t *testing.T) {
	step := defaultOutputStep(t, t.TempDir(), nil)
	grp := groupOf("ABC-123",
		partOf("/media", "ABC-123.mp4", Marker{}, VersionFlags{}),
		partOf("/media", "ABC-123-cd2.mp4", Marker{Text: "cd2", Index: 2, Confident: true}, VersionFlags{}),
	)
	got := videoNames(planOutput(t, step, grp))
	want := []string{"ABC-123.mp4", "ABC-123-cd2.mp4"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("names = %v, want %v", got, want)
	}
}

// TestOutput_EveryVersionLandsInOneDirectoryUnderOneName pins the sharing half of
// the ticket: the parts and versions of one number are hard links in the work's own
// directory, beside a single `movie.nfo` and a single set of images.
func TestOutput_EveryVersionLandsInOneDirectoryUnderOneName(t *testing.T) {
	root := t.TempDir()
	step := defaultOutputStep(t, root, nil)
	grp := groupOf("ABC-123",
		partOf("/media/one", "ABC-123.mp4", Marker{}, VersionFlags{}),
		partOf("/media/two", "ABC-123-C.mp4", Marker{}, VersionFlags{IsChinese: true}),
		partOf("/media/three", "ABC-123-UC.mp4", Marker{}, VersionFlags{IsChinese: true, IsUncensored: true}),
	)
	st := &State{
		Parsed: &Parsed{},
		NfoDoc: &NfoDoc{Items: []NfoItem{{
			Group: grp, Number: grp.Number, Metadata: completeMetadata(), Content: "<movie/>",
		}}},
		Images: &Images{Items: []ImagesItem{{
			Group: grp, Number: grp.Number, Metadata: completeMetadata(),
			Files: []ImageFile{
				{Role: field.Poster, Name: "poster.jpg", Ext: ".jpg", Bytes: []byte("poster")},
				{Role: field.Fanart, Name: "fanart.jpg", Ext: ".jpg", Bytes: []byte("fanart")},
			},
		}}},
	}
	if err := step.Run(context.Background(), st); err != nil {
		t.Fatalf("output: %v", err)
	}

	dir := filepath.Join(root, "ABC-123")
	if len(st.Plan.Items) != 1 || st.Plan.Items[0].Dir != dir {
		t.Fatalf("one work, one directory: %+v", st.Plan.Items)
	}
	kinds := map[WriteKind][]string{}
	for _, w := range st.Plan.Writes() {
		if filepath.Dir(w.Dst) != dir {
			t.Errorf("%s is outside the work's directory %s", w.Dst, dir)
		}
		kinds[w.Kind] = append(kinds[w.Kind], filepath.Base(w.Dst))
	}
	if got := strings.Join(kinds[WriteVideo], ","); got != "ABC-123.mp4,ABC-123-中文.mp4,ABC-123-中文-破解.mp4" {
		t.Errorf("videos = %s", got)
	}
	if got := strings.Join(kinds[WriteNfo], ","); got != "movie.nfo" {
		t.Errorf("the whole group shares one movie.nfo, got %v", got)
	}
	if got := strings.Join(kinds[WriteImage], ","); got != "poster.jpg,fanart.jpg" {
		t.Errorf("the whole group shares one set of images, got %v", got)
	}
	// Every video write is a hard link to its own source (the default `link`), so
	// three versions cost one copy each and not a re-copy of the library.
	for _, w := range st.Plan.Writes() {
		if w.Kind != WriteVideo {
			continue
		}
		if w.Mode != ModeHardlink || w.Path != w.Src || w.Src == "" {
			t.Errorf("%s: mode = %q src = %q path = %q", w.Dst, w.Mode, w.Src, w.Path)
		}
	}
}

// TestOutput_TheTemplateCanSpellPartAndVersionItsOwnWay keeps the configuration
// surface honest: the naming rule is the default `file_template`, and a template
// that spells the part and the version its own way wins for the spelling — the
// evidence (a marked part, a version) is still the parse's, not the template's to
// invent.
func TestOutput_TheTemplateCanSpellPartAndVersionItsOwnWay(t *testing.T) {
	step := defaultOutputStep(t, t.TempDir(), func(w *config.OutputWith) {
		w.FileTemplate = "{{ number }}{% if part %}-part{{ part }}{% endif %}{% if chinese %}-中字{% endif %}"
	})
	grp := groupOf("ABC-123", partOf("/media", "ABC-123-cd2-C.mp4",
		Marker{Text: "cd2", Index: 2, Confident: true}, VersionFlags{IsChinese: true}))
	got := videoNames(planOutput(t, step, grp))
	if len(got) != 1 || got[0] != "ABC-123-part2-中字.mp4" {
		t.Fatalf("name = %v, want the template's own spelling", got)
	}
}
