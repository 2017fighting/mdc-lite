package pipeline

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/2017fighting/mdc-lite/internal/field"
)

func TestSanitiseSegment_KeepsAnOrdinaryName(t *testing.T) {
	for _, name := range []string{"ABC-123", "FC2-PPV-3336019", "标题 与 空格", "..hidden"} {
		if got := sanitiseSegment(name); got == "_" {
			t.Errorf("sanitiseSegment(%q) collapsed an ordinary name", name)
		}
	}
}

func TestSanitiseSegment_ReplacesIllegalAndControlCharacters(t *testing.T) {
	got := sanitiseSegment(`a/b\c:d*e?f"g<h>i|j` + "\x01k")
	want := `a_b_c_d_e_f_g_h_i_j_k`
	if got != want {
		t.Errorf("sanitiseSegment = %q, want %q", got, want)
	}
}

func TestSanitiseSegment_TrimsOuterSpacesAndDots(t *testing.T) {
	if got := sanitiseSegment("  ..ABC-123.. "); got != "ABC-123" {
		t.Errorf("got %q", got)
	}
	// A name that is nothing but dots and spaces is not a path segment at all.
	if got := sanitiseSegment(" ... "); got != "_" {
		t.Errorf("got %q, want the empty-value fallback", got)
	}
}

func TestSanitiseSegment_CapsAtTwoHundredBytesOnAUTF8Boundary(t *testing.T) {
	long := strings.Repeat("标", 150) // 450 bytes of valid UTF-8
	got := sanitiseSegment(long)
	if len(got) > maxSegmentBytes {
		t.Errorf("len = %d, want <= %d", len(got), maxSegmentBytes)
	}
	if !utf8.ValidString(got) {
		t.Error("the cap cut a rune in half")
	}
}

func TestSplitTemplateSegments_OnlyTheTemplatesSlashesSplit(t *testing.T) {
	if got := splitTemplateSegments("{{ number }}/{{ studio }}"); len(got) != 2 {
		t.Errorf("segments = %q, want two", got)
	}
	if got := splitTemplateSegments(`{{ a | replace("/", "-") }}`); len(got) != 1 {
		t.Errorf("a slash inside a filter must not split: %q", got)
	}
}

func TestSanitiseSegment_ValuesCannotInventDirectoryLevels(t *testing.T) {
	// The rendered value contains a slash, but the template did not: one segment.
	r, err := newPathRenderer("{{ studio }}", "{{ number }}")
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.RenderDir(map[string]any{"studio": "A/B"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "/") {
		t.Errorf("rendered directory %q gained a level from a value", got)
	}
}

func TestImageName(t *testing.T) {
	for _, c := range []struct {
		role  field.Name
		index int
		ext   string
		want  string
	}{
		{field.Poster, 0, "jpg", "poster.jpg"},
		{field.Fanart, 0, "png", "fanart.png"},
		{field.Extrafanarts, 0, "jpg", "extrafanart/fanart1.jpg"},
		{field.Extrafanarts, 2, "jpg", "extrafanart/fanart3.jpg"},
	} {
		if got := imageName(c.role, c.index, c.ext); got != c.want {
			t.Errorf("imageName(%s,%d,%s) = %q, want %q", c.role, c.index, c.ext, got, c.want)
		}
	}
}

func TestSniffImageExt(t *testing.T) {
	cases := []struct {
		want string
		data []byte
	}{
		{"jpg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00}},
		{"png", []byte("\x89PNG\r\n\x1a\nrest")},
		{"gif", []byte("GIF89a...")},
		// webp is not on the whitelist: the file is named .jpg, its bytes untouched.
		{"jpg", []byte("RIFF\x00\x00\x00\x00WEBPVP8 ")},
	}
	for _, c := range cases {
		if got := sniffImageExt(c.data); got != c.want {
			t.Errorf("sniff(%q) = %q, want %q", c.data, got, c.want)
		}
	}
	if got := sniffImageExt([]byte("not an image")); got != "jpg" {
		t.Errorf("a sniffing failure falls back to jpg, got %q", got)
	}
}

func TestSplitLanguageToken(t *testing.T) {
	for _, c := range []struct {
		stem, token, base string
	}{
		{"ABC-123.zh", ".zh", "ABC-123"},
		{"ABC-123.zh-cn", ".zh-cn", "ABC-123"},
		{"ABC-123.eng", ".eng", "ABC-123"},
		{"ABC-123", "", "ABC-123"},
		{"ABC-123.2024", "", "ABC-123.2024"},
		{"ABC-123.cd1", "", "ABC-123.cd1"},
	} {
		token, base := splitLanguageToken(c.stem)
		if token != c.token || base != c.base {
			t.Errorf("splitLanguageToken(%q) = (%q, %q), want (%q, %q)", c.stem, token, base, c.token, c.base)
		}
	}
}
