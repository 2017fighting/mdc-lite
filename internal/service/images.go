package service

import (
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/2017fighting/mdc-lite/internal/pipeline"
	"github.com/2017fighting/mdc-lite/internal/tasks"

	// The decoders are registered for image.Decode; a poster is jpeg or png in
	// practice, and a gif is a gif only until it is re-encoded on the way out.
	_ "image/gif"
)

// This file serves the two image endpoints: the 40px thumbnail the list scrolls
// past, and the original the detail page's lightbox opens.
//
// The bytes come from the output ledger's `dst` — never from a source URL, because
// the point of the detail page is what actually landed on disk. Each response
// carries the ledger's sha256 as its ETag and an immutable cache directive, so a
// browser revalidates with one 304 instead of re-downloading a poster.

// ImageWidths is the closed set of widths either endpoint accepts. `thumb`'s
// default is the smallest — the list must not pull originals — and `image`'s default
// is the original itself.
var ImageWidths = []int{40, 80, 160, 320, 640}

// DefaultThumbWidth is what `thumb` answers when the request names no width.
const DefaultThumbWidth = 40

// imageRoles is the closed set of roles either endpoint can serve: the landable
// images of the ledger.
var imageRoles = map[string]bool{
	pipeline.RolePoster:      true,
	pipeline.RoleFanart:      true,
	pipeline.RoleExtrafanart: true,
}

// ImageRequest is one image endpoint's parameters.
type ImageRequest struct {
	TaskID int64
	Role   string
	Idx    int
	// Width is 0 for the original. Either endpoint's default is decided by the
	// handler before this is called.
	Width int
	// IfNoneMatch is the request's validator, used for the 304 path.
	IfNoneMatch string
}

// Image is one image response: the bytes (or the fact that the client already has
// them), the content type, and the validators.
type Image struct {
	Bytes       []byte
	ContentType string
	ETag        string
	// NotModified is true when If-None-Match matched: the handler answers 304 with
	// the ETag and no body.
	NotModified bool
}

// Image serves one landed image of a task.
//
// The validator is the underlying file's sha256 for every width: the width is part
// of the request URL (`?w=40`), and that URL is what a browser keys its cache and
// its If-None-Match by, so one validator across widths cannot hand a client the
// wrong bytes. Cache-Control is `private` for the same reason.
//
// It reads the newest run that has the image: a later failed rerun must not blank the
// thumbnails, so the search walks the run history backwards until the ledger has the
// role+index.
func (s *Service) Image(ctx context.Context, req ImageRequest) (Image, error) {
	st, err := s.taskStore()
	if err != nil {
		return Image{}, err
	}
	role := strings.TrimSpace(req.Role)
	if role == "" {
		role = pipeline.RolePoster
	}
	if !imageRoles[role] {
		return Image{}, badRequestf(CodeBadRequest, "%q is not an image role (poster, fanart, extrafanart)", role)
	}
	if req.Idx < 0 {
		return Image{}, badRequestf(CodeBadRequest, "idx must be >= 0, got %d", req.Idx)
	}
	if req.Width != 0 && !isImageWidth(req.Width) {
		return Image{}, badRequestf(CodeBadRequest, "w must be one of 40, 80, 160, 320, 640 (or omitted for the original), got %d", req.Width)
	}

	row, err := s.findImageRow(ctx, st, req.TaskID, role, req.Idx)
	if err != nil {
		return Image{}, err
	}
	data, err := os.ReadFile(row.Dst)
	if errors.Is(err, fs.ErrNotExist) {
		return Image{}, &Error{Status: 404, Code: CodeImageMissing,
			Message: "the ledger knows this image but the file is gone: " + row.Dst}
	}
	if err != nil {
		return Image{}, err
	}
	etag := row.SHA256
	if etag == "" {
		sum := sha256.Sum256(data)
		etag = hex.EncodeToString(sum[:])
	}
	img := Image{ContentType: contentTypeFor(row.Dst), ETag: etag}
	if etagMatches(req.IfNoneMatch, etag) {
		img.NotModified = true
		return img, nil
	}
	if req.Width == 0 {
		img.Bytes = data
		return img, nil
	}
	scaled, contentType, err := s.scaled(row.Dst, etag, req.Width, data)
	if err != nil {
		// An image the decoders cannot read is still an image: serve the original
		// rather than a 500 over a webp poster.
		s.logger.Warn("the image could not be scaled; serving the original", "path", row.Dst, "err", err)
		img.Bytes = data
		return img, nil
	}
	img.Bytes, img.ContentType = scaled, contentType
	return img, nil
}

// findImageRow looks up one ledger row by walking the task's runs newest first. The
// row is the answer to "the poster of this task"; a later run that failed before
// writing anything simply has no rows and is skipped.
func (s *Service) findImageRow(ctx context.Context, st *tasks.Store, taskID int64, role string, idx int) (tasks.FileRow, error) {
	if _, err := st.Task(ctx, taskID); err != nil {
		return tasks.FileRow{}, taskLookupError(err)
	}
	runs, err := st.Runs(ctx, taskID)
	if err != nil {
		return tasks.FileRow{}, err
	}
	for i := len(runs) - 1; i >= 0; i-- {
		rows, err := st.FileRows(ctx, runs[i].ID)
		if err != nil {
			return tasks.FileRow{}, err
		}
		for _, row := range rows {
			if row.Kind != string(pipeline.WriteImage) || row.Action == string(pipeline.ActionDeleted) {
				continue
			}
			if row.Role == role && row.Idx == idx {
				return row, nil
			}
		}
	}
	return tasks.FileRow{}, &Error{Status: 404, Code: CodeImageMissing,
		Message: "the task has no " + role + " #" + strconv.Itoa(idx) + " in its ledger"}
}

// isImageWidth reports whether w is in the whitelist.
func isImageWidth(w int) bool {
	for _, allowed := range ImageWidths {
		if w == allowed {
			return true
		}
	}
	return false
}

// contentTypeFor maps the landed file's extension onto a content type. The ledger
// names what was written, so the extension is the source of truth.
func contentTypeFor(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".tbn":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}

// etagMatches implements If-None-Match for the one value this server issues: it
// compares the opaque tag, tolerating the W/ prefix, the quotes, and a comma list.
func etagMatches(ifNoneMatch, etag string) bool {
	if strings.TrimSpace(ifNoneMatch) == "" {
		return false
	}
	for _, candidate := range strings.Split(ifNoneMatch, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" {
			return true
		}
		candidate = strings.TrimPrefix(candidate, "W/")
		candidate = strings.Trim(candidate, `"`)
		if candidate == etag {
			return true
		}
	}
	return false
}

// scaled returns the cached scaled image, decoding and resizing on a miss.
func (s *Service) scaled(path, etag string, width int, original []byte) ([]byte, string, error) {
	key := path + "|" + etag + "|" + strconv.Itoa(width)
	if data, contentType, ok := s.images.get(key); ok {
		return data, contentType, nil
	}
	data, contentType, err := resizeImage(original, width)
	if err != nil {
		return nil, "", err
	}
	s.images.put(key, data, contentType)
	return data, contentType, nil
}

// resizeImage scales an image to exactly width, preserving its aspect ratio. It uses
// the standard library only (no third-party imaging, no ffmpeg): posters are jpegs
// and pngs, and a box filter on a 640px target is cheap.
//
// The requested width is honoured both ways: a 320px image asked for at 640 comes
// back 640 wide, so the page's fixed boxes never get a surprise size. Upscaling a
// small poster is blurry, but a wrong-size answer would be a layout bug.
func resizeImage(data []byte, width int) ([]byte, string, error) {
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", err
	}
	if img.Bounds().Dx() == width {
		return data, contentTypeForFormat(format), nil
	}
	scaled := scaleToWidth(img, width)
	var buf bytes.Buffer
	if format == "png" {
		if err := png.Encode(&buf, scaled); err != nil {
			return nil, "", err
		}
		return buf.Bytes(), "image/png", nil
	}
	if err := jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: 85}); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), "image/jpeg", nil
}

// contentTypeForFormat names the format image.Decode reported.
func contentTypeForFormat(format string) string {
	switch format {
	case "png":
		return "image/png"
	case "gif":
		return "image/gif"
	default:
		return "image/jpeg"
	}
}

// scaleToWidth box-filters an image down to width pixels. Each destination pixel is
// the mean of the source rectangle it covers, which is the cheapest filter that does
// not alias; it is executed on RGBA so alpha (a png poster with transparency) is
// preserved.
func scaleToWidth(src image.Image, width int) *image.RGBA {
	b := src.Bounds()
	height := int(float64(b.Dy())*float64(width)/float64(b.Dx()) + 0.5)
	if height < 1 {
		height = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	if b.Min.X != 0 || b.Min.Y != 0 {
		// Normalize the source origin once, so the arithmetic below can use 0-based
		// coordinates for both images.
		normalized := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		draw.Draw(normalized, normalized.Bounds(), src, b.Min, draw.Src)
		src, b = normalized, normalized.Bounds()
	}
	for y := 0; y < height; y++ {
		y0 := y * b.Dy() / height
		y1 := (y + 1) * b.Dy() / height
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < width; x++ {
			x0 := x * b.Dx() / width
			x1 := (x + 1) * b.Dx() / width
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, bl, a uint64
			var n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					cr, cg, cb, ca := src.At(b.Min.X+sx, b.Min.Y+sy).RGBA()
					r += uint64(cr)
					g += uint64(cg)
					bl += uint64(cb)
					a += uint64(ca)
					n++
				}
			}
			if n == 0 {
				continue
			}
			dst.Set(x, y, colorFromAverage(r/n, g/n, bl/n, a/n))
		}
	}
	return dst
}

// colorFromAverage converts a mean of premultiplied 16-bit channels back into the
// 8-bit premultiplied form image.RGBA stores.
func colorFromAverage(r, g, b, a uint64) color.RGBA {
	return color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(a >> 8)}
}

// imageURL is the endpoint the detail page asks for an image's bytes: the ImageRef
// the JSON carries instead of a payload.
func imageURL(taskID int64, role string, idx int) string {
	return "/api/tasks/" + strconv.FormatInt(taskID, 10) + "/image?role=" +
		url.QueryEscape(role) + "&idx=" + strconv.Itoa(idx)
}

// defaultImageCacheBytes is the process-wide budget of scaled images. Ten modest
// thumbnails per task page and a lightbox's 640px poster fit many times over; beyond
// it the cache evicts oldest-first rather than growing with the library.
const defaultImageCacheBytes = 64 << 20

// imageCache is a byte-budgeted LRU of scaled images. The source of truth is the
// file on disk; this only saves re-decoding a poster every time the list scrolls.
type imageCache struct {
	mu    sync.Mutex
	limit int
	used  int
	order *list.List
	items map[string]*list.Element
}

// cacheItem is one cached scaled image.
type cacheItem struct {
	key         string
	contentType string
	data        []byte
}

func newImageCache(limit int) *imageCache {
	return &imageCache{limit: limit, order: list.New(), items: map[string]*list.Element{}}
}

// get returns a cached image and moves it to the front.
func (c *imageCache) get(key string) ([]byte, string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, "", false
	}
	c.order.MoveToFront(el)
	item := el.Value.(*cacheItem)
	return item.data, item.contentType, true
}

// put stores one scaled image, evicting from the back until it fits. An image bigger
// than the whole budget is served but not kept.
func (c *imageCache) put(key string, data []byte, contentType string) {
	if len(data) > c.limit {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.order.MoveToFront(el)
		return
	}
	el := c.order.PushFront(&cacheItem{key: key, contentType: contentType, data: data})
	c.items[key] = el
	c.used += len(data)
	for c.used > c.limit {
		back := c.order.Back()
		if back == nil {
			break
		}
		item := back.Value.(*cacheItem)
		c.order.Remove(back)
		delete(c.items, item.key)
		c.used -= len(item.data)
	}
}
