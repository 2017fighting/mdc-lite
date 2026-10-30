package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/2017fighting/mdc-lite/internal/config"
	"github.com/2017fighting/mdc-lite/internal/field"
	"github.com/2017fighting/mdc-lite/internal/meta"
)

// outputStep is the only step that writes to disk — and it does not do even that:
// it produces a Plan, and the engine (or, in a task run, the runner) applies it.
// The split is what makes `--dry-run` a property of the run rather than a flag
// threaded through every writer.
//
// The layout is fixed by the servers, not by taste: one directory per work, the
// shard videos named `{number}-cd{part}`, one `movie.nfo`, one set of bare-named
// images. Jellyfin groups a work's files by "file base name starts with the
// directory name", which is why the directory name is the number.
type outputStep struct {
	cfg      config.OutputWith
	onError  config.OnError
	required []field.Name
	paths    *pathRenderer
}

func newOutput(w config.OutputWith, onError config.OnError) (Step, error) {
	paths, err := newPathRenderer(w.DirTemplate, w.FileTemplate)
	if err != nil {
		return nil, err
	}
	required := make([]field.Name, 0, len(w.RequiredFields))
	for _, f := range w.RequiredFields {
		required = append(required, field.Name(f))
	}
	return &outputStep{cfg: w, onError: onError, required: required, paths: paths}, nil
}

func (s *outputStep) Type() string { return config.StepOutput }

func (s *outputStep) Name() string { return "" }

func (s *outputStep) Requires() []string { return []string{config.ArtifactParsed} }

func (s *outputStep) Produces() []string { return []string{config.ArtifactManifest} }

func (s *outputStep) Run(_ context.Context, st *State) error {
	items, err := outputInput(st)
	if err != nil {
		return err
	}
	images := map[string]ImagesItem{}
	if st.Images != nil {
		for _, img := range st.Images.Items {
			images[img.Group.Primary.Path] = img
		}
	}
	nfo := map[string]string{}
	if st.NfoDoc != nil {
		for _, doc := range st.NfoDoc.Items {
			nfo[doc.Group.Primary.Path] = doc.Content
		}
	}

	plan := &Plan{}
	summary := &OutputSummary{Missing: newCounts()}
	for _, item := range items {
		key := item.Group.Primary.Path
		dir, err := s.outputDir(item.Number.Normalized, item.Metadata)
		if err != nil {
			return err
		}
		if missing := missingRequired(s.required, item.Metadata); len(missing) > 0 {
			for _, f := range missing {
				summary.Missing[string(f)]++
			}
			if s.onError != config.OnErrorContinue {
				return fmt.Errorf("%s: missing required field(s) %s (output.with.required_fields; add the field, or set on_error: continue)",
					item.Number.Normalized, joinFields(missing))
			}
		}

		planned := PlanItem{Group: item.Group, Number: item.Number, Metadata: item.Metadata, Dir: dir}
		videos, err := s.planVideos(planned)
		if err != nil {
			return err
		}
		planned.Writes = append(planned.Writes, videos...)
		if img, ok := images[key]; ok {
			planned.Writes = append(planned.Writes, planImages(dir, img.Files)...)
			summary.Images += len(img.Files)
		}
		if content, ok := nfo[key]; ok {
			planned.Writes = append(planned.Writes, PlannedWrite{
				Kind: WriteNfo,
				Role: RoleNfo,
				Mode: ModeBytes,
				Dst:  filepath.Join(dir, "movie.nfo"),
				Data: []byte(content),
			})
			summary.Nfo++
		}
		companions, err := s.planCompanions(dir, planned)
		if err != nil {
			return err
		}
		planned.Writes = append(planned.Writes, companions...)

		summary.Items++
		summary.Videos += len(videos)
		summary.Companions += len(companions)
		plan.Items = append(plan.Items, planned)
	}
	st.Plan = plan
	st.OutputRun = summary
	return nil
}

// outputDir renders the directory template under the configured root.
func (s *outputStep) outputDir(number string, md meta.Metadata) (string, error) {
	rel, err := s.paths.RenderDir(templateVars(number, "", md))
	if err != nil {
		return "", fmt.Errorf("dir_template: %w", err)
	}
	return filepath.Join(s.cfg.Root, filepath.FromSlash(rel)), nil
}

// videoBase is the name a part's video file gets. A single-file work keeps the
// source name (nothing about it needs inventing); a split work uses
// `file_template`, whose default is `{number}-cd{part}`.
func (s *outputStep) videoBase(grp Group, p Part, number string, md meta.Metadata) (string, error) {
	if len(grp.Parts) == 1 {
		base := stemOf(p.File.Path)
		return base, nil
	}
	rendered, err := s.paths.RenderFile(templateVars(number, p.Index(), md))
	if err != nil {
		return "", fmt.Errorf("file_template: %w", err)
	}
	base := stemOf(filepath.FromSlash(rendered))
	return base, nil
}

func (s *outputStep) planVideos(item PlanItem) ([]PlannedWrite, error) {
	mode := WriteMode(s.cfg.Link)
	switch mode {
	case ModeCopy, ModeMove:
	case "":
		mode = ModeHardlink
	}
	var out []PlannedWrite
	for _, p := range item.Group.Parts {
		base, err := s.videoBase(item.Group, p, item.Number.Normalized, item.Metadata)
		if err != nil {
			return nil, err
		}
		out = append(out, PlannedWrite{
			Kind: WriteVideo,
			Role: RoleVideo,
			Mode: mode,
			Src:  p.File.Path,
			Path: p.File.Path,
			Dst:  filepath.Join(item.Dir, base+p.File.Ext),
		})
	}
	return out, nil
}

// planImages turns the named image files into writes. Images always go through
// memory (ModeBytes): they are small, and the spec gives them move semantics
// regardless of `output.with.link`.
func planImages(dir string, files []ImageFile) []PlannedWrite {
	out := make([]PlannedWrite, 0, len(files))
	for _, f := range files {
		out = append(out, PlannedWrite{
			Kind:  WriteImage,
			Role:  ledgerRole(f.Role),
			Index: f.Index,
			Mode:  ModeBytes,
			Src:   f.URL,
			Dst:   filepath.Join(dir, filepath.FromSlash(f.Name)),
			Data:  f.Bytes,
		})
	}
	return out
}

// planCompanions writes the group's subtitles and images next to the videos. A
// subtitle follows the video it belongs to and keeps its own language token
// (`ABC-123.mp4` + `ABC-123.zh.srt` → `<base>.zh.srt`); an image keeps its name.
func (s *outputStep) planCompanions(dir string, item PlanItem) ([]PlannedWrite, error) {
	var out []PlannedWrite
	for _, c := range item.Group.Companions {
		base := filepath.Base(c.File.Path)
		name := base
		if !imageCompanion(c.File.Ext) {
			target, err := s.companionName(item, c)
			if err != nil {
				return nil, err
			}
			name = target
		}
		data, err := os.ReadFile(c.File.Path)
		if err != nil {
			return nil, fmt.Errorf("companion %s: %w", c.File.Path, err)
		}
		out = append(out, PlannedWrite{
			Kind: WriteCompanion,
			Role: RoleCompanion,
			Mode: ModeBytes,
			Src:  c.File.Path,
			Path: c.File.Path,
			Dst:  filepath.Join(dir, name),
			Data: data,
		})
	}
	return out, nil
}

// companionName is the subtitle rename: the video's target base plus the
// companion's own language token plus its extension.
func (s *outputStep) companionName(item PlanItem, c Companion) (string, error) {
	grp := item.Group
	part := grp.Parts[0]
	if c.FollowsPart > 0 {
		for _, p := range grp.Parts {
			if p.Index() == c.FollowsPart {
				part = p
				break
			}
		}
	}
	targetBase, err := s.videoBase(grp, part, item.Number.Normalized, item.Metadata)
	if err != nil {
		return "", err
	}
	compBase := stemOf(c.File.Path)
	sourceBase := stemOf(part.File.Path)
	token := ""
	switch {
	case strings.HasPrefix(compBase, sourceBase):
		token = compBase[len(sourceBase):]
	case c.Stem != "" && strings.HasPrefix(compBase, c.Stem):
		token = compBase[len(c.Stem):]
	}
	return targetBase + token + c.File.Ext, nil
}

// imageCompanion reports whether a companion is an image, i.e. one that keeps its
// own name instead of following a video.
func imageCompanion(ext string) bool {
	ext = strings.ToLower(strings.TrimPrefix(ext, "."))
	return imageExtensions[ext] || ext == "webp"
}

// outputInput is the richest artifact `output` can find. `nfo` and `images` are
// optional steps, so a pipeline without them still writes its videos.
func outputInput(st *State) ([]ImagesItem, error) {
	items := stateItems(st, true)
	if items == nil {
		return nil, fmt.Errorf("no Parsed, Scraped, Images or NfoDoc artifact to write")
	}
	return items, nil
}

// stateItems is the per-group view of whichever task-level artifact the current
// pipeline has produced, most processed first. `withParsed` lets `output` fall back
// to the parse result — the videos need nothing but a number and a path — while
// `nfo` refuses to render fields no source supplied.
func stateItems(st *State, withParsed bool) []ImagesItem {
	switch {
	case st.NfoDoc != nil:
		out := make([]ImagesItem, 0, len(st.NfoDoc.Items))
		for _, item := range st.NfoDoc.Items {
			out = append(out, ImagesItem{Group: item.Group, Number: item.Number, Metadata: item.Metadata})
		}
		return out
	case st.Images != nil:
		return st.Images.Items
	case st.Scraped != nil:
		out := make([]ImagesItem, 0, len(st.Scraped.Items))
		for _, item := range st.Scraped.Items {
			out = append(out, ImagesItem{
				Group:    item.Group,
				Number:   item.Number,
				Metadata: item.Metadata,
				Missing:  item.Metadata.Missing,
				Partial:  item.Metadata.Partial,
			})
		}
		return out
	case withParsed && st.Parsed != nil:
		works := ParsedWorks(st)
		out := make([]ImagesItem, 0, len(works))
		for _, work := range works {
			out = append(out, ImagesItem{Group: work.Group, Number: work.Number})
		}
		return out
	default:
		return nil
	}
}

// missingRequired lists the required fields this task did not get, in the order
// the config named them. An image field counts only when its bytes are in hand,
// which is what `Has` means here: `scrape` marks the field only after FetchImage
// succeeded.
func missingRequired(required []field.Name, md meta.Metadata) []field.Name {
	var out []field.Name
	for _, f := range required {
		if !md.Has(f) {
			out = append(out, f)
		}
	}
	return out
}

func joinFields(fields []field.Name) string {
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = string(f)
	}
	return strings.Join(out, ", ")
}

// ledgerRole maps an image field onto the ledger's role column.
func ledgerRole(f field.Name) string {
	switch f {
	case field.Poster:
		return RolePoster
	case field.Fanart:
		return RoleFanart
	case field.Extrafanarts:
		return RoleExtrafanart
	default:
		return string(f)
	}
}

// OutputSummary is the output step's summary projection: what it intends to write,
// and which required fields were missing.
type OutputSummary struct {
	Items      int            `json:"items"`
	Videos     int            `json:"videos"`
	Images     int            `json:"images"`
	Nfo        int            `json:"nfo"`
	Companions int            `json:"companions"`
	Missing    map[string]int `json:"missing_required"`
}

// String renders the output summary as one line.
func (s OutputSummary) String() string {
	parts := []string{
		plural(s.Items, "group", "groups"),
		fmt.Sprintf("%d videos", s.Videos),
		fmt.Sprintf("%d images", s.Images),
		fmt.Sprintf("%d movie.nfo", s.Nfo),
		fmt.Sprintf("%d companions", s.Companions),
	}
	for _, f := range field.All() {
		if s.Missing[string(f)] > 0 {
			parts = append(parts, fmt.Sprintf("missing required %s ×%d", f, s.Missing[string(f)]))
		}
	}
	return strings.Join(parts, ", ")
}
