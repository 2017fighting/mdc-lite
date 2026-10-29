// Package service is the in-process application surface. The HTTP handlers are one
// adapter over it and the CLI is another: neither of them owns a rule, so
// `mdc config check` and a request to `mdc serve` answer from the same code.
//
// It knows the process's settings and pipelines, the task store, and the
// scheduler's two decisions a human can make through the UI (cancel, rerun). It
// also holds the two pieces of state that are deliberately not in the database:
// the scan preview (memory only, ADR-0013) and the image cache.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/2017fighting/mdc-lite/internal/config"
	"github.com/2017fighting/mdc-lite/internal/pipeline"
	"github.com/2017fighting/mdc-lite/internal/store"
	"github.com/2017fighting/mdc-lite/internal/tasks"
)

// Scheduler is the part of the process's concurrency model the service asks for:
// the prefix single-flight gate (a preview and a batch creation wait for the walk
// in flight), the two human decisions, and the wake-up after a write.
//
// An interface, not *scheduler.Scheduler, so a test can hand the service a
// scheduler whose prefix runs inline; the production wiring passes the real one.
type Scheduler interface {
	Prefix(ctx context.Context, fn func(context.Context) error) error
	Cancel(ctx context.Context, taskID int64) error
	Rerun(ctx context.Context, taskID int64) error
	Notify()
}

// Config is what the service is built from. Everything is required except the
// scheduler, which is nil in a test that never asks for a cancel or a rerun.
type Config struct {
	Settings  config.Settings
	Pipelines []config.Pipeline
	// DB is nil when the database could not be opened: every endpoint that needs
	// rows then answers 503 db_unavailable instead of the process refusing to
	// start. Health still works, which is the point.
	DB *store.Store
	// Tasks is the state machine over DB. When nil and DB is open it is built
	// here, because every caller would build the same one.
	Tasks     *tasks.Store
	Scheduler Scheduler
	// EnvFor builds the task-level environment for one pipeline: its sources, its
	// templates directory, its image budget.
	EnvFor func(config.Pipeline) pipeline.Environment
	Logger *slog.Logger
	// Now and PreviewTTL are defaults-for-tests; zero means time.Now and
	// DefaultPreviewTTL.
	Now        func() time.Time
	PreviewTTL time.Duration
}

// Service is the application surface.
type Service struct {
	settings  config.Settings
	pipelines []config.Pipeline
	db        *store.Store
	tasks     *tasks.Store
	sched     Scheduler
	envFor    func(config.Pipeline) pipeline.Environment
	logger    *slog.Logger
	now       func() time.Time

	previews *previews
	images   *imageCache
}

// New builds the service.
func New(cfg Config) (*Service, error) {
	if len(cfg.Pipelines) == 0 && cfg.Settings.PipelinesDir == "" {
		return nil, errors.New("service: no pipelines and no pipelines directory")
	}
	taskStore := cfg.Tasks
	if taskStore == nil && cfg.DB != nil {
		taskStore = tasks.New(cfg.DB, tasks.Config{RetryLimit: cfg.Settings.RetryLimit})
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	ttl := cfg.PreviewTTL
	if ttl <= 0 {
		ttl = DefaultPreviewTTL
	}
	envFor := cfg.EnvFor
	if envFor == nil {
		envFor = func(config.Pipeline) pipeline.Environment { return pipeline.Environment{} }
	}
	return &Service{
		settings:  cfg.Settings,
		pipelines: cfg.Pipelines,
		db:        cfg.DB,
		tasks:     taskStore,
		sched:     cfg.Scheduler,
		envFor:    envFor,
		logger:    logger,
		now:       now,
		previews:  newPreviews(ttl, now),
		images:    newImageCache(defaultImageCacheBytes),
	}, nil
}

// Path, Migrations and Ready are the health probe's three questions. They are here
// rather than in the HTTP package so the CLI and a test can ask them too.
func (s *Service) Path() string {
	if s.db == nil {
		return ""
	}
	return s.db.Path()
}

// Migrations is the highest applied migration version, or 0 when there is no
// database.
func (s *Service) Migrations() int64 {
	if s.db == nil {
		return 0
	}
	return s.db.Migrations()
}

// Ready reports whether the database can serve rows right now.
func (s *Service) Ready(ctx context.Context) error {
	if s.db == nil {
		return errors.New("the database is not open")
	}
	return s.db.Ready(ctx)
}

// ErrDBUnavailable is what every row-backed endpoint answers when the database
// never opened. It maps to 503 `db_unavailable`.
var ErrDBUnavailable = &Error{
	Status:  503,
	Code:    CodeDBUnavailable,
	Message: "the database is not open",
}

// taskStore returns the state machine, or the 503 the caller should answer with.
func (s *Service) taskStore() (*tasks.Store, error) {
	if s.tasks == nil {
		return nil, ErrDBUnavailable
	}
	return s.tasks, nil
}

// PipelineView is one pipeline as `GET /api/pipelines` shows it: its id and the
// sequence of step types. There is no per-pipeline detail endpoint — this is what
// the scan page's pipeline picker needs.
type PipelineView struct {
	ID    string   `json:"id"`
	Steps []string `json:"steps"`
}

// Pipelines lists the loaded pipelines in the order the directory listed them.
func (s *Service) Pipelines() []PipelineView {
	out := make([]PipelineView, 0, len(s.pipelines))
	for _, p := range s.pipelines {
		steps := make([]string, 0, len(p.Steps))
		for _, st := range p.Steps {
			steps = append(steps, st.Type)
		}
		out = append(out, PipelineView{ID: p.ID, Steps: steps})
	}
	return out
}

// pipeline looks one up by id. A missing id is the operator's mistake (a stale
// page, a renamed file), so it is a 400 that names what exists.
func (s *Service) pipeline(id string) (config.Pipeline, error) {
	for _, p := range s.pipelines {
		if p.ID == id {
			return p, nil
		}
	}
	known := make([]string, len(s.pipelines))
	for i, p := range s.pipelines {
		known[i] = p.ID
	}
	return config.Pipeline{}, badRequestf(CodeUnknownPipeline, "no pipeline %q (have: %v)", id, known)
}

// Prefix runs fn inside the scheduler's single-flight prefix gate. Without a
// scheduler (a test, or a process whose database never opened) it runs inline:
// there is nothing to be single about.
func (s *Service) prefix(ctx context.Context, fn func(context.Context) error) error {
	if s.sched == nil {
		return fn(ctx)
	}
	return s.sched.Prefix(ctx, fn)
}

// notify wakes the scheduler after a write that put work in the queue.
func (s *Service) notify() {
	if s.sched != nil {
		s.sched.Notify()
	}
}

// runPrefix runs one pipeline's batch-level prefix (through parse) over a trigger's
// directories and reports what each step did.
func (s *Service) runPrefix(ctx context.Context, p config.Pipeline, in pipeline.Input, out *[]pipeline.StepOutcome) (*pipeline.Result, error) {
	engine, err := pipeline.Build(p, pipeline.Options{
		Input: in,
		// The preview and a manual trigger both stop after `parse`: the number the
		// panel shows exists only after the parse step, and everything past it needs
		// the network.
		Until:  config.StepParse,
		DryRun: true,
		Env:    s.envFor(p),
		OnStep: func(_ context.Context, o pipeline.StepOutcome) error {
			*out = append(*out, o)
			return nil
		},
	})
	if err != nil {
		return nil, err
	}
	return engine.Run(ctx)
}

// recordBatchSteps writes the prefix's step traces under the batch they produced.
// They are written after the batch row exists (a step run needs its owner), each in
// its own transaction, so a crash between them keeps the steps that were written.
//
// A trace failure is logged, not returned: the batch and its tasks are already
// committed, and losing the trace is a worse reason to fail a request than the
// dedup gate noticing the paths are taken on the retry.
func (s *Service) recordBatchSteps(ctx context.Context, batchID int64, outcomes []pipeline.StepOutcome) {
	for _, o := range outcomes {
		summary := "{}"
		if o.Summary != nil {
			if data, err := json.Marshal(o.Summary); err == nil {
				summary = string(data)
			}
		}
		stepErr := ""
		if o.Err != nil {
			stepErr = o.Err.Error()
		}
		_, err := s.tasks.RecordStep(ctx, tasks.Step{
			BatchID:     batchID,
			Idx:         o.Idx,
			Type:        o.Type,
			Name:        o.Name,
			Status:      o.Status,
			MS:          o.MS,
			SummaryJSON: summary,
			Error:       stepErr,
		})
		if err != nil {
			s.logger.Error("the batch's step trace could not be written", "batch", batchID, "step", o.Type, "err", err)
			return
		}
	}
}

// checkRoots validates a trigger's roots against the filesystem, in the one place
// both the preview and the batch creation use.
//
// A root is a directory (walked recursively) or a file (taken on its own), as the
// spec's `roots` allows. A root that is gone is reported per path: the preview
// refuses it (400), a batch records it as `not_found` and carries on with the rest.
func (s *Service) checkRoots(roots []string) (existing []string, missing []string, err error) {
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			return nil, nil, badRequest(CodeBadRequest, "a root path is empty")
		}
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, nil, badRequest(CodeBadRequest, fmt.Sprintf("%s: %v", root, err))
		}
		info, statErr := os.Stat(abs)
		switch {
		case statErr == nil && !info.IsDir() && !info.Mode().IsRegular():
			return nil, nil, badRequest(CodeBadRequest, fmt.Sprintf("%s is neither a file nor a directory", abs))
		case statErr == nil:
			existing = append(existing, abs)
		case errors.Is(statErr, fs.ErrNotExist):
			missing = append(missing, abs)
		default:
			return nil, nil, badRequest(CodeBadRequest, fmt.Sprintf("%s: %v", abs, statErr))
		}
	}
	// ADR-0016's invariant: the same tree twice, or a tree inside another, walks the
	// same files twice and would create two tasks over them. Compared lexically.
	for i := range existing {
		for j := i + 1; j < len(existing); j++ {
			if withinPath(existing[i], existing[j]) || withinPath(existing[j], existing[i]) {
				return nil, nil, badRequestf(CodeBadRequest,
					"the selected paths overlap (%s and %s); select each tree once", existing[i], existing[j])
			}
		}
	}
	return existing, missing, nil
}
