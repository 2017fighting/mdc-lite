package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Pipeline is one pipeline file. One file = one complete pipeline; there is no
// hierarchical merge and no `id` key — the id is the file name's stem.
type Pipeline struct {
	ID    string
	File  string
	Name  string
	Steps []Step

	registry *Registry
}

// Step is one step of a pipeline. The metadata layer is exactly four keys:
// type / with / name / on_error. Everything a step is configured with lives
// under `with`, which is kept as a node so it can be decoded into the step
// type's own struct (and walked for unknown keys) after the type is known.
type Step struct {
	Type    string
	Name    string
	OnError OnError
	With    *yaml.Node

	WithLine, WithColumn int
	// Line and Column locate the step's own mapping in the file, so an error about
	// the step as a whole can point at it.
	Line, Column int
}

// OnError is the step-level failure policy.
type OnError string

const (
	// OnErrorFail stops the pipeline when the step fails. It is the default.
	OnErrorFail OnError = "fail"
	// OnErrorContinue lets the pipeline continue, marking the product partial.
	OnErrorContinue OnError = "continue"
)

// rawPipeline / rawStep are the decode targets for the outer, type-independent
// layer. `with` stays a node on purpose: a map[string]any would swallow a typo,
// and re-marshalling the node to decode it strictly would lose its position.
type rawPipeline struct {
	Name  string    `yaml:"name"`
	Steps []rawStep `yaml:"steps"`
}

type rawStep struct {
	Type    string    `yaml:"type"`
	Name    string    `yaml:"name"`
	OnError string    `yaml:"on_error"`
	With    yaml.Node `yaml:"with"`
}

// PipelineID is the pipeline id a file defines: the file name without its
// extension. There is no `id` key; a reference is always this stem.
func PipelineID(path string) string {
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

// PipelineFiles lists the pipeline files in dir: top-level *.yaml and *.yml only,
// never recursive. A symlink that points at a directory is an error rather than a
// file to read. Renaming a file's extension is how a pipeline is temporarily
// disabled.
func PipelineFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	var files []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		ext := filepath.Ext(name)
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		p := filepath.Join(dir, name)
		info, err := os.Lstat(p)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Stat(p)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", p, err)
			}
			if target.IsDir() {
				return nil, fmt.Errorf("%s: symbolic link points at a directory, not a pipeline file", p)
			}
		} else if info.IsDir() {
			continue
		}
		files = append(files, p)
	}
	sort.Strings(files)
	return files, nil
}

// LoadPipelines reads and validates every pipeline file in dir.
//
// It loads the whole directory, including files no trigger references: a
// pipeline that nothing uses today is one a trigger will use tomorrow, and it
// must not be the one that fails at 3am.
func LoadPipelines(dir string, reg *Registry, env Env) ([]Pipeline, error) {
	files, err := PipelineFiles(dir)
	if err != nil {
		return nil, err
	}
	pipelines := make([]Pipeline, 0, len(files))
	for _, f := range files {
		p, err := LoadPipelineFile(f, reg, env)
		if err != nil {
			return nil, err
		}
		pipelines = append(pipelines, p)
	}
	return pipelines, nil
}

// LoadPipelineFile loads one pipeline file. The returned error is already shaped
// as 文件:行:列: 第 N 步 (type): ...
func LoadPipelineFile(path string, reg *Registry, env Env) (Pipeline, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Pipeline{}, fmt.Errorf("%s: %w", path, err)
	}
	p, err := ParsePipeline(path, data, reg, env)
	if err != nil {
		return Pipeline{}, err
	}
	return p, nil
}

// ParsePipeline parses one pipeline document.
func ParsePipeline(path string, data []byte, reg *Registry, env Env) (Pipeline, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return Pipeline{}, fmt.Errorf("%s: %w", path, err)
	}
	if root.Kind == 0 || len(root.Content) == 0 {
		return Pipeline{}, fmt.Errorf("%s: the file is empty", path)
	}
	lookup := env
	if lookup == nil {
		lookup = os.LookupEnv
	}
	fromEnv := map[*yaml.Node]bool{}
	if err := expandNodeTracked(&root, lookup, fromEnv); err != nil {
		return Pipeline{}, fmt.Errorf("%s: %w", path, err)
	}

	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return Pipeline{}, fmt.Errorf("%s:%d:%d: the file must be a mapping", path, doc.Line, doc.Column)
	}
	if probs := checkUnknownKeys(doc, typeOf(rawPipeline{}), ""); len(probs) > 0 {
		p := probs[0]
		return Pipeline{}, fmt.Errorf("%s:%d:%d: unknown key %q", path, p.Line, p.Column, p.Path)
	}

	var rp rawPipeline
	if err := doc.Decode(&rp); err != nil {
		return Pipeline{}, fmt.Errorf("%s: %w", path, err)
	}

	id := PipelineID(path)
	p := Pipeline{ID: id, File: path, Name: rp.Name, Steps: make([]Step, 0, len(rp.Steps)), registry: reg}
	if p.Name == "" {
		p.Name = id
	}

	stepNodes := stepNodesOf(doc)
	for i, rs := range rp.Steps {
		st := Step{Type: rs.Type, Name: rs.Name, OnError: OnError(rs.OnError), WithLine: rs.With.Line, WithColumn: rs.With.Column}
		if st.OnError == "" {
			st.OnError = OnErrorFail
		}
		if rs.With.Kind != 0 {
			n := rs.With
			st.With = &n
		}
		if i < len(stepNodes) {
			st.Line, st.Column = stepNodes[i].Line, stepNodes[i].Column
		}
		if err := validateStep(st, i, path); err != nil {
			return Pipeline{}, err
		}
		if reg != nil {
			spec, ok := reg.Lookup(st.Type)
			if !ok {
				return Pipeline{}, stepErr(path, st, i, "unknown step type %q (known: %s)", st.Type, strings.Join(reg.Types(), ", "))
			}
			if err := decodeWith(st, spec, path, i); err != nil {
				return Pipeline{}, err
			}
		}
		p.Steps = append(p.Steps, st)
	}

	if err := p.Validate(); err != nil {
		return Pipeline{}, err
	}
	return p, nil
}

// stepNodesOf returns the mapping node of each element of the top-level `steps`
// sequence, so errors can point at the step's first line.
func stepNodesOf(doc *yaml.Node) []*yaml.Node {
	if doc.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		if doc.Content[i].Value != "steps" {
			continue
		}
		seq := doc.Content[i+1]
		if seq.Kind != yaml.SequenceNode {
			return nil
		}
		return seq.Content
	}
	return nil
}

// stepErr renders the documented error shape: 文件:行:列: 第 N 步 (type): ...
// N is 1-based in the message because that is how a human counts steps in a file.
func stepErr(path string, st Step, idx int, format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	if st.Line == 0 {
		return fmt.Errorf("%s: step %d (%s): %s", path, idx+1, st.Type, msg)
	}
	return fmt.Errorf("%s:%d:%d: step %d (%s): %s", path, st.Line, st.Column, idx+1, st.Type, msg)
}

// decodeWith strictly decodes the step's `with` node into the step type's own
// struct and runs its Validate.
func decodeWith(st Step, spec StepSpec, path string, idx int) error {
	if st.With == nil {
		return stepErr(path, st, idx, "`with` is required")
	}
	isNull := st.With.Kind == yaml.ScalarNode && st.With.Tag == "!!null"
	if st.With.Kind != yaml.MappingNode && !isNull {
		return fmt.Errorf("%s:%d:%d: step %d (%s): `with` must be a mapping", path, st.With.Line, st.With.Column, idx+1, st.Type)
	}
	probs := checkUnknownKeys(st.With, typeOf(spec.With), "")
	if len(probs) > 0 {
		p := probs[0]
		return fmt.Errorf("%s:%d:%d: step %d (%s): unknown field %q", path, p.Line, p.Column, idx+1, st.Type, p.Path)
	}
	if err := st.With.Decode(spec.With); err != nil {
		return fmt.Errorf("%s:%d:%d: step %d (%s): %w", path, st.With.Line, st.With.Column, idx+1, st.Type, err)
	}
	if v, ok := spec.With.(Validator); ok {
		if err := v.Validate(); err != nil {
			return fmt.Errorf("%s:%d:%d: step %d (%s): %w", path, st.With.Line, st.With.Column, idx+1, st.Type, err)
		}
	}
	return nil
}

// validateStep checks the type-independent rules of the metadata layer.
func validateStep(st Step, idx int, path string) error {
	if strings.TrimSpace(st.Type) == "" {
		return stepErr(path, st, idx, "`type` is required")
	}
	switch st.OnError {
	case OnErrorFail, OnErrorContinue:
	default:
		return stepErr(path, st, idx, "on_error %q is not one of fail, continue", st.OnError)
	}
	return nil
}

// Validate runs the pipeline-level rules: no repeated type without a name, the
// artifact chain must connect, and at least one step must write to disk.
func (p Pipeline) Validate() error {
	if len(p.Steps) == 0 {
		return fmt.Errorf("%s: the pipeline has no steps", p.File)
	}
	seen := map[string]int{}
	names := map[string]int{}
	for i, st := range p.Steps {
		if st.Name != "" {
			if _, dup := names[st.Name]; dup {
				return stepErr(p.File, st, i, "step name %q is used twice", st.Name)
			}
			names[st.Name] = i
		}
		seen[st.Type]++
	}
	for i, st := range p.Steps {
		if seen[st.Type] > 1 && st.Name == "" {
			return stepErr(p.File, st, i, "type %q appears more than once, so each occurrence needs a `name`", st.Type)
		}
	}
	if !hasWrites(p) {
		return fmt.Errorf("%s: the pipeline has no step that writes to disk (needs an `output` step)", p.File)
	}
	return checkTopology(p)
}
