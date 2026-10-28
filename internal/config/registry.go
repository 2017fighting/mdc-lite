package config

import (
	"fmt"
	"sort"
	"strings"
)

// Env resolves an environment variable for ${VAR} interpolation.
type Env func(string) (string, bool)

// Validator is implemented by a step's `with` struct when it has value-level
// rules beyond the YAML shape.
type Validator interface {
	Validate() error
}

// StepSpec is one step type's configuration surface: the struct its `with` maps
// onto, and the artifacts it needs and produces.
type StepSpec struct {
	// Type is the value of `type:` that selects this step.
	Type string
	// With is a pointer to a zero value of the step's `with` struct. It is
	// decoded into, so it must be a pointer to a struct.
	With any
	// Requires lists the artifact kinds this step consumes.
	Requires []string
	// Produces lists the artifact kinds this step emits.
	Produces []string
}

// Registry is the set of step types the binary knows.
type Registry struct {
	specs map[string]StepSpec
}

// NewRegistry builds a registry from specs.
func NewRegistry(specs ...StepSpec) *Registry {
	r := &Registry{specs: make(map[string]StepSpec, len(specs))}
	for _, s := range specs {
		r.specs[s.Type] = s
	}
	return r
}

// Lookup returns the spec for a step type.
func (r *Registry) Lookup(typ string) (StepSpec, bool) {
	if r == nil {
		return StepSpec{}, false
	}
	s, ok := r.specs[typ]
	return s, ok
}

// Types returns every known step type, sorted, for error messages.
func (r *Registry) Types() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.specs))
	for k := range r.specs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Artifact kinds, as named in the step contracts.
const (
	ArtifactFileSet    = "FileSet"
	ArtifactCandidates = "Candidates"
	ArtifactGroups     = "Groups"
	ArtifactParsed     = "Parsed"
	ArtifactScraped    = "Scraped"
	ArtifactImages     = "Images"
	ArtifactNfoDoc     = "NfoDoc"
	ArtifactManifest   = "Manifest"
)

// hasWrites reports whether the pipeline has a step that writes to disk.
func hasWrites(p Pipeline) bool {
	for _, st := range p.Steps {
		if st.Type == "output" {
			return true
		}
	}
	return false
}

// checkTopology walks the declared sequence and checks that each step's required
// artifact was produced by an earlier step. Optional steps may be omitted, which
// is why the check accumulates rather than comparing neighbours.
func checkTopology(p Pipeline) error {
	available := map[string]bool{}
	for i, st := range p.Steps {
		for _, need := range requiresOf(p, st) {
			if !available[need] {
				return stepErr(p.File, st, i,
					"needs %s, which no earlier step produces (steps before it: %s)",
					need, describeSteps(p.Steps[:i]))
			}
		}
		for _, has := range producesOf(p, st) {
			available[has] = true
		}
	}
	return nil
}

func describeSteps(steps []Step) string {
	if len(steps) == 0 {
		return "none"
	}
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = s.Type
	}
	return strings.Join(out, ", ")
}

func requiresOf(p Pipeline, st Step) []string {
	if p.registry != nil {
		if spec, ok := p.registry.Lookup(st.Type); ok {
			return spec.Requires
		}
	}
	return nil
}

func producesOf(p Pipeline, st Step) []string {
	if p.registry != nil {
		if spec, ok := p.registry.Lookup(st.Type); ok {
			return spec.Produces
		}
	}
	return nil
}

// String renders a pipeline the way `mdc config check` lists it.
func (p Pipeline) String() string {
	types := make([]string, len(p.Steps))
	for i, s := range p.Steps {
		if s.Name != "" {
			types[i] = s.Type + "(" + s.Name + ")"
		} else {
			types[i] = s.Type
		}
	}
	return fmt.Sprintf("%s: %s", p.ID, strings.Join(types, " → "))
}
