//go:build acceptance

package acceptance

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// getenv reads one variable. Every *variable* the environment reads comes through
// here, which keeps the outside surface greppable; the child processes are handed
// os.Environ() as it is, because that is how a process is spawned.
func getenv(key string) string { return os.Getenv(key) }

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// currentUID and currentGID are the user that owns the scratch root. The container
// runs as this identity so the files it writes can be deleted by the next reset.
func currentUID() string { return strconv.Itoa(os.Getuid()) }
func currentGID() string { return strconv.Itoa(os.Getgid()) }

// httpGet is a plain GET with a short timeout: it is used inside waits, where a hung
// request must not become a hung environment.
func httpGet(url string) ([]byte, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return body, fmt.Errorf("HTTP %d: %s", resp.StatusCode, summarize(body))
	}
	return body, nil
}

// waitFor is the environment's only wait.
func (e *env) waitFor(t *testing.T, timeout time.Duration, what string, probe func() (bool, string)) {
	t.Helper()
	pollUntil(t, timeout, what, probe)
}

// pollUntil waits until the probe says the expected thing happened, and fails with the
// last observation when it does not.
//
// It waits for the expectation and for nothing else: no fixed sleeps (a sleep is a bet
// on someone else's timing that hides the day it becomes wrong), and no retrying the
// whole run (a flaky acceptance test is a bug in the acceptance test). The poll
// interval is short because the thing being waited for is a file the product just
// wrote or an index the server just updated.
func pollUntil(t *testing.T, timeout time.Duration, what string, probe func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var detail string
	for {
		done, observed := probe()
		if done {
			return
		}
		detail = observed
		if time.Now().After(deadline) {
			t.Fatalf("acceptance: waited %s for %s; last observation: %s", timeout, what, detail)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// waitUntilUp waits for the media server to answer before the wizard has run, which is
// the only endpoint that does.
func (j *jellyfin) waitUntilUp(t *testing.T) {
	t.Helper()
	var info map[string]any
	pollUntil(t, 3*time.Minute, "the media server to answer /System/Info/Public", func() (bool, string) {
		info = j.publicInfo(t)
		if info == nil {
			return false, j.base + " is not answering yet"
		}
		return true, ""
	})
	t.Logf("jellyfin: %v %v up at %s", info["ProductName"], info["Version"], j.base)
}

// writeConfig makes the scratch root's copy of the acceptance configuration.
//
// The pipeline file is copied instead of read in place so a run cannot write into the
// checkout, and the copy is what the server is told to load.
func (e *env) writeConfig(t *testing.T) {
	t.Helper()
	pipelines := filepath.Join(e.Scratch, "config", "pipelines")
	for _, dir := range []string{pipelines, filepath.Join(e.Scratch, "config", "templates")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("acceptance: create %s: %v", dir, err)
		}
	}
	src := filepath.Join(e.Repo, "acceptance", "config", "pipelines")
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("acceptance: read %s: %v", src, err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		body, err := os.ReadFile(filepath.Join(src, entry.Name()))
		if err != nil {
			t.Fatalf("acceptance: read %s: %v", entry.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(pipelines, entry.Name()), body, 0o644); err != nil {
			t.Fatalf("acceptance: write %s: %v", entry.Name(), err)
		}
	}
	if len(entries) == 0 {
		t.Fatal("acceptance: no pipeline files to load; the corpus has nothing to run through")
	}
}

// marker is the positive record that the acceptance assertions actually ran.
//
// Every gate writes one, because "the command exited 0" is also what a gate that
// skipped everything says. It records what was asserted and against which server
// image, and CI reads it back.
type marker struct {
	Assertions []string `json:"assertions"`
	Jellyfin   string   `json:"jellyfin_image"`
	RanAt      string   `json:"ran_at"`
}

// markRan writes the marker and prints it. The list is the record of which assertions
// ran, deliberately not a count of anything in the library: the assertions look for the
// values they expect, they never count what the media server holds.
func (e *env) markRan(t *testing.T, assertions ...string) {
	t.Helper()
	m := marker{Assertions: assertions, Jellyfin: e.image, RanAt: time.Now().UTC().Format(time.RFC3339)}
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatalf("acceptance: encode the marker: %v", err)
	}
	path := filepath.Join(e.Scratch, "ran.json")
	if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
		t.Fatalf("acceptance: write the marker %s: %v", path, err)
	}
	t.Logf("ACCEPTANCE RAN: %s @ %s", joinOrNone(assertions), m.RanAt)
}

// joinOrNone keeps the marker line readable when nothing ran, which is itself the
// thing worth noticing.
func joinOrNone(list []string) string {
	if len(list) == 0 {
		return "(nothing ran — this is a failure, not a green run)"
	}
	return strings.Join(list, "; ")
}
