package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/2017fighting/mdc-lite/internal/field"
)

// This file declares the `with` surface of the eight V1 step types. It exists in
// the config package because the ticket's whole point is that a typo inside
// `with` must be refused at startup: the shape has to be typed here, not read out
// of a map. The step implementations in internal/pipeline decode the same nodes
// into the same structs.

// ScanWith configures `scan`. There is deliberately no `extensions` key:
// extensions are a filter rule, and filtering them at scan time would keep
// companion files from ever reaching `filter`.
type ScanWith struct {
	Roots   []string `yaml:"roots"`
	Exclude []string `yaml:"exclude"`
}

// FilterWith configures `filter`: an ordered rule list, first hit wins.
type FilterWith struct {
	Rules []FilterRule `yaml:"rules"`
}

// FilterRule is one candidate rule. `Name` is the key that lands in
// Candidate.Rule and in the step_run's skipped counts.
type FilterRule struct {
	Name string     `yaml:"name"`
	When FilterWhen `yaml:"when"`
	Then string     `yaml:"then"`
}

// FilterWhen is the condition of one rule. V1 opens exactly four keys.
type FilterWhen struct {
	ExtIn       []string `yaml:"ext_in"`
	ExtNotIn    []string `yaml:"ext_not_in"`
	MinSize     *int64   `yaml:"min_size"`
	StemMatches string   `yaml:"stem_matches"`
}

// GroupWith configures `group`. There is no distance knob: since ADR-0019 a work's
// identity is the number `parse` read, so there is nothing to measure between
// names. What is left is the table that decides which files are companions.
type GroupWith struct {
	CompanionExtensions []string `yaml:"companion_extensions"`
}

// ParseWith configures `parse`: the number rule table and the two tables that
// decide what text reaches it.
type ParseWith struct {
	Rules []ParseRule `yaml:"rules"`
	// Sanitize is the user's noise table, stripped before the rules run. A bare
	// string is a literal; `r:` makes it a Go regexp.
	Sanitize []string `yaml:"sanitize"`
	// PartPatterns are user part-marker patterns: they must match at the end of
	// the stripped name, and their first capture group is the part number.
	PartPatterns []string `yaml:"part_patterns"`
}

// ParseRule is either a builtin expansion or one user pattern.
type ParseRule struct {
	Builtin string `yaml:"builtin"`
	Pattern string `yaml:"pattern"`
	Family  string `yaml:"family"`
}

// ScrapeDefaults are the documented defaults of `scrape.with`: the global source
// order, the per-field order written out for all 14 fields, and a 5m budget for one
// number's whole scrape unit.
//
// The per-field table is a complete replacement for the global order, so the
// factory default spells out every field instead of leaving the operator to guess
// which sources are candidates for which field. It is also capability-correct: a
// source that does not declare a field is not in that field's list, because the
// loader refuses `priority[field]` naming such a source.
func ScrapeDefaults() ScrapeWith {
	return ScrapeWith{
		Sources:  SourceOrder{{ID: "javdb"}, {ID: "dmm"}, {ID: "javbus"}, {ID: "fc2"}},
		Priority: DefaultPriority(),
		Timeout:  Duration(5 * time.Minute),
	}
}

// DefaultPriority is the factory field-level order: every one of the 14 fields, in
// the global order, minus the sources that do not declare that field.
func DefaultPriority() map[string][]string {
	return map[string][]string{
		string(field.Title):        {"javdb", "dmm", "javbus", "fc2"},
		string(field.Plot):         {"javdb", "dmm", "fc2"},
		string(field.Fanart):       {"javdb", "dmm", "javbus", "fc2"},
		string(field.Extrafanarts): {"javdb", "dmm", "fc2"},
		string(field.Tags):         {"javdb", "dmm", "javbus", "fc2"},
		string(field.Releasedate):  {"javdb", "dmm", "javbus", "fc2"},
		string(field.Actors):       {"javdb", "dmm", "javbus"},
		string(field.Poster):       {"javdb", "dmm"},
		string(field.Director):     {"javdb", "dmm", "javbus"},
		string(field.Runtime):      {"javdb", "dmm", "javbus", "fc2"},
		string(field.Rating):       {"javdb", "dmm"},
		string(field.Series):       {"javdb", "dmm", "javbus"},
		string(field.Studio):       {"javdb", "dmm", "javbus", "fc2"},
		string(field.Publisher):    {"javdb", "dmm", "javbus"},
	}
}

// ScrapeWith configures `scrape`: the global source order, per-field overrides,
// the whole-number timeout, and the per-source knobs.
//
// `sources` is the ordered global list, and each entry is either a bare id or an
// object carrying that source's knobs. One key, one meaning: the order and the
// knobs belong to the same list, so a source's settings cannot drift away from its
// position.
type ScrapeWith struct {
	Sources  SourceOrder         `yaml:"sources"`
	Priority map[string][]string `yaml:"priority"`
	Timeout  Duration            `yaml:"timeout"`
}

// UnmarshalYAML keeps the priority table's two meanings apart.
//
// A table written in the file replaces the factory's table wholesale: an unlisted
// field falls back to `sources`, not to the factory's list for that field. Decoding
// into a defaulted map would merge the two, and a user who wrote one field's order
// would silently keep the factory's order for the other thirteen.
//
// The factory table also belongs to the factory source list: a file that writes its
// own `sources` but no `priority` gets that list as the per-field order. Otherwise
// the factory table's ids would still be the candidates, a narrowed source list
// would schedule nothing, and `sources: [javbus]` would quietly scrape nothing at
// all.
func (w *ScrapeWith) UnmarshalYAML(n *yaml.Node) error {
	type plain ScrapeWith
	// Presence is asked of the decoder, not of the raw key nodes: a `<<: *anchor`
	// merge brings `sources` or `priority` in under a `!!merge` key, and a key walk
	// that only matches literal names would miss it and let the factory table leak
	// into a file that wrote its own.
	var probe struct {
		Sources  yaml.Node           `yaml:"sources"`
		Priority map[string][]string `yaml:"priority"`
	}
	if err := n.Decode(&probe); err != nil {
		return err
	}
	if probe.Sources.Kind != 0 || probe.Priority != nil {
		w.Priority = nil
	}
	return n.Decode((*plain)(w))
}

// SourceOrder is the ordered global source list.
type SourceOrder []SourceEntry

// SourceEntry is one source in the global order: an id, optionally with knobs.
type SourceEntry struct {
	ID string `yaml:"id"`

	SourceWith `yaml:",inline"`
}

// UnmarshalYAML accepts either a bare id string or a mapping with an `id`.
func (e *SourceEntry) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		e.ID = n.Value
		return nil
	}
	type plain SourceEntry
	return n.Decode((*plain)(e))
}

// IDs returns the ordered source ids.
func (o SourceOrder) IDs() []string {
	out := make([]string, len(o))
	for i, e := range o {
		out[i] = e.ID
	}
	return out
}

// Knobs returns the per-source settings that were actually written, keyed by id.
func (o SourceOrder) Knobs() map[string]SourceWith {
	out := make(map[string]SourceWith, len(o))
	for _, e := range o {
		if e.Proxy != "" || len(e.Domains) > 0 || e.Rate != nil || e.Timeout != 0 {
			out[e.ID] = e.SourceWith
		}
	}
	return out
}

// SourceWith holds the four knobs a single source exposes: proxy, domains, rate
// and timeout. Everything else (UA, cookies, image Referer, signature constants)
// is a code constant.
type SourceWith struct {
	Proxy   string   `yaml:"proxy"`
	Domains []string `yaml:"domains"`
	Rate    *Rate    `yaml:"rate"`
	Timeout Duration `yaml:"timeout"`
}

// Rate overrides the default rate limit and cooldown for one source.
type Rate struct {
	RequestsPerSecond float64  `yaml:"requests_per_second"`
	Burst             int      `yaml:"burst"`
	Cooldown          Duration `yaml:"cooldown"`
}

// ImagesWith configures `images`. V1 does one thing: crop a poster out of the
// right side of the fanart when the task has no poster.
type ImagesWith struct{}

// NfoWith configures `nfo`.
type NfoWith struct {
	Template string `yaml:"template"`
}

// OutputDefaults are the documented defaults of `output.with`. `root` has none.
//
// `file_template` is the naming rule in template form: `{number}[-cd{part}][-中文]
// [-破解]`. `part` renders empty for a part the source's name never marked, so a
// work that is not split has no `-cd1`, and `version` carries the version suffix.
func OutputDefaults() OutputWith {
	return OutputWith{
		DirTemplate:    "{{ number }}",
		FileTemplate:   "{{ number }}{% if part %}-cd{{ part }}{% endif %}{{ version }}",
		Link:           "hardlink",
		RequiredFields: []string{string(field.Title), string(field.Fanart)},
	}
}

// OutputWith configures `output`, the only step that writes to disk.
type OutputWith struct {
	Root           string   `yaml:"root"`
	DirTemplate    string   `yaml:"dir_template"`
	FileTemplate   string   `yaml:"file_template"`
	Link           string   `yaml:"link"`
	RequiredFields []string `yaml:"required_fields"`
}

// V1 step types, in the order the spec lists them.
const (
	StepScan   = "scan"
	StepFilter = "filter"
	StepGroup  = "group"
	StepParse  = "parse"
	StepScrape = "scrape"
	StepImages = "images"
	StepNfo    = "nfo"
	StepOutput = "output"
)

// DefaultStepRegistry returns the registry of V1 step types.
func DefaultStepRegistry() *Registry {
	return NewRegistry(
		StepSpec{Type: StepScan, NewWith: func() any { return &ScanWith{} }, Produces: []string{ArtifactFileSet}},
		StepSpec{Type: StepFilter, NewWith: func() any { return &FilterWith{} }, Requires: []string{ArtifactFileSet}, Produces: []string{ArtifactCandidates}},
		StepSpec{Type: StepParse, NewWith: func() any { return &ParseWith{} }, Requires: []string{ArtifactCandidates}, Produces: []string{ArtifactParsed}},
		StepSpec{Type: StepGroup, NewWith: func() any { g := GroupDefaults(); return &g }, Requires: []string{ArtifactParsed}, Produces: []string{ArtifactGroups}},
		StepSpec{Type: StepScrape, NewWith: func() any { w := ScrapeDefaults(); return &w }, Requires: []string{ArtifactParsed}, Produces: []string{ArtifactScraped}},
		StepSpec{Type: StepImages, NewWith: func() any { return &ImagesWith{} }, Requires: []string{ArtifactScraped}, Produces: []string{ArtifactImages}},
		StepSpec{Type: StepNfo, NewWith: func() any { return &NfoWith{} }, Requires: []string{ArtifactScraped}, Produces: []string{ArtifactNfoDoc}},
		StepSpec{Type: StepOutput, NewWith: func() any { w := OutputDefaults(); return &w }, Requires: []string{ArtifactParsed}, Produces: []string{ArtifactManifest}},
	)
}

func itoa(i int) string { return strconv.Itoa(i) }

var (
	extPattern      = regexp.MustCompile(`^\.[a-z0-9]+$`)
	linkModes       = map[string]bool{"hardlink": true, "copy": true, "move": true}
	templateVarRef  = regexp.MustCompile(`\{\{.*?\}\}`)
	filterThenKinds = map[string]bool{"include": true, "exclude": true}
)

// Validate checks the filter rules: `then` is an enum, `name` is unique and
// required, and each `when` uses at least one of the four V1 keys.
func (w *FilterWith) Validate() error {
	if len(w.Rules) == 0 {
		return fmt.Errorf("rules must not be empty")
	}
	seen := map[string]bool{}
	for i, r := range w.Rules {
		if strings.TrimSpace(r.Name) == "" {
			return fmt.Errorf("rules[%d].name is required", i)
		}
		if seen[r.Name] {
			return fmt.Errorf("rules[%d].name %q is used twice", i, r.Name)
		}
		seen[r.Name] = true
		if !filterThenKinds[r.Then] {
			return fmt.Errorf("rules[%d].then %q is not one of include, exclude", i, r.Then)
		}
		if err := r.When.validate(); err != nil {
			return fmt.Errorf("rules[%d].when: %w", i, err)
		}
	}
	return nil
}

func (w FilterWhen) validate() error {
	n := 0
	for _, exts := range [][]string{w.ExtIn, w.ExtNotIn} {
		if exts == nil {
			continue
		}
		n++
		for _, e := range exts {
			if !extPattern.MatchString(e) {
				return fmt.Errorf("extension %q must look like .mp4", e)
			}
		}
	}
	if w.MinSize != nil {
		n++
		if *w.MinSize < 0 {
			return fmt.Errorf("min_size must be >= 0")
		}
	}
	if w.StemMatches != "" {
		n++
		if _, err := regexp.Compile(w.StemMatches); err != nil {
			return fmt.Errorf("stem_matches: %w", err)
		}
	}
	if n == 0 {
		return fmt.Errorf("needs one of ext_in, ext_not_in, min_size, stem_matches")
	}
	if n > 1 {
		return fmt.Errorf("uses more than one of ext_in, ext_not_in, min_size, stem_matches")
	}
	return nil
}

// GroupDefaults are the documented defaults of `group.with`.
func GroupDefaults() GroupWith {
	return GroupWith{
		CompanionExtensions: []string{".srt", ".ass", ".ssa", ".jpg", ".jpeg", ".png", ".webp"},
	}
}

// Validate checks the group knobs.
func (w *GroupWith) Validate() error {
	for _, e := range w.CompanionExtensions {
		if !extPattern.MatchString(e) {
			return fmt.Errorf("companion_extensions entry %q must look like .srt", e)
		}
	}
	return nil
}

// Validate checks the parse rule table and the two stripping tables.
func (w *ParseWith) Validate() error {
	// An empty table numbers nothing, so every group would have to be typed in by
	// hand. That is a misconfiguration, not a default: a step that deliberately
	// wants no built-in rules still writes a `{pattern: …}` entry.
	if len(w.Rules) == 0 {
		return fmt.Errorf("rules must not be empty (write `- {builtin: default}` for the built-in set, or your own `- {pattern: …}` entries)")
	}
	for i, r := range w.Rules {
		switch {
		case r.Builtin != "" && r.Pattern != "":
			return fmt.Errorf("rules[%d]: builtin and pattern are mutually exclusive", i)
		case r.Builtin != "":
			if r.Builtin != "default" {
				return fmt.Errorf("rules[%d].builtin %q is not a builtin set (only \"default\")", i, r.Builtin)
			}
		case r.Pattern != "":
			if _, err := regexp.Compile(r.Pattern); err != nil {
				return fmt.Errorf("rules[%d].pattern: %w", i, err)
			}
		default:
			return fmt.Errorf("rules[%d]: needs builtin or pattern", i)
		}
	}
	for _, p := range w.PartPatterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return fmt.Errorf("part_patterns: %w", err)
		}
		// The first capture group is the part number. A pattern without one would
		// load, match, and then decide nothing — a key that does nothing is worse
		// than a key that is absent.
		if re.NumSubexp() < 1 {
			return fmt.Errorf("part_patterns %q needs a capture group holding the part number, e.g. `(?:^|[-_. ])slice(\\d)$`", p)
		}
	}
	for _, p := range w.Sanitize {
		re := strings.TrimPrefix(p, "r:")
		if _, err := regexp.Compile(re); err != nil {
			return fmt.Errorf("sanitize %q: %w", p, err)
		}
	}
	return nil
}

// Validate checks the scrape knobs.
func (w *ScrapeWith) Validate() error {
	if len(w.Sources) == 0 {
		return fmt.Errorf("sources must not be empty")
	}
	seen := map[string]bool{}
	for i, e := range w.Sources {
		if strings.TrimSpace(e.ID) == "" {
			return fmt.Errorf("sources[%d].id must not be empty", i)
		}
		if seen[e.ID] {
			return fmt.Errorf("sources: %q is listed twice", e.ID)
		}
		seen[e.ID] = true
		if err := e.validate("sources[" + itoa(i) + "]"); err != nil {
			return err
		}
	}
	for f, order := range w.Priority {
		if !field.Valid(f) {
			return fmt.Errorf("priority: %q is not one of the 14 source-level fields", f)
		}
		if len(order) == 0 {
			return fmt.Errorf("priority.%s must not be empty", f)
		}
		seen := map[string]bool{}
		for _, id := range order {
			if strings.TrimSpace(id) == "" {
				return fmt.Errorf("priority.%s has an empty source id", f)
			}
			if seen[id] {
				return fmt.Errorf("priority.%s lists %q twice", f, id)
			}
			seen[id] = true
			// A field's order is drawn from the pipeline's own sources: an id the
			// pipeline did not configure would be silently dropped at run time, and
			// the field would quietly have one fewer candidate.
			if !containsID(w.Sources, id) {
				return fmt.Errorf("priority.%s names %q, which is not in sources", f, id)
			}
		}
	}
	if w.Timeout < 0 {
		return fmt.Errorf("timeout must be >= 0")
	}
	return nil
}

// containsID reports whether the ordered source list names an id.
func containsID(order SourceOrder, id string) bool {
	for _, entry := range order {
		if entry.ID == id {
			return true
		}
	}
	return false
}

// validate checks one source's knobs. The key is the location the value was
// written at, so the error names the key the operator can actually go and fix.
func (o SourceWith) validate(key string) error {
	if o.Timeout < 0 {
		return fmt.Errorf("%s.timeout must be >= 0", key)
	}
	for i, d := range o.Domains {
		if strings.TrimSpace(d) == "" {
			return fmt.Errorf("%s.domains[%d] must not be empty", key, i)
		}
		// A domain is a bare host or a full URL (the fixture form). A full URL with
		// no host would otherwise produce a request to nothing at run time.
		if strings.Contains(d, "://") {
			u, err := url.Parse(d)
			if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
				return fmt.Errorf("%s.domains[%d] %q is not a host or an http(s) URL", key, i, d)
			}
		}
	}
	if o.Rate != nil && (o.Rate.RequestsPerSecond < 0 || o.Rate.Burst < 0 || o.Rate.Cooldown < 0) {
		return fmt.Errorf("%s.rate values must be >= 0", key)
	}
	return nil
}

// Validate checks the output step. `root` has no default: an output step that
// does not say where it writes is a config error, not a guess.
func (w *OutputWith) Validate() error {
	if strings.TrimSpace(w.Root) == "" {
		return fmt.Errorf("root is required")
	}
	if w.Link != "" && !linkModes[w.Link] {
		return fmt.Errorf("link %q is not one of hardlink, copy, move", w.Link)
	}
	for _, t := range []struct{ key, val string }{
		{"dir_template", w.DirTemplate},
		{"file_template", w.FileTemplate},
	} {
		if t.val == "" {
			continue
		}
		if err := checkTemplateVars(t.val); err != nil {
			return fmt.Errorf("%s: %w", t.key, err)
		}
	}
	for _, f := range w.RequiredFields {
		if !field.Valid(f) {
			return fmt.Errorf("required_fields: %q is not one of the 14 source-level fields", f)
		}
	}
	return nil
}

// checkTemplateVars rejects a template that names a variable the render context
// does not have. Only the shape is checked here; rendering belongs to the step.
func checkTemplateVars(tpl string) error {
	allowed := map[string]bool{
		string(field.Number): true,
		"part":               true,
		// The naming rule's version half: the suffix as a whole, and its two
		// halves for a template that spells them its own way.
		"version":    true,
		"chinese":    true,
		"uncensored": true,
	}
	for _, f := range field.All() {
		allowed[string(f)] = true
	}
	for _, ref := range templateVarRef.FindAllString(tpl, -1) {
		name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(ref, "{{"), "}}"))
		name = strings.TrimSpace(strings.TrimSuffix(name, "|"))
		if name == "" {
			return fmt.Errorf("%q is not a variable reference", ref)
		}
		base := strings.Fields(name)[0]
		if !allowed[base] {
			return fmt.Errorf("%q names an unknown variable (allowed: number, part, version, chinese, uncensored and the 14 source-level fields)", base)
		}
	}
	return nil
}

// Validate checks the nfo step's template selector.
func (w *NfoWith) Validate() error {
	if w.Template == "" {
		return nil
	}
	if err := CheckTemplateSelector(w.Template); err != nil {
		return err
	}
	return nil
}

// CheckTemplateSelector applies the three-way rule: `@name` is a file under
// templates_dir, anything containing `{{` or a newline is inline, and anything
// else is a builtin name.
func CheckTemplateSelector(sel string) error {
	if strings.HasPrefix(sel, "@") {
		name := strings.TrimPrefix(sel, "@")
		if name == "" {
			return fmt.Errorf("template %q: the file name after @ is empty", sel)
		}
		if strings.HasPrefix(name, "/") {
			return fmt.Errorf("template %q: absolute paths are not allowed", sel)
		}
		if strings.Contains(name, "..") {
			return fmt.Errorf("template %q: `..` is not allowed", sel)
		}
		return nil
	}
	if strings.Contains(sel, "{{") || strings.Contains(sel, "\n") {
		return nil // inline
	}
	if sel != "default" {
		return fmt.Errorf("template %q is not a builtin name (only \"default\"); use @file for a file or inline text with {{ }}", sel)
	}
	return nil
}
