package config

import (
	"fmt"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// problem is one unknown-key finding, located at the key that caused it.
type problem struct {
	Line, Column int
	Path         string
	Field        string
}

// checkUnknownKeys walks a decoded YAML node against a struct type and reports
// every mapping key that has no matching yaml tag.
//
// This is the one place strictness lives. Decoding into map[string]any silently
// swallows a typo, and re-marshalling a sub-node to get a strict decode throws
// away the position, so the walk has to happen on the original node tree: the
// positions are the ones from the file.
func checkUnknownKeys(n *yaml.Node, t reflect.Type, path string) []problem {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.DocumentNode {
		if len(n.Content) == 0 {
			return nil
		}
		return checkUnknownKeys(n.Content[0], t, path)
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == reflect.TypeOf(yaml.Node{}) {
		// A node is an opaque subtree: it is walked by whoever knows its type,
		// once the discriminator that selects that type has been read.
		return nil
	}

	var out []problem
	switch t.Kind() {
	case reflect.Struct:
		if n.Kind != yaml.MappingNode {
			// A wrong shape is the decoder's error to report, with its own position.
			return nil
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, val := n.Content[i], n.Content[i+1]
			if key.Tag == "!!merge" {
				// `<<:` pulls keys from an anchor; the anchored mapping is
				// validated where it is written. `<<` itself is never unknown.
				continue
			}
			f, ok := fieldByTag(t, key.Value)
			if !ok {
				out = append(out, problem{
					Line: key.Line, Column: key.Column,
					Path: joinPath(path, key.Value), Field: key.Value,
				})
				continue
			}
			out = append(out, checkUnknownKeys(val, f.Type, joinPath(path, key.Value))...)
		}
	case reflect.Slice, reflect.Array:
		if n.Kind != yaml.SequenceNode {
			return nil
		}
		for i, el := range n.Content {
			out = append(out, checkUnknownKeys(el, t.Elem(), fmt.Sprintf("%s[%d]", path, i))...)
		}
	case reflect.Map:
		if n.Kind != yaml.MappingNode {
			return nil
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			out = append(out, checkUnknownKeys(n.Content[i+1], t.Elem(), joinPath(path, n.Content[i].Value))...)
		}
	}
	return out
}

func fieldByTag(t reflect.Type, key string) (reflect.StructField, bool) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" { // unexported
			continue
		}
		name := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		if name == key {
			return f, true
		}
	}
	return reflect.StructField{}, false
}

func joinPath(base, key string) string {
	if base == "" {
		return key
	}
	if strings.HasPrefix(key, "[") {
		return base + key
	}
	return base + "." + key
}
