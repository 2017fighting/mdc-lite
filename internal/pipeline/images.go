package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"strings"

	// The decoders the poster crop can need. webp/svg have no standard-library
	// decoder, which is exactly why a crop out of one of them fails and leaves the
	// poster missing instead of failing the task (ADR-0015).
	_ "image/gif"
	_ "image/png"

	"github.com/2017fighting/mdc-lite/internal/config"
	"github.com/2017fighting/mdc-lite/internal/field"
	"github.com/2017fighting/mdc-lite/internal/meta"
)

// imagesStep does the two things V1 asks of it: name the images for the output
// directory, and cut a poster out of the fanart when no source supplied one.
//
// It downloads nothing and calls no source. The bytes are already in hand, because
// `scrape` only marks an image field as contributed once FetchImage succeeded.
type imagesStep struct {
	cfg config.ImagesWith
}

func newImages(w config.ImagesWith) (Step, error) { return &imagesStep{cfg: w}, nil }

func (s *imagesStep) Type() string { return config.StepImages }

func (s *imagesStep) Name() string { return "" }

func (s *imagesStep) Requires() []string { return []string{config.ArtifactScraped} }

func (s *imagesStep) Produces() []string { return []string{config.ArtifactImages} }

func (s *imagesStep) Run(_ context.Context, st *State) error {
	if st.Scraped == nil {
		return fmt.Errorf("no Scraped artifact to turn into images")
	}
	out := &Images{}
	summary := &ImagesSummary{Items: len(st.Scraped.Items), Missing: newCounts()}
	for _, item := range st.Scraped.Items {
		md := item.Metadata
		img := ImagesItem{Group: item.Group, Number: item.Number, Metadata: md}

		for _, blob := range item.Images {
			ext := sniffImageExt(blob.Bytes)
			img.Files = append(img.Files, ImageFile{
				Role:   blob.Role,
				Index:  blob.Index,
				Name:   imageName(blob.Role, blob.Index, ext),
				Ext:    ext,
				Bytes:  blob.Bytes,
				Source: blob.Source,
				URL:    blob.URL,
			})
		}

		var cropped int
		img.Metadata, img.Files, cropped = cropPosterWhenMissing(img.Metadata, img.Files)
		if cropped > 0 {
			summary.Cropped += cropped
			summary.Notes = append(summary.Notes, "从横版封套右侧裁切 poster.jpg (2:3)")
		}

		present := map[field.Name]bool{}
		for _, f := range img.Files {
			present[f.Role] = true
		}
		for _, f := range imageFields {
			if !present[f] {
				// "The source gave a URL but the bytes never arrived" is a missing
				// field, so forget the provenance too: Sources answers "who supplied
				// it", and nobody did.
				img.Metadata.Forget(f)
				summary.Missing[string(f)]++
			}
		}
		img.Missing = append([]field.Name(nil), img.Metadata.Missing...)
		img.Partial = len(img.Missing) > 0
		if img.Partial {
			summary.PartialItems++
		}
		summary.Images += len(img.Files)
		out.Items = append(out.Items, img)
	}
	st.Images = out
	st.ImagesRun = summary
	return nil
}

// cropPosterWhenMissing implements ADR-0015: when the task has no poster and the
// fanart's bytes are in hand, cut a 2:3 poster out of the fanart's right side.
//
// A crop that fails leaves the poster missing — the run goes partial, it does not
// fail: `poster` is not in the default required_fields, and a missing poster is a
// worse-looking library entry, not a failed task.
func cropPosterWhenMissing(md meta.Metadata, files []ImageFile) (meta.Metadata, []ImageFile, int) {
	if md.Has(field.Poster) {
		return md, files, 0
	}
	fanart, ok := findImage(files, field.Fanart)
	if !ok {
		return md, files, 0
	}
	posterBytes, err := cropPoster(fanart.Bytes)
	if err != nil {
		// Still missing: leave the provenance empty so `Missing` keeps poster.
		md.Forget(field.Poster)
		return md, files, 0
	}
	md.Win(field.Poster, fanart.Source+" (自动裁切)")
	md.Derived.PosterCrop = true
	files = append(files, ImageFile{
		Role:    field.Poster,
		Name:    imageName(field.Poster, 0, "jpg"),
		Ext:     "jpg",
		Bytes:   posterBytes,
		Source:  fanart.Source,
		URL:     fanart.URL,
		Cropped: true,
	})
	return md, files, 1
}

func findImage(files []ImageFile, role field.Name) (ImageFile, bool) {
	for _, f := range files {
		if f.Role == role {
			return f, true
		}
	}
	return ImageFile{}, false
}

// cropPoster cuts the right-hand 2:3 slice out of a landscape image and encodes it
// as JPEG. Only the standard library is used: no ffmpeg, no third-party imaging
// package, so the static binary stays small.
func cropPoster(fanart []byte) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(fanart))
	if err != nil {
		return nil, fmt.Errorf("decode fanart: %w", err)
	}
	b := src.Bounds()
	// A 2:3 poster is width = height * 2 / 3. The crop keeps the full height and
	// takes that width from the right edge, which is where a landscape sleeve's
	// front cover sits (a 16:9 fanart keeps about 37.5% of its width).
	w := (b.Dy()*2 + 1) / 3
	if w > b.Dx() {
		w = b.Dx()
	}
	if w <= 0 || b.Dy() <= 0 {
		return nil, fmt.Errorf("fanart is %dx%d, nothing to crop", b.Dx(), b.Dy())
	}
	rect := image.Rect(b.Max.X-w, b.Min.Y, b.Max.X, b.Max.Y)
	dst := image.NewRGBA(image.Rect(0, 0, w, b.Dy()))
	draw.Draw(dst, dst.Bounds(), src, rect.Min, draw.Src)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 90}); err != nil {
		return nil, fmt.Errorf("encode poster: %w", err)
	}
	return buf.Bytes(), nil
}

// imageName maps a field and its index onto the fixed layout of §8: bare
// `poster.*` and `fanart.*`, and `extrafanart/fanart{i+1}.*` counted from 1.
// `thumb` is never written: no source in the 14-field set supplies it.
func imageName(role field.Name, index int, ext string) string {
	switch role {
	case field.Poster:
		return "poster." + ext
	case field.Fanart:
		return "fanart." + ext
	case field.Extrafanarts:
		return fmt.Sprintf("extrafanart/fanart%d.%s", index+1, ext)
	default:
		return "_." + ext
	}
}

// imageFields are the three fields whose values are bytes; everything else in the
// 14 has a text value.
var imageFields = []field.Name{field.Poster, field.Fanart, field.Extrafanarts}

// imageExtensions is the public subset both media servers read as-is: those bytes
// are written unchanged. Anything else (webp, svg, a sniffing failure) is named
// `.jpg`, which is the whitelist rule of §8 — the bytes are not re-encoded.
var imageExtensions = map[string]bool{"jpg": true, "jpeg": true, "png": true, "gif": true, "tbn": true}

// SniffImageFormat reads the magic bytes and names the format, or "" when the
// bytes are not a format this build knows. Content-Type is not trusted: the site's
// header has been wrong often enough that the engine sniffs.
//
// It is exported because `mdc source test` has to answer the same question ("what
// did we actually get?") with the same code, rather than with a second copy that
// drifts.
func SniffImageFormat(b []byte) string {
	switch {
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "jpg"
	case len(b) >= 8 && bytes.Equal(b[:8], []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case len(b) >= 6 && (bytes.Equal(b[:6], []byte("GIF87a")) || bytes.Equal(b[:6], []byte("GIF89a"))):
		return "gif"
	case len(b) >= 12 && bytes.Equal(b[:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return "webp"
	default:
		return ""
	}
}

// sniffImageExt is the name the bytes get on disk: the public whitelist as-is, and
// anything else as `.jpg` — webp, svg and an unrecognised body are still written,
// and the bytes are never re-encoded.
func sniffImageExt(b []byte) string {
	if ext := SniffImageFormat(b); imageExtensions[ext] {
		return ext
	}
	return "jpg"
}

// ImagesSummary is the images step's summary projection.
type ImagesSummary struct {
	Items        int `json:"items"`
	Images       int `json:"images"`
	Cropped      int `json:"cropped"`
	PartialItems int `json:"partial_items"`
	// Missing is keyed by field name: how many groups lack it.
	Missing map[string]int `json:"missing"`
	// Notes is the human-readable trace the detail page shows; the crop sentence
	// belongs there because "how did this poster appear" is not a count.
	Notes []string `json:"notes,omitempty"`
}

// String renders the images summary as one line, naming the crop when it happened:
// "从横版封套右侧裁切 poster.jpg (2:3)" is the trace ADR-0015 asks for.
func (s ImagesSummary) String() string {
	parts := []string{
		plural(s.Items, "group", "groups"),
		fmt.Sprintf("%d images", s.Images),
	}
	if s.Cropped > 0 {
		parts = append(parts, fmt.Sprintf("%d poster(s) 从横版封套右侧裁切 poster.jpg (2:3)", s.Cropped))
	}
	for _, f := range imageFields {
		if s.Missing[string(f)] > 0 {
			parts = append(parts, fmt.Sprintf("%d missing %s", s.Missing[string(f)], f))
		}
	}
	return strings.Join(parts, ", ")
}
