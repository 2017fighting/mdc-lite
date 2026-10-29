package service

import (
	"context"

	"github.com/2017fighting/mdc-lite/internal/pipeline"
)

// RunRequest is a one-off run of one pipeline over one set of roots: `mdc run`'s
// entry, and the same engine call the preview makes with a different stop point.
type RunRequest struct {
	Pipeline string
	Roots    []string
	// Until is the step to stop after (its type or its name). Empty runs the whole
	// pipeline.
	Until string
	// DryRun plans without applying. The caller decides whether to apply the plan;
	// the service never writes files, because `output` is the only write exit and
	// this package has no business calling it.
	DryRun bool
}

// RunPipeline builds and runs one pipeline without touching the database. It is the
// CLI's half of "the CLI and the HTTP surface share one service layer": `mdc run`
// does not re-implement the engine's wiring, it asks for a run.
func (s *Service) RunPipeline(ctx context.Context, req RunRequest) (*pipeline.Result, error) {
	p, err := s.pipeline(req.Pipeline)
	if err != nil {
		return nil, err
	}
	roots, err := cleanPaths(req.Roots)
	if err != nil {
		return nil, err
	}
	engine, err := pipeline.Build(p, pipeline.Options{
		Input:  pipeline.Input{Roots: roots},
		Until:  req.Until,
		DryRun: req.DryRun,
		Env:    s.envFor(p),
	})
	if err != nil {
		return nil, badRequest(CodeBadRequest, err.Error())
	}
	return engine.Run(ctx)
}
