package config

import (
	"fmt"
	"strings"
)

// ExpandEnv substitutes `${VAR}` and `${VAR:-default}` in s. A `${VAR}` with no
// default and no value in the environment is an error: a config that silently
// loses a value is worse than one that refuses to start.
func ExpandEnv(s string, lookup func(string) (string, bool)) (string, bool, error) {
	var b strings.Builder
	changed := false
	for i := 0; i < len(s); i++ {
		if s[i] != '$' || i+1 >= len(s) || s[i+1] != '{' {
			b.WriteByte(s[i])
			continue
		}
		end := strings.IndexByte(s[i+2:], '}')
		if end < 0 {
			b.WriteByte(s[i])
			continue
		}
		body := s[i+2 : i+2+end]
		name, def, hasDef := strings.Cut(body, ":-")
		if !validEnvName(name) {
			b.WriteByte(s[i])
			continue
		}
		if v, ok := lookup(name); ok {
			b.WriteString(v)
			changed = true
		} else if hasDef {
			b.WriteString(def)
			changed = true
		} else {
			return "", false, fmt.Errorf("environment variable %q is not set (used as ${%s})", name, name)
		}
		i += 2 + end
	}
	return b.String(), changed, nil
}

func validEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
