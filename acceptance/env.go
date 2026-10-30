//go:build acceptance

package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The acceptance environment's fixed names. They are constants rather than settings
// because two of them are load-bearing: the library path is handed to Jellyfin
// verbatim and must be the same string inside and outside the container, and the
// scratch root must not start with a dot (a dot-leading path is exactly the shape the
// segment sanitiser and the scan's filter rules treat specially, and an acceptance
// environment that leans on a special case proves nothing about ordinary paths).
const (
	user        = "acceptance"
	password    = "acceptance-password"
	libraryName = "Movies"

	// defaultJellyfinImage is the pinned server. It is the version the NFO layout
	// claims were measured on; `MDC_ACCEPT_JELLYFIN_IMAGE` overrides it, because a
	// reason to run a different version is a good reason to keep the option.
	defaultJellyfinImage = "jellyfin/jellyfin:10.10.7"
)

// env is the acceptance environment: where everything lives, and the handles to the
// three moving parts (the server binary, the media server, the replay).
type env struct {
	Repo     string
	Scratch  string
	Library  string
	Binary   string
	Server   string // base URL of the product
	Replay   *replayProxy
	JF       *jellyfin
	clipPath string
	compose  string
	image    string
}

// newEnvironment brings the environment up from nothing: a clean scratch root, the
// real binary built from this checkout, the replay proxy, the generated corpus, a
// pinned Jellyfin with one library over the output root, and the product server
// running against both.
//
// It is one function because the order matters and is the hard part: the library has
// to hold content before the first scan (an empty library registered and scanned is
// the case where a path refresh is silently dropped), the replay has to exist before
// the config is written (its address is in the config), and the corpus has to exist
// before the scan that is supposed to find it.
func newEnvironment(t *testing.T) *env {
	t.Helper()
	e := &env{}
	e.Repo = repoRoot(t)
	e.Scratch = scratchRoot(t, e.Repo)
	e.Library = filepath.Join(e.Scratch, "library")
	e.Binary = filepath.Join(e.Scratch, "mdc")
	e.Server = "http://127.0.0.1:" + orDefault(getenv("MDC_ACCEPT_LISTEN_PORT"), "19208")
	e.compose = filepath.Join(e.Repo, "acceptance", "compose.yaml")
	e.image = orDefault(getenv("MDC_ACCEPT_JELLYFIN_IMAGE"), defaultJellyfinImage)

	// Teardown is registered before anything is started: a failure halfway through
	// must not leave a container running against a scratch root the next run will
	// delete underneath it.
	t.Cleanup(func() { e.stop(t) })

	t.Logf("acceptance: scratch root %s", e.Scratch)
	e.composeDownQuiet()
	resetScratch(t, e.Scratch)

	// The replay first: its address goes into the config, and its CA into the child's
	// SSL_CERT_FILE.
	e.Replay = newReplay(t, filepath.Join(e.Scratch, "replay-ca.pem"),
		filepath.Join(e.Repo, "internal", "source", "javbus", "testdata", "captures"),
		filepath.Join(e.Repo, "internal", "source", "javdb", "testdata", "captures"),
	)

	e.buildBinary(t)
	e.composeUp(t)

	// The primer, before the library is registered and scanned: the library has to
	// hold something the first time the server looks at it.
	e.clipPath = e.generateClip(t)
	primer := filepath.Join(e.Library, "PRIMER-000")
	writeClip(t, filepath.Join(primer, "PRIMER-000.mp4"), e.clipPath)

	e.JF = newJellyfin()
	e.JF.waitUntilUp(t)
	e.JF.setup(t)
	e.JF.ensureLibrary(t, e.Library)
	e.JF.scan(t, e.Library)

	e.writeConfig(t)
	e.buildCorpus(t)
	e.startServer(t)
	return e
}

// stop tears the environment down, keeping the server's and the media server's logs
// when the run failed: a red acceptance with no logs is a red acceptance nobody can
// act on.
func (e *env) stop(t *testing.T) {
	t.Helper()
	if t.Failed() {
		e.dumpLogs(t)
	}
	composeDown(t, e)
}

// repoRoot is the checkout this test belongs to: the acceptance package is
// `<repo>/acceptance`, so its parent holds go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("acceptance: %v", err)
	}
	root := filepath.Dir(wd)
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("acceptance: %s is not the checkout root: %v", root, err)
	}
	return root
}

// scratchRoot is where the run keeps everything: the corpus, the library the server
// reads, the database, the logs. It is overridable so CI can put it on a roomier
// volume, and it refuses a dot-leading name instead of silently accepting one —
// requirement, not preference.
func scratchRoot(t *testing.T, repo string) string {
	t.Helper()
	root := getenv("MDC_ACCEPT_ROOT")
	if root == "" {
		root = filepath.Join(repo, "acceptance", "scratch")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("acceptance: %v", err)
	}
	if strings.HasPrefix(filepath.Base(abs), ".") {
		t.Fatalf("acceptance: the scratch root %s starts with a dot; the run must not depend on a dotted path", abs)
	}
	return abs
}

func resetScratch(t *testing.T, scratch string) {
	t.Helper()
	if err := os.RemoveAll(scratch); err != nil {
		t.Fatalf("acceptance: clear %s: %v", scratch, err)
	}
	for _, dir := range []string{scratch, filepath.Join(scratch, "library")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("acceptance: create %s: %v", dir, err)
		}
	}
}

// jellyfinDB is the server's library database inside the scratch root. It exists as a
// method so the one place that knows the container's layout is this file.
func (e *env) jellyfinDB() string {
	return filepath.Join(e.Scratch, "jellyfin", "config", "data", "library.db")
}

// buildBinary builds the artifact under test from this checkout. The gate builds what
// it tests on purpose: the one failure mode a self-check cannot afford is a green run
// against yesterday's binary.
func (e *env) buildBinary(t *testing.T) {
	t.Helper()
	cmd := exec.Command("go", "build", "-trimpath", "-o", e.Binary, "./cmd/mdc")
	cmd.Dir = e.Repo
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("acceptance: build the binary: %v\n%s", err, out)
	}
}

// generateClip makes the corpus's video bytes: a one-second clip per file, made by
// the ffmpeg inside the pinned Jellyfin image.
//
// Generated, not zero-byte: a zero-byte file makes Jellyfin fail the probe, turn off
// image extraction for the item and report nothing, and every assertion afterwards
// would be measuring a broken file rather than the pipeline. Generated by the media
// server's own ffmpeg rather than committed as a blob so the bytes are reproducible
// and the host needs no ffmpeg of its own.
func (e *env) generateClip(t *testing.T) string {
	t.Helper()
	out := filepath.Join(e.Scratch, "clip.mp4")
	cmd := exec.Command("docker", "run", "--rm",
		"--user", fmt.Sprintf("%s:%s", orDefault(getenv("MDC_ACCEPT_UID"), "0"), orDefault(getenv("MDC_ACCEPT_GID"), "0")),
		"-v", e.Scratch+":"+e.Scratch,
		"--entrypoint", "/usr/lib/jellyfin-ffmpeg/ffmpeg",
		e.image,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=10:duration=1",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-movflags", "+faststart",
		out,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("acceptance: generate the corpus clip with the Jellyfin image's ffmpeg: %v\n%s", err, output)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("acceptance: the generated clip is not there: %v", err)
	}
	if info.Size() < 1024 {
		t.Fatalf("acceptance: the generated clip is %d bytes, which is not a video", info.Size())
	}
	return out
}

// writeClip copies the generated clip to a corpus path. Copying (not hardlinking) so
// a corpus file can be rewritten by a later case without touching the original.
func writeClip(t *testing.T, dst, clip string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("acceptance: create %s: %v", filepath.Dir(dst), err)
	}
	body, err := os.ReadFile(clip)
	if err != nil {
		t.Fatalf("acceptance: read the clip: %v", err)
	}
	if err := os.WriteFile(dst, body, 0o644); err != nil {
		t.Fatalf("acceptance: write %s: %v", dst, err)
	}
}

// buildCorpus lays out the two corpus roots from the table in corpus.go.
func (e *env) buildCorpus(t *testing.T) {
	t.Helper()
	for _, file := range corpus {
		dst := filepath.Join(e.Scratch, string(file.Root), file.Dir, file.Name)
		writeClip(t, dst, e.clipPath)
	}
}

// composeUp starts the media server and waits for its healthcheck. `--wait` is the
// readiness gate, not a sleep: it returns when the healthcheck passes or fails the
// command.
func (e *env) composeUp(t *testing.T) {
	t.Helper()
	e.composeRun(t, "up", "-d", "--wait", "jellyfin")
}

func composeDown(t *testing.T, e *env) {
	t.Helper()
	if e == nil || e.compose == "" {
		return
	}
	e.composeRun(t, "down", "--volumes", "--remove-orphans")
}

// composeDownQuiet tears down whatever a previous, killed run left behind. It is
// best-effort on purpose: "nothing was running" is the good case here, not a failure.
func (e *env) composeDownQuiet() {
	if e.compose == "" {
		return
	}
	cmd := exec.Command("docker", "compose", "-f", e.compose, "down", "--volumes", "--remove-orphans")
	cmd.Dir = e.Repo
	cmd.Env = append(os.Environ(), e.composeEnv()...)
	_ = cmd.Run()
}

func (e *env) composeRun(t *testing.T, args ...string) {
	t.Helper()
	base := append([]string{"compose", "-f", e.compose}, args...)
	cmd := exec.Command("docker", base...)
	cmd.Dir = e.Repo
	cmd.Env = append(os.Environ(), e.composeEnv()...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("acceptance: docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// composeEnv is what compose.yaml interpolates. The uid/gid are resolved here rather
// than left to the image's default, so everything the server writes under the scratch
// root belongs to the user who owns it and the next reset can delete it.
func (e *env) composeEnv() []string {
	return []string{
		"MDC_ACCEPT_ROOT=" + e.Scratch,
		"MDC_ACCEPT_UID=" + orDefault(getenv("MDC_ACCEPT_UID"), currentUID()),
		"MDC_ACCEPT_GID=" + orDefault(getenv("MDC_ACCEPT_GID"), currentGID()),
		"MDC_ACCEPT_JELLYFIN_IMAGE=" + e.image,
		"MDC_ACCEPT_JELLYFIN_PORT=" + orDefault(getenv("MDC_ACCEPT_JELLYFIN_PORT"), "8096"),
	}
}

func (e *env) dumpLogs(t *testing.T) {
	t.Helper()
	if body, err := os.ReadFile(filepath.Join(e.Scratch, "mdc.log")); err == nil {
		t.Logf("mdc server log:\n%s", tail(string(body), 60))
	} else {
		t.Logf("no mdc server log at %s: %v", filepath.Join(e.Scratch, "mdc.log"), err)
	}
	for _, name := range []string{"jellyfin.log", "replay.log"} {
		if body, err := os.ReadFile(filepath.Join(e.Scratch, name)); err == nil {
			t.Logf("%s:\n%s", name, tail(string(body), 60))
		}
	}
	cmd := exec.Command("docker", "compose", "-f", e.compose, "logs", "--no-color", "--tail", "80")
	cmd.Env = append(os.Environ(), e.composeEnv()...)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Logf("jellyfin container log:\n%s", tail(string(out), 80))
	}
}

func tail(s string, lines int) string {
	parts := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return strings.Join(parts, "\n")
}

// ---- the product server ----

// startServer runs the real binary, with the acceptance configuration, and waits for
// its health endpoint. The environment variables it needs are exactly the ones the
// config interpolates: the scratch root, and the replay's address.
func (e *env) startServer(t *testing.T) {
	t.Helper()
	logFile, err := os.Create(filepath.Join(e.Scratch, "mdc.log"))
	if err != nil {
		t.Fatalf("acceptance: create the server log: %v", err)
	}
	t.Cleanup(func() { _ = logFile.Close() })

	cmd := exec.Command(e.Binary, "serve",
		"--config", filepath.Join(e.Repo, "acceptance", "config", "config.yaml"),
		"--pipelines-dir", filepath.Join(e.Scratch, "config", "pipelines"),
	)
	cmd.Dir = e.Repo
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = append(os.Environ(),
		"MDC_ACCEPT_ROOT="+e.Scratch,
		"MDC_ACCEPT_REPLAY_PROXY="+e.Replay.URL,
		// The replay terminates TLS with a CA minted for this run; this is how the
		// child trusts it and nothing else.
		"SSL_CERT_FILE="+e.Replay.CAPath,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("acceptance: start the server: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() {
			_, _ = cmd.Process.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			_ = cmd.Process.Kill()
		}
	})

	e.waitFor(t, 30*time.Second, "the product server answers /api/health", func() (bool, string) {
		reply, err := httpGet(e.Server + "/api/health")
		if err != nil {
			return false, err.Error()
		}
		var health struct {
			Status string `json:"status"`
			DB     struct {
				OK bool `json:"ok"`
			} `json:"db"`
		}
		if err := json.Unmarshal(reply, &health); err != nil {
			return false, "health is not JSON: " + summarize(reply)
		}
		if health.Status != "ok" || !health.DB.OK {
			return false, "health: " + summarize(reply)
		}
		return true, ""
	})
}
