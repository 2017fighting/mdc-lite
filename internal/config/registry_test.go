package config_test

import (
	"strings"
	"testing"

	"github.com/2017fighting/mdc-lite/internal/config"
)

// contract is one step type's declared artifacts, in declaration order.
type contract struct {
	requires []string
	produces []string
}

func TestDefaultStepRegistry_ParseBeforeGroupContracts(t *testing.T) {
	// ADR-0019: parse consumes the filter's Candidates and produces Parsed;
	// group consumes Parsed and produces Groups. Getting these two backwards is
	// what makes the old order (group before parse) load, so they are pinned
	// separately from the pipeline that uses them.
	want := map[string]contract{
		config.StepParse: {
			requires: []string{config.ArtifactCandidates},
			produces: []string{config.ArtifactParsed},
		},
		config.StepGroup: {
			requires: []string{config.ArtifactParsed},
			produces: []string{config.ArtifactGroups},
		},
	}
	reg := config.DefaultStepRegistry()
	for typ, w := range want {
		spec, ok := reg.Lookup(typ)
		if !ok {
			t.Fatalf("%s is not in the default registry", typ)
		}
		if got := strings.Join(spec.Requires, ","); got != strings.Join(w.requires, ",") {
			t.Errorf("%s requires %q, want %q", typ, got, strings.Join(w.requires, ","))
		}
		if got := strings.Join(spec.Produces, ","); got != strings.Join(w.produces, ",") {
			t.Errorf("%s produces %q, want %q", typ, got, strings.Join(w.produces, ","))
		}
	}
}

// batchPipeline is scan → filter, then the given batch-level steps, then output.
func batchPipeline(steps string) string {
	return head + filterOnly + steps + `  - type: output
    with: {root: /o}
`
}

const groupStep = `  - type: group
    with: {}
`

func TestParsePipeline_BatchPrefixParsesBeforeGrouping(t *testing.T) {
	p, err := parse(t, batchPipeline(defaultParse+groupStep))
	if err != nil {
		t.Fatalf("scan → filter → parse → group must be a legal topology: %v", err)
	}
	if got := p.Steps[2].Type; got != config.StepParse {
		t.Errorf("step 3 = %q, want parse", got)
	}
	if got := p.Steps[3].Type; got != config.StepGroup {
		t.Errorf("step 4 = %q, want group", got)
	}
}

func TestParsePipeline_GroupBeforeParseIsRefused(t *testing.T) {
	// The old order (ADR-0002) is refused by the artifact chain, not by a special
	// case: group needs Parsed, and nothing before it produces one.
	_, err := parse(t, batchPipeline(groupStep+defaultParse))
	if err == nil {
		t.Fatal("group before parse must be refused")
	}
	if !strings.Contains(err.Error(), "step 3 (group)") || !strings.Contains(err.Error(), "Parsed") {
		t.Errorf("got: %v", err)
	}
}
