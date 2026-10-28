package config

import (
	"fmt"
	"reflect"
	"strconv"

	"gopkg.in/yaml.v3"
)

func typeOf(v any) reflect.Type { return reflect.TypeOf(v) }

// expandNodeTracked is expandNode plus a record of which scalar nodes had a
// ${VAR} in them, so the value's source can be reported as "env".
func expandNodeTracked(n *yaml.Node, lookup func(string) (string, bool), fromEnv map[*yaml.Node]bool) error {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.ScalarNode && n.Tag == "!!str" && containsEnvRef(n.Value) {
		v, _, err := ExpandEnv(n.Value, lookup)
		if err != nil {
			return fmt.Errorf("line %d: %w", n.Line, err)
		}
		n.Value = v
		fromEnv[n] = true
	}
	for _, c := range n.Content {
		if err := expandNodeTracked(c, lookup, fromEnv); err != nil {
			return err
		}
	}
	return nil
}

func containsEnvRef(s string) bool {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '$' && s[i+1] == '{' {
			return true
		}
	}
	return false
}

// collectNodes maps each dotted key path in a document to the node holding its
// value. Sequence elements are indexed, so `browse_roots[0].path` resolves.
func collectNodes(n *yaml.Node, prefix string, out map[string]*yaml.Node) {
	if n == nil {
		return
	}
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			collectNodes(c, prefix, out)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, val := n.Content[i], n.Content[i+1]
			p := joinPath(prefix, key.Value)
			out[p] = val
			collectNodes(val, p, out)
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			p := prefix + "[" + strconv.Itoa(i) + "]"
			out[p] = c
			collectNodes(c, p, out)
		}
	}
}
