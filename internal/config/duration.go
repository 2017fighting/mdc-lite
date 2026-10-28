package config

import (
	"errors"
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a YAML scalar holding a Go duration ("30s", "5m"). A bare integer
// is read as seconds so `timeout: 30` does what it looks like.
type Duration time.Duration

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return errors.New("a duration must be a scalar")
	}
	if v, err := time.ParseDuration(n.Value); err == nil {
		*d = Duration(v)
		return nil
	}
	var secs int
	if _, err := fmt.Sscanf(n.Value, "%d", &secs); err == nil && fmt.Sprint(secs) == n.Value {
		*d = Duration(time.Duration(secs) * time.Second)
		return nil
	}
	return fmt.Errorf("%q is not a duration (use \"30s\", \"5m\" or a whole number of seconds)", n.Value)
}

// MarshalYAML implements yaml.Marshaler.
func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// Std returns the duration as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }
