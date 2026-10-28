// Package cli is the command line. It calls the same service layer the HTTP
// surface adapts, so `mdc config check` and the running server agree about what
// the configuration says.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/2017fighting/mdc-lite/internal/api"
	"github.com/2017fighting/mdc-lite/internal/config"
	"github.com/2017fighting/mdc-lite/internal/store"
	"github.com/2017fighting/mdc-lite/internal/web"
)

// Version is stamped at build time with -ldflags.
var Version = "dev"

// shutdownGrace bounds how long the server waits for in-flight requests. It
// matches the container's default terminationGracePeriodSeconds of 30 with room
// to finish.
const shutdownGrace = 25 * time.Second

// Run executes one command and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "serve":
		return runServe(args[1:], stdout, stderr)
	case "config":
		return runConfig(args[1:], stdout, stderr)
	case "version":
		fmt.Fprintln(stdout, Version)
		return 0
	case "-h", "--help", "help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "mdc: unknown command %q\n\n", args[0])
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `mdc — a media library pipeline runner

usage:
  mdc serve           [--config FILE] [overrides]   run the server
  mdc config check    [--config FILE] [overrides]   load and validate the config, print what took effect
  mdc version

overrides (these win over the file, which wins over ${VAR}, which wins over the defaults):
  --listen ADDR
  --db-path FILE
  --pipelines-dir DIR
  --templates-dir DIR
  --log-level LEVEL
  --log-format FORMAT
`)
}

// commonFlags are the flags every command shares.
type commonFlags struct {
	fs *flag.FlagSet

	config string

	listen, dbPath, pipelinesDir, templatesDir string
	logLevel, logFormat                        string
}

func newCommonFlags(name string, stderr io.Writer) *commonFlags {
	c := &commonFlags{fs: flag.NewFlagSet(name, flag.ContinueOnError)}
	c.fs.SetOutput(stderr)
	c.fs.StringVar(&c.config, "config", "/config/config.yaml", "path to the process-level config file")
	c.fs.StringVar(&c.listen, "listen", "", "HTTP listen address")
	c.fs.StringVar(&c.dbPath, "db-path", "", "path to the SQLite file")
	c.fs.StringVar(&c.pipelinesDir, "pipelines-dir", "", "directory holding the pipeline files")
	c.fs.StringVar(&c.templatesDir, "templates-dir", "", "directory holding @file templates")
	c.fs.StringVar(&c.logLevel, "log-level", "", "log level: debug, info, warn, error")
	c.fs.StringVar(&c.logFormat, "log-format", "", "log format: text, json")
	return c
}

// flags turns whatever was set on the command line into config overrides.
func (c *commonFlags) flags() config.Flags {
	set := func(name string) *string {
		f := c.fs.Lookup(name)
		if f == nil || f.Value.String() == "" {
			return nil
		}
		v := f.Value.String()
		return &v
	}
	return config.Flags{
		Listen:       set("listen"),
		DBPath:       set("db-path"),
		PipelinesDir: set("pipelines-dir"),
		TemplatesDir: set("templates-dir"),
		LogLevel:     set("log-level"),
		LogFormat:    set("log-format"),
	}
}

func (c *commonFlags) load() (config.Settings, error) {
	return config.Load(c.config, config.LoadOptions{Flags: c.flags()})
}

// load is one load of both config files: process settings, then every pipeline.
type loaded struct {
	settings  config.Settings
	pipelines []config.Pipeline
	problems  []pipelineProblem
}

type pipelineProblem struct {
	ID, File string
	Err      error
}

// loadAll loads the settings and every pipeline file. A broken pipeline does not
// abort the load: `config check` has to report all of them, and `serve` refuses
// afterwards. A broken settings file does abort, because without it there is no
// directory to read.
func loadAll(c *commonFlags, reg *config.Registry) (*loaded, error) {
	settings, err := c.load()
	if err != nil {
		return nil, err
	}
	l := &loaded{settings: settings}
	files, err := config.PipelineFiles(settings.PipelinesDir)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		p, err := config.LoadPipelineFile(f, reg, nil)
		if err != nil {
			l.problems = append(l.problems, pipelineProblem{ID: config.PipelineID(f), File: f, Err: err})
			continue
		}
		l.pipelines = append(l.pipelines, p)
	}
	return l, nil
}

func runConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "check" {
		fmt.Fprintln(stderr, "mdc config: expected `check`")
		return 2
	}
	c := newCommonFlags("mdc config check", stderr)
	if err := c.fs.Parse(args[1:]); err != nil {
		return 2
	}
	l, err := loadAll(c, config.DefaultStepRegistry())
	if err != nil {
		fmt.Fprintf(stderr, "config: %v\n", err)
		return 1
	}
	report(stdout, c.config, l)
	if len(l.problems) > 0 {
		return 1
	}
	return 0
}

// report prints what was loaded: the pipelines with their validation result, then
// every effective process-level key with where it came from.
func report(w io.Writer, configPath string, l *loaded) {
	fmt.Fprintf(w, "config: %s\n", configPath)
	fmt.Fprintf(w, "pipelines_dir: %s\n", l.settings.PipelinesDir)

	if len(l.pipelines) == 0 && len(l.problems) == 0 {
		fmt.Fprintf(w, "\npipelines: none found in %s\n", l.settings.PipelinesDir)
	} else {
		fmt.Fprintf(w, "\npipelines (%d):\n", len(l.pipelines)+len(l.problems))
		for _, p := range l.pipelines {
			fmt.Fprintf(w, "  ok    %s\n        %s\n", p.ID, p)
		}
		for _, p := range l.problems {
			fmt.Fprintf(w, "  FAIL  %s\n        %v\n", p.ID, p.Err)
		}
	}

	fmt.Fprintf(w, "\neffective settings:\n")
	rows := l.settings.Effective()
	width := 0
	for _, kv := range rows {
		if len(kv.Key) > width {
			width = len(kv.Key)
		}
	}
	for _, kv := range rows {
		fmt.Fprintf(w, "  %-*s  %-32s  (%s)\n", width, kv.Key, kv.Value, l.settings.SourceOf(kv.Key))
	}
}

func runServe(args []string, stdout, stderr io.Writer) int {
	c := newCommonFlags("mdc serve", stderr)
	if err := c.fs.Parse(args); err != nil {
		return 2
	}
	l, err := loadAll(c, config.DefaultStepRegistry())
	if err != nil {
		fmt.Fprintf(stderr, "config: %v\n", err)
		return 1
	}
	if len(l.problems) > 0 {
		// A bad pipeline refuses to start the process. Finding out at 3am from a
		// monitor trigger is the failure mode this rule exists to prevent.
		for _, p := range l.problems {
			fmt.Fprintf(stderr, "refusing to start: %v\n", p.Err)
		}
		return 1
	}

	logger := newLogger(stdout, l.settings.Log)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The interface value, not the concrete pointer: a nil *store.Store stored in
	// api.Store is a non-nil interface, and the health handler's nil check would
	// miss it and panic instead of answering 503.
	var healthStore api.Store
	st, err := store.Open(ctx, l.settings.DBPath)
	if err != nil {
		// The server still comes up: /api/health then answers 503 with the reason,
		// which is a better diagnostic than a crash loop with no output.
		logger.Error("database is not available", "path", l.settings.DBPath, "err", err)
	} else {
		defer func() { _ = st.Close() }()
		healthStore = st
		logger.Info("database ready", "path", st.Path(), "migrations", st.Migrations())
	}

	srv := api.New(Version, healthStore, web.Handler())
	httpServer := &http.Server{
		Addr:              l.settings.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	for _, p := range l.pipelines {
		logger.Info("pipeline loaded", "id", p.ID, "file", p.File, "steps", len(p.Steps))
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", l.settings.Listen, "version", Version)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		logger.Error("server failed", "err", err)
		return 1
	case <-ctx.Done():
		logger.Info("shutting down", "grace", shutdownGrace.String())
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown did not finish cleanly", "err", err)
		return 1
	}
	return 0
}

func newLogger(w io.Writer, s config.LogSettings) *slog.Logger {
	var level slog.Level
	switch s.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	if s.Format == "json" {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}
