package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os/signal"
	"strings"
	"syscall"

	"github.com/2017fighting/mdc-lite/internal/config"
	"github.com/2017fighting/mdc-lite/internal/pipeline"
)

// runPipeline is `mdc run`: one pipeline, one directory, printed summary.
//
// It is the same entry point the preview and a monitor tick use — the trigger's
// directories are injected over `scan.with` and nothing else changes — which is
// why a wrong rule shows up here first. It writes no files and never opens the
// database: this build only produces batch-level artifacts, and they live in
// memory.
func runPipeline(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mdc run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		configPath   string
		pipelinesDir string
		pipelineID   string
		roots        pathList
		until        string
		dryRun       bool
	)
	fs.StringVar(&configPath, "config", "/config/config.yaml", "path to the process-level config file")
	fs.StringVar(&pipelinesDir, "pipelines-dir", "", "directory holding the pipeline files")
	fs.StringVar(&pipelineID, "pipeline", "", "id of the pipeline to run (the file name's stem)")
	fs.Var(&roots, "roots", "directory or file to scan; repeatable, and accepts a comma-separated list")
	fs.StringVar(&until, "until", "", "stop after this step (its type, or its name)")
	fs.BoolVar(&dryRun, "dry-run", false, "plan only: write no files and touch no database")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if pipelineID == "" {
		fmt.Fprintln(stderr, "mdc run: --pipeline is required")
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
	if len(l.problems) > 0 {
		// Same rule as `serve`: a directory with a broken pipeline is refused as a
		// whole, so a run can never quietly use one config while another is broken.
		for _, p := range l.problems {
			fmt.Fprintf(stderr, "refusing to run: %v\n", p.Err)
		}
		return 1
	}
	p, ok := findPipeline(l.pipelines, pipelineID)
	if !ok {
		known := make([]string, len(l.pipelines))
		for i, one := range l.pipelines {
			known[i] = one.ID
		}
		fmt.Fprintf(stderr, "mdc run: no pipeline %q in %s (have: %s)\n",
			pipelineID, l.settings.PipelinesDir, strings.Join(known, ", "))
		return 1
	}

	engine, err := pipeline.Build(p, pipeline.Options{
		Input:  pipeline.Input{Roots: roots},
		Until:  until,
		DryRun: dryRun,
	})
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	res, err := engine.Run(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	res.Print(stdout)
	return 0
}

func findPipeline(pipelines []config.Pipeline, id string) (config.Pipeline, bool) {
	for _, p := range pipelines {
		if p.ID == id {
			return p, true
		}
	}
	return config.Pipeline{}, false
}

// pathList is a repeatable path flag that also accepts a comma-separated list,
// so both `--roots a --roots b` and `--roots a,b` work.
type pathList []string

func (l *pathList) String() string { return strings.Join(*l, ",") }

func (l *pathList) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			*l = append(*l, part)
		}
	}
	return nil
}
