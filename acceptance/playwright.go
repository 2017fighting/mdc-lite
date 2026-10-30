//go:build acceptance

package acceptance

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The interface arm: a real browser, rendering the built frontend, against the same
// running product the other arms drive over HTTP.
//
// It is the one assertion that cannot be made over the API: "the interface renders"
// is a claim about what a browser shows, and a jsdom test (which the frontend has too)
// proves the components work against injected callbacks, not that the binary serves an
// interface that boots and renders the run's own data.
//
// Playwright is deliberately not in the pull-request gate: a browser download is a
// minute, and `make check` stays the fast gate. This arm runs here, in the acceptance
// environment, once.

// interfaceTimeout bounds the browser's install-and-run. The install is a download on
// a cold cache; the run itself is seconds.
const interfaceTimeout = 10 * time.Minute

// runInterfaceSmoke runs `web/e2e` against the product server. The spec reads the ids
// and names the arm found over the API, so the browser does not have to guess which
// row is which.
func (e *env) runInterfaceSmoke(t *testing.T, specEnv map[string]string) string {
	t.Helper()
	web := filepath.Join(e.Repo, "web")

	// The browser is downloaded once per machine (into ~/.cache/ms-playwright) and is
	// a no-op afterwards. Missing system libraries are Playwright's own error message,
	// which names the command that installs them; the README repeats it.
	e.playwright(t, web, "install", "chromium")

	args := []string{"playwright", "test", "--reporter=line"}
	out, err := e.execWithTimeout(t, web, append(os.Environ(), envPairs(specEnv)...), "npx", args...)
	if err != nil {
		t.Fatalf("acceptance: the interface smoke test failed: %v\n%s", err, tail(out, 80))
	}
	return out
}

// playwright runs one `playwright` subcommand in web/, and keeps the output only when
// it fails.
func (e *env) playwright(t *testing.T, web string, args ...string) {
	t.Helper()
	out, err := e.execWithTimeout(t, web, os.Environ(), "npx", append([]string{"playwright"}, args...)...)
	if err != nil {
		t.Fatalf("acceptance: npx playwright %s: %v\n%s", strings.Join(args, " "), err, tail(out, 40))
	}
}

// execWithTimeout runs one command with the interface's budget. The command is given
// the environment it was handed (never the parent's implicitly), so what the browser
// reads is what the arm wrote down.
func (e *env) execWithTimeout(t *testing.T, dir string, env []string, name string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), interfaceTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return string(out), ctx.Err()
	}
	return string(out), err
}

// envPairs turns the spec's settings into the KEY=value form a child process reads.
func envPairs(pairs map[string]string) []string {
	out := make([]string, 0, len(pairs))
	for key, value := range pairs {
		out = append(out, key+"="+value)
	}
	return out
}
