package pipeline

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2017fighting/mdc-lite/internal/config"
	"github.com/2017fighting/mdc-lite/internal/field"
	"github.com/2017fighting/mdc-lite/internal/meta"
	"github.com/2017fighting/mdc-lite/internal/source"
)

// fakeSource is the fixture source of this ticket: a test double that answers with
// whatever fields the test set up, and serves image bytes it already holds. It is a
// source like any other — same interface, same three-state result — so the chain it
// drives is the production chain.
type fakeSource struct {
	id       string
	declared []field.Name

	scrapeErr error
	found     bool
	fields    source.Fields

	images   map[string][]byte
	fetchErr map[string]bool

	// scrapes counts the calls, so a test can assert that a source was (or was not)
	// asked at all.
	scrapes int
}

func newFakeSource() *fakeSource {
	return &fakeSource{
		id:       "fixture",
		declared: field.All(),
		found:    true,
		images:   map[string][]byte{},
		fetchErr: map[string]bool{},
	}
}

func (f *fakeSource) ID() string { return f.id }

func (f *fakeSource) Fields() []field.Name { return f.declared }

func (f *fakeSource) Scrape(_ context.Context, _ string) (source.Result, error) {
	f.scrapes++
	if f.scrapeErr != nil {
		return source.Result{}, f.scrapeErr
	}
	return source.Result{Found: f.found, Fields: f.fields}, nil
}

func (f *fakeSource) FetchImage(_ context.Context, ref source.ImageRef) ([]byte, error) {
	if f.fetchErr[ref.URL] {
		return nil, errors.New("fixture source: image is a 404")
	}
	data, ok := f.images[ref.URL]
	if !ok {
		return nil, errors.New("fixture source: no such image")
	}
	return data, nil
}

// jpegImage is a real image, because the poster crop decodes what it cuts.
func jpegImage(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 255 / w), G: uint8(y * 255 / h), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// pngImage is a GIF/PNG whose whitelisted extension must survive untouched.
func pngImage(t *testing.T) []byte {
	t.Helper()
	// A 1x1 GIF is enough: the engine only sniffs the first bytes.
	return []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\x00\x00\x00\xff\xff\xff!\xf9\x04\x01\x00\x00\x00\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;")
}

const chainPipeline = `steps:
  - type: scan
    with: {roots: [/media]}
  - type: filter
    with:
      rules:
        - name: companions
          when: {ext_in: [".srt", ".ass", ".ssa", ".jpg", ".png"]}
          then: include
        - name: too-small
          when: {min_size: 100}
          then: exclude
        - name: videos
          when: {ext_in: [".mp4"]}
          then: include
        - name: everything-else
          when: {stem_matches: "^.*$"}
          then: exclude
  - type: group
    with: {companion_extensions: [".srt", ".ass", ".ssa", ".jpg", ".png"]}
  - type: parse
    with:
      rules:
        - {builtin: default}
  - type: scrape
    with: {sources: [fixture]}
  - type: images
    with: {}
  - type: nfo
    with: {template: default}
  - type: output
    with:
      root: OUTPUT_ROOT
      link: hardlink
`

func chainConfig(t *testing.T, root, extra string) config.Pipeline {
	t.Helper()
	body := strings.ReplaceAll(chainPipeline, "OUTPUT_ROOT", root)
	if extra != "" {
		body = strings.ReplaceAll(body, "      link: hardlink", extra)
	}
	p, err := config.ParsePipeline("/config/pipelines/main.yaml", []byte(body), config.DefaultStepRegistry(), nil)
	if err != nil {
		t.Fatalf("the test pipeline does not parse: %v", err)
	}
	return p
}

// runChain runs the whole pipeline over root and applies its plan, which is what a
// task run does — minus the database.
func runChain(t *testing.T, media, out string, opts Options, extra string) (*Result, *Manifest) {
	t.Helper()
	opts.Input = Input{Roots: []string{media}}
	opts.Env = Environment{Sources: source.NewRegistry(opts.Env.Sources["fixture"])}
	engine, err := Build(chainConfig(t, out, extra), opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	res, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if opts.DryRun {
		return res, nil
	}
	manifest, err := Apply(context.Background(), res.State.Plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return res, manifest
}

// standardFixture is the ticket's corpus: one two-part work, a subtitle that follows
// the primary and keeps its language token, a companion image that keeps its name,
// and a fixture source with a title, a plot that needs escaping, and a fanart.
func standardFixture(t *testing.T) (media string, src *fakeSource) {
	t.Helper()
	media = t.TempDir()
	writeFile(t, filepath.Join(media, "ABC-123-cd1.mp4"), 200)
	writeFile(t, filepath.Join(media, "ABC-123-cd2.mp4"), 200)
	writeFile(t, filepath.Join(media, "ABC-123.zh.srt"), 10)
	writeFile(t, filepath.Join(media, "ABC-123.jpg"), 10)

	src = newFakeSource()
	title, plot, releasedate, publisher, series, studio := "My & Title", `A & B <tag> "quoted"`, "2024-01-02", "Maker", "Series", "Studio"
	runtime, rating := 120, 8.5
	src.fields = source.Fields{
		Title:        &title,
		Plot:         &plot,
		Releasedate:  &releasedate,
		Publisher:    &publisher,
		Series:       &series,
		Studio:       &studio,
		Runtime:      &runtime,
		Rating:       &rating,
		Tags:         []string{"tag1", "tag2"},
		Director:     []string{"Director A"},
		Actors:       []meta.Actor{{Name: "Actor A", Gender: meta.Female}, {Name: "Actor B", Gender: meta.Male}},
		Fanart:       &source.ImageRef{Role: field.Fanart, URL: "https://fixture/fanart.jpg"},
		Extrafanarts: []source.ImageRef{{Role: field.Extrafanarts, Index: 0, URL: "https://fixture/e1.jpg"}},
	}
	src.images["https://fixture/fanart.jpg"] = jpegImage(t, 160, 90)
	src.images["https://fixture/e1.jpg"] = jpegImage(t, 32, 18)
	return media, src
}

func TestChain_WritesTheDirectoryAJellyfinServerCanRead(t *testing.T) {
	media, src := standardFixture(t)
	out := t.TempDir()
	res, manifest := runChain(t, media, out, Options{Env: Environment{Sources: source.NewRegistry(src)}}, "")

	dir := filepath.Join(out, "ABC-123")
	nfoPath := filepath.Join(dir, "movie.nfo")
	nfo, err := os.ReadFile(nfoPath)
	if err != nil {
		t.Fatalf("movie.nfo: %v", err)
	}
	got := string(nfo)
	for _, want := range []string{
		"<title>ABC-123 My &amp; Title</title>",
		"<originaltitle>My &amp; Title</originaltitle>",
		"<sorttitle>My &amp; Title cd1</sorttitle>",
		"<plot>A &amp; B &lt;tag&gt; &#34;quoted&#34;</plot>",
		"<premiered>2024-01-02</premiered>",
		"<releasedate>2024-01-02</releasedate>",
		"<runtime>120</runtime>",
		"<rating>8.5</rating>",
		"<set>\n    <name>Series</name>\n  </set>",
		"<studio>Studio</studio>",
		"<tag>发行: Maker</tag>",
		"<tag>tag1</tag>",
		"<tag>tag2</tag>",
		"<name>Actor A</name>",
		"<role>女优</role>",
		"<role>男优</role>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("movie.nfo does not contain %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"<outline>", "<genre>", "<thumb>", "<dateadded>"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("movie.nfo must not write %s:\n%s", unwanted, got)
		}
	}

	// The videos follow `{number}-cd{part}`, because this work is in two parts.
	for _, name := range []string{"ABC-123-cd1.mp4", "ABC-123-cd2.mp4"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("video %s: %v", name, err)
		}
	}
	// The subtitle follows the primary — whose name changed — and keeps its language.
	if _, err := os.Stat(filepath.Join(dir, "ABC-123-cd1.zh.srt")); err != nil {
		t.Errorf("the subtitle must follow the renamed primary: %v", err)
	}
	// The fanart is named by its field, not by the source's name.
	if _, err := os.Stat(filepath.Join(dir, "fanart.jpg")); err != nil {
		t.Errorf("fanart.jpg: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "extrafanart", "fanart1.jpg")); err != nil {
		t.Errorf("extrafanart/fanart1.jpg (1-based): %v", err)
	}
	// The companion image keeps its own name.
	if _, err := os.Stat(filepath.Join(dir, "ABC-123.jpg")); err != nil {
		t.Errorf("an image companion keeps its name: %v", err)
	}

	// No poster came from the source, so the fanart's right side was cut: a 2:3
	// poster out of a 160x90 image is 60x90.
	posterPath := filepath.Join(dir, "poster.jpg")
	poster, err := os.Open(posterPath)
	if err != nil {
		t.Fatalf("poster.jpg must be cropped from the fanart: %v", err)
	}
	defer func() { _ = poster.Close() }()
	cfg, err := jpeg.DecodeConfig(poster)
	if err != nil {
		t.Fatalf("poster.jpg is not a JPEG: %v", err)
	}
	if cfg.Width != 60 || cfg.Height != 90 {
		t.Errorf("poster.jpg is %dx%d, want a 2:3 60x90 crop", cfg.Width, cfg.Height)
	}

	// The crop is traced the way ADR-0015 says: the source plus "(自动裁切)".
	item := res.State.Plan.Items[0]
	if got := item.Metadata.Source(field.Poster); got != "fixture (自动裁切)" {
		t.Errorf("Sources[poster] = %q", got)
	}
	if item.Metadata.Partial {
		t.Errorf("nothing is missing, so the task is not partial: %v", item.Metadata.Missing)
	}
	if !item.Metadata.Derived.PosterCrop {
		t.Error("the poster crop must be visible in Derived")
	}
	// Every write has a ledger row, and the poster's `src` is the fanart's URL.
	var posterRow *Row
	for i := range manifest.Rows {
		row := &manifest.Rows[i]
		if row.Role == RolePoster {
			posterRow = row
		}
	}
	if posterRow == nil {
		t.Fatal("the ledger must have a poster row")
	}
	if posterRow.Src != "https://fixture/fanart.jpg" || posterRow.Kind != string(WriteImage) || posterRow.Action != ActionCreated {
		t.Errorf("poster row = %+v", posterRow)
	}
}

func TestChain_DryRunPlansButWritesNothing(t *testing.T) {
	media, src := standardFixture(t)
	out := filepath.Join(t.TempDir(), "library")
	res, _ := runChain(t, media, out, Options{DryRun: true, Env: Environment{Sources: source.NewRegistry(src)}}, "")

	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("--dry-run created the output directory: %v", err)
	}
	if res.State.Plan == nil || len(res.State.Plan.Writes()) == 0 {
		t.Fatal("--dry-run must still produce the plan")
	}
	if res.State.Manifest != nil {
		t.Error("--dry-run applied something")
	}
}

func TestChain_APosterFromTheSourceIsNotCropped(t *testing.T) {
	media, src := standardFixture(t)
	src.fields.Poster = &source.ImageRef{Role: field.Poster, URL: "https://fixture/poster.png"}
	src.images["https://fixture/poster.png"] = pngImage(t)
	out := t.TempDir()
	res, _ := runChain(t, media, out, Options{Env: Environment{Sources: source.NewRegistry(src)}}, "")

	dir := filepath.Join(out, "ABC-123")
	if _, err := os.Stat(filepath.Join(dir, "poster.gif")); err != nil {
		t.Errorf("a whitelisted extension is kept as-is (gif): %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "poster.jpg")); !os.IsNotExist(err) {
		t.Error("a source-supplied poster must not be replaced by a crop")
	}
	md := res.State.Plan.Items[0].Metadata
	if md.Derived.PosterCrop || md.Source(field.Poster) != "fixture" {
		t.Errorf("poster provenance = %q crop=%v", md.Source(field.Poster), md.Derived.PosterCrop)
	}
}

func TestChain_ACropThatFailsLeavesThePosterMissingAndTheTaskPartial(t *testing.T) {
	media, src := standardFixture(t)
	// webp has no decoder in the standard library, so the crop cannot happen.
	src.images["https://fixture/fanart.jpg"] = []byte("RIFF\x00\x00\x00\x00WEBPVP8 ")
	out := t.TempDir()
	res, _ := runChain(t, media, out, Options{Env: Environment{Sources: source.NewRegistry(src)}}, "")

	md := res.State.Plan.Items[0].Metadata
	if md.Source(field.Poster) != "" {
		t.Errorf("a failed crop must not claim a poster: %q", md.Source(field.Poster))
	}
	if !containsField(md.Missing, field.Poster) {
		t.Errorf("poster must stay in Missing: %v", md.Missing)
	}
	if !md.Partial {
		t.Error("a missing poster makes the task partial")
	}
	if _, err := os.Stat(filepath.Join(out, "ABC-123", "poster.jpg")); !os.IsNotExist(err) {
		t.Error("no poster file may appear when the crop failed")
	}
	// It wrote everything else: a crop failure is not a task failure.
	if _, err := os.Stat(filepath.Join(out, "ABC-123", "movie.nfo")); err != nil {
		t.Errorf("movie.nfo: %v", err)
	}
}

func TestChain_ARequiredFieldDecidesWhetherTheStepFails(t *testing.T) {
	media, src := standardFixture(t)
	// No title: the default required_fields are [title, fanart].
	src.fields.Title = nil

	_, err := build1(t, media, t.TempDir(), Options{Env: Environment{Sources: source.NewRegistry(src)}}, "")
	if err == nil || !strings.Contains(err.Error(), "missing required field(s) title") {
		t.Fatalf("a missing required field must fail the step, got %v", err)
	}

	// With `on_error: continue` the same run writes what it has and ends partial.
	out := t.TempDir()
	res, _ := runChain(t, media, out, Options{Env: Environment{Sources: source.NewRegistry(src)}},
		"      link: hardlink\n    on_error: continue")
	md := res.State.Plan.Items[0].Metadata
	if !md.Partial || !containsField(md.Missing, field.Title) {
		t.Errorf("with on_error: continue the run goes partial instead: %v", md.Missing)
	}
	if res.State.OutputRun.Missing["title"] != 1 {
		t.Errorf("the step's summary must name the missing required field: %+v", res.State.OutputRun)
	}
	if _, err := os.Stat(filepath.Join(out, "ABC-123", "movie.nfo")); err != nil {
		t.Errorf("on_error: continue still writes: %v", err)
	}
}

// build1 runs the pipeline without applying, returning the commit error (the
// required-fields failure happens while planning).
func build1(t *testing.T, media, out string, opts Options, extra string) (*Result, error) {
	t.Helper()
	opts.Input = Input{Roots: []string{media}}
	opts.Env = Environment{Sources: source.NewRegistry(opts.Env.Sources["fixture"])}
	engine, err := Build(chainConfig(t, out, extra), opts)
	if err != nil {
		return nil, err
	}
	return engine.Run(context.Background())
}

func TestChain_SegmentsNeverInventDirectories(t *testing.T) {
	media, src := standardFixture(t)
	src.fields.Studio = stringPtr("../escape/../etc")
	out := t.TempDir()
	// The directory template is given a studio level: the studio's slashes must not
	// become directory levels.
	_, _ = runChain(t, media, out, Options{Env: Environment{Sources: source.NewRegistry(src)}},
		"      link: hardlink\n      dir_template: \"{{ studio }}/{{ number }}\"")

	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		t.Fatalf("one directory level was expected, got %v", entries)
	}
	if entries[0].Name() != ".._escape_.._etc" && entries[0].Name() != "_.._escape_.._etc" {
		// Either trimming shape is fine, as long as no second level appeared.
		if strings.Contains(entries[0].Name(), "/") {
			t.Errorf("directory name %q contains a separator", entries[0].Name())
		}
	}
	if _, err := os.Stat(filepath.Join(out, "ABC-123")); err == nil {
		t.Error("the studio value must not have collapsed the number level")
	}
}

func TestChain_CompanionsSkipWhenTheBytesAreAlreadyThere(t *testing.T) {
	media, src := standardFixture(t)
	out := t.TempDir()
	_, first := runChain(t, media, out, Options{Env: Environment{Sources: source.NewRegistry(src)}}, "")
	if len(first.Rows) == 0 {
		t.Fatal("no rows")
	}
	path := filepath.Join(out, "ABC-123", "ABC-123-cd1.zh.srt")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	_, second := runChain(t, media, out, Options{Env: Environment{Sources: source.NewRegistry(src)}}, "")
	for _, row := range second.Rows {
		if row.Dst == path && row.Action != ActionSkipped {
			t.Errorf("an unchanged companion must be skipped, got %q", row.Action)
		}
	}
	again, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !again.ModTime().Equal(info.ModTime()) {
		t.Error("a skipped companion must keep its mtime")
	}
}

func TestApply_HardlinkAcrossFilesystemsFailsWithTheFix(t *testing.T) {
	other := "/dev/shm"
	if _, err := os.Stat(other); err != nil {
		t.Skip("/dev/shm is not available for a real EXDEV")
	}
	media := t.TempDir()
	src := filepath.Join(media, "ABC-123.mp4")
	writeFile(t, src, 200)
	dst, err := os.MkdirTemp(other, "mdc-exdev-")
	if err != nil {
		t.Skipf("cannot create on %s: %v", other, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dst) })

	target := filepath.Join(dst, "ABC-123.mp4")
	// The previous run's product is already there. A failed link must not take it
	// away: the failure is the run's, not the library's.
	if err := os.WriteFile(target, []byte("the previous run's file"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := &Plan{Items: []PlanItem{{Writes: []PlannedWrite{{
		Kind: WriteVideo, Role: RoleVideo, Mode: ModeHardlink, Src: src, Path: src,
		Dst: target,
	}}}}}
	_, err = Apply(context.Background(), plan)
	if err == nil {
		t.Fatal("a hardlink across filesystems must fail")
	}
	for _, want := range []string{"not on the same filesystem", "copy or move", "EXDEV"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	kept, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("the previous output must survive a failed link: %v", readErr)
	}
	if string(kept) != "the previous run's file" {
		t.Errorf("the previous output was replaced: %q", kept)
	}
	if entries, _ := os.ReadDir(dst); len(entries) != 1 {
		t.Errorf("a failed link left a temp file behind: %v", entries)
	}
}

func TestApply_LeavesNoTempFileBehind(t *testing.T) {
	out := t.TempDir()
	plan := &Plan{Items: []PlanItem{{Writes: []PlannedWrite{{
		Kind: WriteImage, Role: RolePoster, Mode: ModeBytes, Dst: filepath.Join(out, "poster.jpg"), Data: []byte("bytes"),
	}}}}}
	if _, err := Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("a temp file is still there: %s", e.Name())
		}
	}
}

func containsField(fields []field.Name, want field.Name) bool {
	for _, f := range fields {
		if f == want {
			return true
		}
	}
	return false
}

func stringPtr(s string) *string { return &s }

func TestChain_ACompanionFollowsItsOwnPart(t *testing.T) {
	media := t.TempDir()
	writeFile(t, filepath.Join(media, "ABC-123-cd1.mp4"), 200)
	writeFile(t, filepath.Join(media, "ABC-123-cd2.mp4"), 200)
	// The subtitle names the second part, so it must follow that part's new name.
	writeFile(t, filepath.Join(media, "ABC-123-cd2.eng.srt"), 10)
	src := newFakeSource()
	title := "Title"
	src.fields.Title = &title
	src.fields.Fanart = &source.ImageRef{Role: field.Fanart, URL: "https://fixture/fanart.jpg"}
	src.images["https://fixture/fanart.jpg"] = jpegImage(t, 160, 90)

	out := t.TempDir()
	runChain(t, media, out, Options{Env: Environment{Sources: source.NewRegistry(src)}}, "")
	if _, err := os.Stat(filepath.Join(out, "ABC-123", "ABC-123-cd2.eng.srt")); err != nil {
		t.Errorf("the subtitle must follow cd2 and keep .eng: %v", err)
	}
}

// TestApply_DoesNotStartAFileAfterTheContextDied is the cancellation rule: the
// context is checked *between* files, so a run that was called off writes nothing
// further and never leaves a half-written file or a `.tmp` behind. The check is at
// the boundary on purpose; a file being copied is not interrupted mid-stream.
func TestApply_DoesNotStartAFileAfterTheContextDied(t *testing.T) {
	out := t.TempDir()
	plan := &Plan{Items: []PlanItem{{Writes: []PlannedWrite{
		{Kind: WriteImage, Role: RolePoster, Mode: ModeBytes, Dst: filepath.Join(out, "poster.jpg"), Data: []byte("poster")},
		{Kind: WriteNfo, Role: RoleNfo, Mode: ModeBytes, Dst: filepath.Join(out, "movie.nfo"), Data: []byte("<movie/>")},
	}}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Apply(ctx, plan); !errors.Is(err, context.Canceled) {
		t.Fatalf("Apply = %v, want context.Canceled", err)
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("a cancelled apply wrote files: %v", entries)
	}
}
