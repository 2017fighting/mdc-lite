package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/2017fighting/mdc-lite/internal/config"
	"github.com/2017fighting/mdc-lite/internal/field"
	"github.com/2017fighting/mdc-lite/internal/pipeline"
	"github.com/2017fighting/mdc-lite/internal/source"
	"github.com/2017fighting/mdc-lite/internal/sources"
)

// runSourceTest is `mdc source test`: one number, one source, printed field by
// field, with the status the run would have recorded.
//
// It goes through the program's own Fetcher and the program's own parser — the same
// code a task uses — so what it prints is what a task would get. The reason the
// command exists is that the alternative is curl plus reading HTML by eye, which
// tests neither the cookie, nor the rate limit, nor the selectors.
func runSourceTest(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mdc source test", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		configPath   string
		pipelinesDir string
		pipelineID   string
		id           string
		number       string
		dump         bool
		dumpDir      string
	)
	fs.StringVar(&configPath, "config", "/config/config.yaml", "path to the process-level config file")
	fs.StringVar(&pipelinesDir, "pipelines-dir", "", "directory holding the pipeline files")
	fs.StringVar(&pipelineID, "pipeline", "", "pipeline whose scrape settings to use (default: the first pipeline with a scrape step)")
	fs.StringVar(&id, "id", "", "source id to test (javbus, …)")
	fs.StringVar(&number, "number", "", "the normalized number to ask about, e.g. SSIS-001")
	fs.BoolVar(&dump, "dump", false, "also write the raw answer into the fixture directory")
	fs.StringVar(&dumpDir, "dump-dir", "", "where --dump writes (default: internal/source/<id>/testdata/captures)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if id == "" || number == "" {
		fmt.Fprintln(stderr, "mdc source test: --id and --number are required")
		return 2
	}

	var flags config.Flags
	if pipelinesDir != "" {
		flags.PipelinesDir = &pipelinesDir
	}
	l, err := loadAllWith(configPath, flags, config.DefaultStepRegistry())
	if err != nil {
		fmt.Fprintf(stderr, "config: %v\n", err)
		return 1
	}
	for _, p := range l.problems {
		fmt.Fprintf(stderr, "refusing to test: %v\n", p.Err)
		return 1
	}
	p, ok := pickPipeline(l.pipelines, pipelineID)
	if !ok {
		fmt.Fprintf(stderr, "mdc source test: no pipeline to take the scrape settings from (have: %s)\n",
			strings.Join(pipelineIDs(l.pipelines), ", "))
		return 1
	}

	with := sources.ScrapeWith(p)
	fetcher := sources.NewFetcher(l.settings)
	// The pipeline's knobs for this source when it listed it, the documented defaults
	// when it did not: a source test is about the source, not about which pipeline
	// happens to select it.
	src, ok := sources.For(with, id, fetcher)
	if !ok {
		fmt.Fprintf(stderr, "mdc source test: %q is not built into this binary (known: %s)\n", id, sources.Known())
		return 1
	}

	ctx := context.Background()
	if budget := time.Duration(with.Timeout); budget > 0 {
		// The same budget a task gets for one number (`scrape.with.timeout`).
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, budget)
		defer cancel()
	}

	started := time.Now()
	res, scrapeErr := src.Scrape(ctx, number)
	elapsed := time.Since(started)

	out := testReport{
		Source: src.ID(),
		Number: number,
		Status: statusOf(res, scrapeErr),
		Reason: source.ReasonOf(scrapeErr),
		Err:    errText(scrapeErr),
		MS:     elapsed.Milliseconds(),
	}
	if res.Found {
		out.Images = fetchImages(ctx, src, res.Fields)
		out.Fields = describeFields(src.Fields(), res.Fields)
		markImageDelivery(out.Fields, out.Images)
	}

	// --dump keeps the raw answer, and only a raw answer: a notfound page is a
	// useful fixture too (it is the positive marker), but a run that never got an
	// answer has nothing to keep.
	if dump {
		dir := dumpDir
		if dir == "" {
			dir = filepath.Join("internal", "source", src.ID(), "testdata", "captures")
		}
		captures, dumpErr := dumpCapture(ctx, src, number, dir)
		if dumpErr != nil {
			fmt.Fprintf(stderr, "mdc source test: --dump: %v\n", dumpErr)
			return 1
		}
		out.Dump = captures
	}

	if err := writeReport(stdout, out); err != nil {
		fmt.Fprintf(stderr, "mdc source test: %v\n", err)
		return 1
	}
	if out.Status == "failed" || out.imageFailed() {
		return 1
	}
	return 0
}

// statusOf is the three-state answer, decided the way `source_runs` decides it: an
// error is failed, no error and no match is notfound, and a match is ok.
func statusOf(res source.Result, err error) string {
	switch {
	case err != nil:
		return "failed"
	case !res.Found:
		return "notfound"
	default:
		return "ok"
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// pickPipeline chooses which pipeline's scrape settings to test with: the one named,
// or the first that has a scrape step, because a source test is about the source and
// the operator should not have to name a pipeline for the common case.
func pickPipeline(pipelines []config.Pipeline, id string) (config.Pipeline, bool) {
	for _, p := range pipelines {
		if id != "" && p.ID != id {
			continue
		}
		if id != "" {
			return p, true
		}
		for _, st := range p.Steps {
			if st.Type == config.StepScrape {
				return p, true
			}
		}
	}
	return config.Pipeline{}, false
}

func pipelineIDs(pipelines []config.Pipeline) []string {
	out := make([]string, len(pipelines))
	for i, p := range pipelines {
		out[i] = p.ID
	}
	return out
}

// testReport is what `mdc source test` measured: the three-state answer, the field
// rows, what each image delivery turned out to be, and where the capture came from.
type testReport struct {
	Source string               `json:"source"`
	Number string               `json:"number"`
	Status string               `json:"status"`
	Reason string               `json:"reason,omitempty"`
	Err    string               `json:"err,omitempty"`
	MS     int64                `json:"ms"`
	Fields []fieldReport        `json:"fields"`
	Images []imageReport        `json:"images,omitempty"`
	Dump   []source.CaptureInfo `json:"dump,omitempty"`
}

// fieldReport is one field's row: what this source declared, what it actually said,
// and whether the two agree. The declaration and the contribution are separate
// columns because "declared but absent" (javbus's `series`) is a normal answer, and
// confusing it with "not declared" would misread the source.
type fieldReport struct {
	Name        field.Name `json:"name"`
	Declared    bool       `json:"declared"`
	Value       string     `json:"value,omitempty"`
	Contributed bool       `json:"contributed"`
	// Note explains why a declared field is not a contribution when the reason is
	// not simply "the page does not have it" — an image whose bytes never arrived.
	Note string `json:"note,omitempty"`
}

// imageReport is one image field's delivery: an image field counts only once the
// bytes are in hand, so the address alone proves nothing and this row is the proof.
type imageReport struct {
	Role  string `json:"role"`
	Index int    `json:"index,omitempty"`
	URL   string `json:"url"`
	Bytes int    `json:"bytes"`
	Kind  string `json:"kind,omitempty"`
	Err   string `json:"err,omitempty"`
	MS    int64  `json:"ms"`
}

// describeFields renders the fourteen source-level fields in spec order, so two
// sources' output can be compared line by line.
func describeFields(declared []field.Name, f source.Fields) []fieldReport {
	isDeclared := map[field.Name]bool{}
	for _, name := range declared {
		isDeclared[name] = true
	}
	out := make([]fieldReport, 0, len(field.All()))
	for _, name := range field.All() {
		value, contributed := describeField(name, f)
		out = append(out, fieldReport{
			Name:        name,
			Declared:    isDeclared[name],
			Value:       value,
			Contributed: contributed,
		})
	}
	return out
}

// markImageDelivery applies the image rule to the field rows: a source that offered
// a cover it could not deliver has contributed nothing, and the row says so instead
// of printing the address as if the bytes were in hand.
func markImageDelivery(fields []fieldReport, images []imageReport) {
	delivered := map[field.Name]bool{}
	attempted := map[field.Name]bool{}
	for _, img := range images {
		role := field.Name(img.Role)
		attempted[role] = true
		if img.Err == "" {
			delivered[role] = true
		}
	}
	for i := range fields {
		name := fields[i].Name
		if !isImageField(name) || delivered[name] || !attempted[name] {
			continue
		}
		fields[i].Contributed = false
		fields[i].Note = "offered, but the bytes never arrived"
	}
}

// isImageField reports whether a source-level field's value is bytes rather than
// text. The list is the engine's (see the images step); this copy exists so the
// report can apply the same rule without exporting the engine's vocabulary.
func isImageField(name field.Name) bool {
	switch name {
	case field.Poster, field.Fanart, field.Extrafanarts:
		return true
	default:
		return false
	}
}

// imageFailed reports whether any image the source offered failed to arrive. The
// meta answer can be perfect while the cover is a 403 page, and that is exactly the
// distinction "fanart is judged by bytes in hand" exists for.
func (r testReport) imageFailed() bool {
	for _, img := range r.Images {
		if img.Err != "" {
			return true
		}
	}
	return false
}

// fetchImages asks the source for every image it offered, in field order.
func fetchImages(ctx context.Context, src source.Source, f source.Fields) []imageReport {
	refs := imageRefs(f)
	out := make([]imageReport, 0, len(refs))
	for _, ref := range refs {
		started := time.Now()
		data, err := src.FetchImage(ctx, ref)
		row := imageReport{
			Role:  string(ref.Role),
			Index: ref.Index,
			URL:   ref.URL,
			Bytes: len(data),
			MS:    time.Since(started).Milliseconds(),
		}
		if err != nil {
			row.Err = err.Error()
		} else {
			row.Kind = describeImageBytes(data)
		}
		out = append(out, row)
	}
	return out
}

func imageRefs(f source.Fields) []source.ImageRef {
	var out []source.ImageRef
	if f.Poster != nil {
		out = append(out, *f.Poster)
	}
	if f.Fanart != nil {
		out = append(out, *f.Fanart)
	}
	return append(out, f.Extrafanarts...)
}

// describeImageBytes names what came back by magic bytes, through the engine's own
// sniffer: a "403 page saved as cover.jpg" is the failure this line is here to show,
// and a second copy of the sniffing rules would eventually disagree with the first.
func describeImageBytes(b []byte) string {
	if len(b) == 0 {
		return "empty"
	}
	if format := pipeline.SniffImageFormat(b); format != "" {
		return format
	}
	preview := b
	if len(preview) > 40 {
		preview = preview[:40]
	}
	return fmt.Sprintf("not an image (%q…)", preview)
}

// dumpCapture writes every raw answer the source took and its manifest entries into
// dir, through the source's own Fetcher: the fixture is the program's bytes, never
// curl's.
//
// One capture keeps the old name (`<id>-<number>.html`); a two-leg source adds the
// leg (`<id>-<number>-search.json`), because a fixture is replayed request by
// request and two files claiming one path would be one file.
func dumpCapture(ctx context.Context, src source.Source, number, dir string) ([]source.CaptureInfo, error) {
	dumper, ok := src.(source.Dumper)
	if !ok {
		return nil, fmt.Errorf("source %s cannot hand back its raw answer, so there is nothing to dump", src.ID())
	}
	captures, err := dumper.Dump(ctx, number)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	stem := fmt.Sprintf("%s-%s", src.ID(), strings.ReplaceAll(number, "/", "_"))
	stamp := time.Now().UTC().Format(time.RFC3339)
	out := make([]source.CaptureInfo, 0, len(captures))
	for _, capture := range captures {
		name := stem + captureExt(capture.Body)
		if capture.Leg != "" {
			name = stem + "-" + capture.Leg + captureExt(capture.Body)
		}
		if err := os.WriteFile(filepath.Join(dir, name), capture.Body, 0o644); err != nil {
			return nil, err
		}
		sum := sha256.Sum256(capture.Body)
		info := source.CaptureInfo{
			Path:     name,
			Leg:      capture.Leg,
			URL:      capture.URL,
			Status:   capture.Status,
			Bytes:    len(capture.Body),
			Captured: stamp,
			SHA256:   hex.EncodeToString(sum[:]),
		}
		if err := writeManifest(filepath.Join(dir, "fixtures.json"), info); err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, nil
}

// captureExt names a capture by what it holds rather than by what the source calls
// it: a JSON answer is `.json`, anything else is the page it came from. The
// extension is a convenience for a human reading the directory; the manifest is
// what the tests trust.
func captureExt(body []byte) string {
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		return ".json"
	}
	return ".html"
}

// writeManifest adds one entry to the directory's manifest, replacing an earlier
// capture of the same path so re-capturing a page does not leave two rows claiming
// the same file.
func writeManifest(path string, info source.CaptureInfo) error {
	existing := []source.CaptureInfo{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &existing); err != nil {
			return fmt.Errorf("%s is not a manifest: %w", path, err)
		}
	}
	kept := make([]source.CaptureInfo, 0, len(existing)+1)
	for _, row := range existing {
		if row.Path != info.Path {
			kept = append(kept, row)
		}
	}
	kept = append(kept, info)
	sort.Slice(kept, func(i, j int) bool { return kept[i].Path < kept[j].Path })
	data, err := json.MarshalIndent(kept, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// writeReport prints the field-by-field answer, then the status line with the
// reason code when there is one.
func writeReport(w io.Writer, r testReport) error {
	fmt.Fprintf(w, "source:  %s\n", r.Source)
	fmt.Fprintf(w, "number:  %s\n", r.Number)
	fmt.Fprintf(w, "declared: %s\n", joinDeclared(r.Fields))
	fmt.Fprintln(w)
	for _, f := range r.Fields {
		switch {
		case f.Contributed:
			fmt.Fprintf(w, "  %-12s %s\n", f.Name, f.Value)
		case f.Note != "":
			fmt.Fprintf(w, "  %-12s — (%s)\n", f.Name, f.Note)
		case f.Declared:
			fmt.Fprintf(w, "  %-12s — (declared, absent on this page)\n", f.Name)
		default:
			fmt.Fprintf(w, "  %-12s — (not declared by this source)\n", f.Name)
		}
	}
	if len(r.Images) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "images:")
		for _, img := range r.Images {
			role := img.Role
			if img.Index > 0 {
				role = fmt.Sprintf("%s[%d]", role, img.Index)
			}
			if img.Err != "" {
				fmt.Fprintf(w, "  %-12s %s — failed: %s\n", role, img.URL, img.Err)
				continue
			}
			fmt.Fprintf(w, "  %-12s %s (%d bytes, %s)\n", role, img.URL, img.Bytes, img.Kind)
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "status:  %s (%d ms)\n", r.Status, r.MS)
	if r.Reason != "" {
		fmt.Fprintf(w, "reason:  %s\n", r.Reason)
	}
	if r.Err != "" {
		fmt.Fprintf(w, "error:   %s\n", r.Err)
	}
	for _, d := range r.Dump {
		fmt.Fprintf(w, "dumped:  %s (%d bytes, HTTP %d, sha256 %s)\n",
			d.Path, d.Bytes, d.Status, shortHash(d.SHA256))
		fmt.Fprintf(w, "from:    %s\n", d.URL)
	}
	_, err := fmt.Fprintln(w)
	return err
}

// shortHash shortens a sha256 for the printed line; the manifest keeps the whole
// value, which is what a fixture check compares.
func shortHash(sum string) string {
	if len(sum) <= 12 {
		return sum
	}
	return sum[:12]
}

func joinDeclared(fields []fieldReport) string {
	var out []string
	for _, f := range fields {
		if f.Declared {
			out = append(out, string(f.Name))
		}
	}
	return strings.Join(out, ", ")
}

// runSourceCommand dispatches the `mdc source` verbs. Only `test` exists in V1.
func runSourceCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "test" {
		fmt.Fprintln(stderr, "mdc source: expected `test`")
		return 2
	}
	return runSourceTest(args[1:], stdout, stderr)
}
