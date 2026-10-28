// Package config loads and validates the process-level config file and the
// pipeline files. Everything it accepts it accepts strictly: an unknown key
// stops the process from starting, and the error names 文件:行:列.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Source says where an effective value came from, in override order.
type Source string

const (
	SourceDefault Source = "default"
	SourceFile    Source = "file"
	SourceEnv     Source = "env"
	SourceFlag    Source = "flag"
)

// LogSettings is the `log` block. Logs go to stdout only; there is no file sink.
type LogSettings struct {
	Level  LogLevel  `yaml:"level"`
	Format LogFormat `yaml:"format"`
}

// LogLevel is the `log.level` enum. It is a type rather than a string so the two
// places that care — validation and the logger — read the same closed set.
type LogLevel string

// The log levels, in increasing severity.
const (
	LogDebug LogLevel = "debug"
	LogInfo  LogLevel = "info"
	LogWarn  LogLevel = "warn"
	LogError LogLevel = "error"
)

// Levels returns the accepted log levels.
func Levels() []LogLevel { return []LogLevel{LogDebug, LogInfo, LogWarn, LogError} }

// Valid reports whether l is one of the accepted levels.
func (l LogLevel) Valid() bool {
	for _, v := range Levels() {
		if l == v {
			return true
		}
	}
	return false
}

// Slog maps the level onto the stdlib's.
func (l LogLevel) Slog() slog.Level {
	switch l {
	case LogDebug:
		return slog.LevelDebug
	case LogWarn:
		return slog.LevelWarn
	case LogError:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// LogFormat is the `log.format` enum.
type LogFormat string

// The two accepted log formats.
const (
	LogText LogFormat = "text"
	LogJSON LogFormat = "json"
)

// Formats returns the accepted log formats.
func Formats() []LogFormat { return []LogFormat{LogText, LogJSON} }

// Valid reports whether f is one of the accepted formats.
func (f LogFormat) Valid() bool {
	for _, v := range Formats() {
		if f == v {
			return true
		}
	}
	return false
}

// BrowseRoot is one entry of the directory-picker allow-list.
type BrowseRoot struct {
	Path  string `yaml:"path"`
	Label string `yaml:"label"`
}

// Concurrency is the three numbers of the concurrency model. They live here and
// nowhere else: no step may open its own `with.concurrency`.
type Concurrency struct {
	Tasks    int `yaml:"tasks"`
	Requests int `yaml:"requests"`
	Images   int `yaml:"images"`
}

// Webhook is the second-batch trigger block. The shape is frozen now so a
// config written today keeps validating; the endpoint is not registered in V1.
type Webhook struct {
	Enabled    bool              `yaml:"enabled"`
	Token      string            `yaml:"token"`
	Categories map[string]string `yaml:"categories"`
}

// Settings is the effective process-level configuration.
type Settings struct {
	Listen       string       `yaml:"listen"`
	DBPath       string       `yaml:"db_path"`
	PipelinesDir string       `yaml:"pipelines_dir"`
	TemplatesDir string       `yaml:"templates_dir"`
	Log          LogSettings  `yaml:"log"`
	BrowseRoots  []BrowseRoot `yaml:"browse_roots"`
	Concurrency  Concurrency  `yaml:"concurrency"`
	RetryLimit   int          `yaml:"retry_limit"`
	Proxy        string       `yaml:"proxy"`
	Webhook      Webhook      `yaml:"webhook"`

	sources map[string]Source
}

// Defaults returns the documented defaults for every key.
func Defaults() Settings {
	return Settings{
		Listen:       ":9208",
		DBPath:       "/config/mdc.db",
		PipelinesDir: "/config/pipelines",
		TemplatesDir: "/config/templates",
		Log:          LogSettings{Level: LogInfo, Format: LogText},
		BrowseRoots:  []BrowseRoot{{Path: "/media", Label: "/media"}},
		Concurrency:  Concurrency{Tasks: 4, Requests: 8, Images: 4},
		RetryLimit:   5,
		Proxy:        "",
		Webhook:      Webhook{Enabled: false, Token: "", Categories: map[string]string{}},
		sources:      map[string]Source{},
	}
}

// SourceOf reports where the value of a dotted key (or list element, written
// `browse_roots[0].path`) came from.
func (s Settings) SourceOf(key string) Source {
	if v, ok := s.sources[key]; ok {
		return v
	}
	return SourceDefault
}

// Flags are the command-line overrides. A nil field is not set.
type Flags struct {
	Listen       *string
	DBPath       *string
	PipelinesDir *string
	TemplatesDir *string
	LogLevel     *LogLevel
	LogFormat    *LogFormat
}

// LoadOptions carries the inputs Load reads besides the file.
type LoadOptions struct {
	// Flags override every other source.
	Flags Flags
	// LookupEnv resolves ${VAR}. Nil means os.LookupEnv.
	LookupEnv func(string) (string, bool)
}

// fileSettings mirrors Settings with pointers so an absent key stays
// distinguishable from a zero value. Every field here must have a counterpart in
// Settings; the strict walk is what keeps the two honest.
type fileSettings struct {
	Listen       *string       `yaml:"listen"`
	DBPath       *string       `yaml:"db_path"`
	PipelinesDir *string       `yaml:"pipelines_dir"`
	TemplatesDir *string       `yaml:"templates_dir"`
	Log          *logFile      `yaml:"log"`
	BrowseRoots  *[]BrowseRoot `yaml:"browse_roots"`
	Concurrency  *concFile     `yaml:"concurrency"`
	RetryLimit   *int          `yaml:"retry_limit"`
	Proxy        *string       `yaml:"proxy"`
	Webhook      *webhookFile  `yaml:"webhook"`
}

type logFile struct {
	Level  *LogLevel  `yaml:"level"`
	Format *LogFormat `yaml:"format"`
}

type concFile struct {
	Tasks    *int `yaml:"tasks"`
	Requests *int `yaml:"requests"`
	Images   *int `yaml:"images"`
}

type webhookFile struct {
	Enabled    *bool              `yaml:"enabled"`
	Token      *string            `yaml:"token"`
	Categories *map[string]string `yaml:"categories"`
}

// Load reads the process-level config. A missing file is not an error: every key
// has a default, and the first start must not need one.
func Load(path string, opts LoadOptions) (Settings, error) {
	lookup := opts.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	s := Defaults()

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := s.applyFile(path, data, lookup); err != nil {
			return Settings{}, err
		}
	case errors.Is(err, fs.ErrNotExist):
		// Defaults only.
	default:
		return Settings{}, fmt.Errorf("%s: %w", path, err)
	}

	s.applyFlags(opts.Flags)
	if err := s.Validate(); err != nil {
		return Settings{}, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

func (s *Settings) applyFile(path string, data []byte, lookup func(string) (string, bool)) error {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if root.Kind == 0 {
		return nil // empty file
	}
	fromEnv := map[*yaml.Node]bool{}
	if err := expandNodeTracked(&root, lookup, fromEnv); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return fmt.Errorf("%s:%d:%d: the file must be a mapping", path, doc.Line, doc.Column)
	}
	if probs := checkUnknownKeys(doc, typeOf(fileSettings{}), ""); len(probs) > 0 {
		p := probs[0]
		return fmt.Errorf("%s:%d:%d: unknown key %q", path, p.Line, p.Column, p.Path)
	}

	var f fileSettings
	if err := doc.Decode(&f); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	// Map every present key to the node it was written on, so its source can be
	// "env" when the value came out of a ${VAR} expansion.
	nodes := map[string]*yaml.Node{}
	collectNodes(doc, "", nodes)
	src := func(key string) Source {
		if n, ok := nodes[key]; ok && fromEnv[n] {
			return SourceEnv
		}
		return SourceFile
	}
	set := func(key string, node string) { s.sources[key] = src(node) }

	if f.Listen != nil {
		s.Listen = *f.Listen
		set("listen", "listen")
	}
	if f.DBPath != nil {
		s.DBPath = *f.DBPath
		set("db_path", "db_path")
	}
	if f.PipelinesDir != nil {
		s.PipelinesDir = *f.PipelinesDir
		set("pipelines_dir", "pipelines_dir")
	}
	if f.TemplatesDir != nil {
		s.TemplatesDir = *f.TemplatesDir
		set("templates_dir", "templates_dir")
	}
	if f.Log != nil {
		if f.Log.Level != nil {
			s.Log.Level = *f.Log.Level
			set("log.level", "log.level")
		}
		if f.Log.Format != nil {
			s.Log.Format = *f.Log.Format
			set("log.format", "log.format")
		}
	}
	if f.BrowseRoots != nil {
		s.BrowseRoots = *f.BrowseRoots
		for i := range s.BrowseRoots {
			if s.BrowseRoots[i].Label == "" {
				s.BrowseRoots[i].Label = s.BrowseRoots[i].Path
			}
			set(fmt.Sprintf("browse_roots[%d].path", i), fmt.Sprintf("browse_roots[%d].path", i))
			if s.BrowseRoots[i].Label != s.BrowseRoots[i].Path {
				set(fmt.Sprintf("browse_roots[%d].label", i), fmt.Sprintf("browse_roots[%d].label", i))
			}
		}
	}
	if f.Concurrency != nil {
		if f.Concurrency.Tasks != nil {
			s.Concurrency.Tasks = *f.Concurrency.Tasks
			set("concurrency.tasks", "concurrency.tasks")
		}
		if f.Concurrency.Requests != nil {
			s.Concurrency.Requests = *f.Concurrency.Requests
			set("concurrency.requests", "concurrency.requests")
		}
		if f.Concurrency.Images != nil {
			s.Concurrency.Images = *f.Concurrency.Images
			set("concurrency.images", "concurrency.images")
		}
	}
	if f.RetryLimit != nil {
		s.RetryLimit = *f.RetryLimit
		set("retry_limit", "retry_limit")
	}
	if f.Proxy != nil {
		s.Proxy = *f.Proxy
		set("proxy", "proxy")
	}
	if f.Webhook != nil {
		if f.Webhook.Enabled != nil {
			s.Webhook.Enabled = *f.Webhook.Enabled
			set("webhook.enabled", "webhook.enabled")
		}
		if f.Webhook.Token != nil {
			s.Webhook.Token = *f.Webhook.Token
			set("webhook.token", "webhook.token")
		}
		if f.Webhook.Categories != nil {
			s.Webhook.Categories = *f.Webhook.Categories
			for k := range s.Webhook.Categories {
				set("webhook.categories."+k, "webhook.categories."+k)
			}
		}
	}
	return nil
}

func (s *Settings) applyFlags(f Flags) {
	if f.Listen != nil {
		s.Listen = *f.Listen
		s.sources["listen"] = SourceFlag
	}
	if f.DBPath != nil {
		s.DBPath = *f.DBPath
		s.sources["db_path"] = SourceFlag
	}
	if f.PipelinesDir != nil {
		s.PipelinesDir = *f.PipelinesDir
		s.sources["pipelines_dir"] = SourceFlag
	}
	if f.TemplatesDir != nil {
		s.TemplatesDir = *f.TemplatesDir
		s.sources["templates_dir"] = SourceFlag
	}
	if f.LogLevel != nil {
		s.Log.Level = *f.LogLevel
		s.sources["log.level"] = SourceFlag
	}
	if f.LogFormat != nil {
		s.Log.Format = *f.LogFormat
		s.sources["log.format"] = SourceFlag
	}
}

// Validate checks the value-level rules. Unknown keys are caught earlier, by the
// strict walk, because only it knows where the key was written.
func (s Settings) Validate() error {
	var errs []string
	if strings.TrimSpace(s.Listen) == "" {
		errs = append(errs, "listen must not be empty")
	}
	if strings.TrimSpace(s.DBPath) == "" {
		errs = append(errs, "db_path must not be empty")
	}
	if !s.Log.Level.Valid() {
		errs = append(errs, fmt.Sprintf("log.level %q is not one of %s", s.Log.Level, joinEnum(Levels())))
	}
	if !s.Log.Format.Valid() {
		errs = append(errs, fmt.Sprintf("log.format %q is not one of %s", s.Log.Format, joinEnum(Formats())))
	}
	if len(s.BrowseRoots) == 0 {
		errs = append(errs, "browse_roots must not be empty")
	}
	for i, r := range s.BrowseRoots {
		if strings.TrimSpace(r.Path) == "" {
			errs = append(errs, fmt.Sprintf("browse_roots[%d].path must not be empty", i))
		}
	}
	for _, c := range []struct {
		key string
		val int
	}{
		{"concurrency.tasks", s.Concurrency.Tasks},
		{"concurrency.requests", s.Concurrency.Requests},
		{"concurrency.images", s.Concurrency.Images},
	} {
		if c.val < 1 {
			errs = append(errs, fmt.Sprintf("%s must be an integer >= 1, got %d", c.key, c.val))
		}
	}
	if s.RetryLimit < 0 {
		errs = append(errs, fmt.Sprintf("retry_limit must be >= 0, got %d", s.RetryLimit))
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// Effective returns the effective keys in display order, with secret values
// redacted. `mdc config check` prints exactly this.
func (s Settings) Effective() []KeyValue {
	out := []KeyValue{
		{"listen", s.Listen},
		{"db_path", s.DBPath},
		{"pipelines_dir", s.PipelinesDir},
		{"templates_dir", s.TemplatesDir},
		{"log.level", string(s.Log.Level)},
		{"log.format", string(s.Log.Format)},
	}
	for i, r := range s.BrowseRoots {
		out = append(out, KeyValue{fmt.Sprintf("browse_roots[%d].path", i), r.Path})
		if r.Label != r.Path {
			out = append(out, KeyValue{fmt.Sprintf("browse_roots[%d].label", i), r.Label})
		}
	}
	out = append(out,
		KeyValue{"concurrency.tasks", fmt.Sprint(s.Concurrency.Tasks)},
		KeyValue{"concurrency.requests", fmt.Sprint(s.Concurrency.Requests)},
		KeyValue{"concurrency.images", fmt.Sprint(s.Concurrency.Images)},
		KeyValue{"retry_limit", fmt.Sprint(s.RetryLimit)},
		KeyValue{"proxy", RedactURL(s.Proxy)},
		KeyValue{"webhook.enabled", fmt.Sprint(s.Webhook.Enabled)},
		KeyValue{"webhook.token", Secret(s.Webhook.Token)},
	)
	cats := make([]string, 0, len(s.Webhook.Categories))
	for k := range s.Webhook.Categories {
		cats = append(cats, k)
	}
	sort.Strings(cats)
	for _, k := range cats {
		out = append(out, KeyValue{"webhook.categories." + k, s.Webhook.Categories[k]})
	}
	return out
}

// KeyValue is one line of the effective-config listing.
type KeyValue struct {
	Key   string
	Value string
}

// joinEnum renders an enum's values for an error message.
func joinEnum[T ~string](vals []T) string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = string(v)
	}
	return strings.Join(out, ", ")
}

// Secret renders a secret without printing it. `mdc config check` never prints a
// secret value, so the only two states it can show are set and unset.
func Secret(v string) string {
	if v == "" {
		return "(unset)"
	}
	return "(set)"
}

// RedactURL removes the password from a URL's userinfo, if it has one. A proxy
// value may embed credentials, and `config check` prints it.
func RedactURL(raw string) string {
	if raw == "" {
		return "(none)"
	}
	at := strings.LastIndex(raw, "@")
	if at < 0 {
		return raw
	}
	schemeEnd := strings.Index(raw, "://")
	if schemeEnd < 0 || schemeEnd > at {
		return raw
	}
	userinfo := raw[schemeEnd+3 : at]
	user, _, hasPassword := strings.Cut(userinfo, ":")
	if !hasPassword {
		return raw
	}
	return raw[:schemeEnd+3] + user + ":***@" + raw[at+1:]
}
