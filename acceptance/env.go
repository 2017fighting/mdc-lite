//go:build acceptance

package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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
	// secondFS is the environment's own filesystem (see mount.go). It is where the
	// cross-device output root lives.
	secondFS string
	// server is the product process, kept so an arm can kill it on purpose and start
	// it again.
	server *serverProcess
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
	// A killed run leaves its mount behind, and a mount point cannot be deleted.
	e.unmountLeftover()
	resetScratch(t, e.Scratch)
	// The second filesystem exists before anything is written into it: the corpus
	// does not use it, but the cross-device pipeline's output root does.
	e.mountSecond(t)

	// The replay first: its address goes into the config, and its CA into the child's
	// SSL_CERT_FILE.
	e.Replay = newReplay(t, filepath.Join(e.Scratch, "replay-ca.pem"),
		filepath.Join(e.Repo, "internal", "source", "javbus", "testdata", "captures"),
		filepath.Join(e.Repo, "internal", "source", "javdb", "testdata", "captures"),
		filepath.Join(e.Repo, "internal", "source", "fc2", "testdata", "captures"),
	)

	// The interface is built, not taken from the checkout: the smoke test has to visit
	// the frontend this commit describes, and the embedded, committed page is the
	// placeholder a fresh checkout compiles with.
	e.buildWeb(t)
	e.buildBinary(t)
	e.composeUp(t)
	e.assertServerOwnership(t)

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

// assertServerOwnership checks the uid/gid alignment the container is started with:
// everything the media server writes under the scratch root has to belong to the user
// who owns it, or the next reset fails with a pile of permission errors and leaves
// the state it was supposed to delete.
func (e *env) assertServerOwnership(t *testing.T) {
	t.Helper()
	info, err := os.Stat(filepath.Join(e.Scratch, "jellyfin", "config"))
	if err != nil {
		t.Fatalf("acceptance: the media server's config directory is not there: %v", err)
	}
	sys, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return // not a unix system: nothing to assert
	}
	if int(sys.Uid) != os.Getuid() || int(sys.Gid) != os.Getgid() {
		t.Errorf("the media server wrote %s as %d:%d, but this run is %d:%d; "+
			"MDC_ACCEPT_UID/MDC_ACCEPT_GID are not aligned",
			info.Name(), sys.Uid, sys.Gid, os.Getuid(), os.Getgid())
	}
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
	// Every component, not just the leaf: `/tmp/.cache/scratch` is still a run that
	// lives under a dot-leading path, which is the thing this rule is about.
	for _, part := range strings.Split(filepath.ToSlash(abs), "/") {
		if strings.HasPrefix(part, ".") {
			t.Fatalf("acceptance: the scratch root %s has a dot-leading component %q; "+
				"the run must not depend on a dotted path", abs, part)
		}
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
		"--user", fmt.Sprintf("%s:%s", orDefault(getenv("MDC_ACCEPT_UID"), currentUID()), orDefault(getenv("MDC_ACCEPT_GID"), currentGID())),
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

// buildCorpus lays out the corpus roots from the table in corpus.go.
func (e *env) buildCorpus(t *testing.T) {
	t.Helper()
	for _, file := range corpus {
		dst := filepath.Join(e.Scratch, string(file.Root), file.Dir, file.Name)
		if file.Size > 0 {
			writeSizedFile(t, dst, file.Size)
			continue
		}
		writeClip(t, dst, e.clipPath)
	}
}

// writeSizedFile makes the one corpus file that is not a one-second clip: a sparse
// file of the requested size, because the interrupted-write arm needs a copy that
// lasts long enough to be caught. Sparse, so the run costs a few pages of disk and no
// time; the copy the product makes is a real 128 MiB of bytes, which is the point.
// Nothing reads its content — no media server scans this root.
func writeSizedFile(t *testing.T, dst string, size int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("acceptance: create %s: %v", filepath.Dir(dst), err)
	}
	f, err := os.Create(dst)
	if err != nil {
		t.Fatalf("acceptance: create %s: %v", dst, err)
	}
	if err := f.Truncate(size); err != nil {
		_ = f.Close()
		t.Fatalf("acceptance: size %s to %d: %v", dst, size, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("acceptance: close %s: %v", dst, err)
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

// serverProcess is one run of the binary under test. It is a handle rather than a
// cleanup closure because one arm kills the server on purpose (a write interrupted
// mid-file) and then has to start it again to see what the process's own crash
// recovery does.
type serverProcess struct {
	cmd  *exec.Cmd
	log  *os.File
	done chan struct{}
}

// startServer runs the real binary, with the acceptance configuration, and waits for
// its health endpoint. The environment variables it needs are exactly the ones the
// config interpolates: the scratch root, and the replay's address.
func (e *env) startServer(t *testing.T) {
	t.Helper()
	if e.server != nil {
		t.Fatal("acceptance: the server is already running")
	}
	e.server = e.spawnServer(t)
	t.Cleanup(func() {
		if e.server != nil {
			e.stopServer(t)
		}
	})
}

// spawnServer starts the process and waits for it to answer. The log is opened in
// append mode and shared across restarts, so the record of a killed run and of the
// recovery that followed sit in one file.
func (e *env) spawnServer(t *testing.T) *serverProcess {
	t.Helper()
	logFile, err := os.OpenFile(filepath.Join(e.Scratch, "mdc.log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("acceptance: open the server log: %v", err)
	}

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
		_ = logFile.Close()
		t.Fatalf("acceptance: start the server: %v", err)
	}
	p := &serverProcess{cmd: cmd, log: logFile, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(p.done)
	}()
	e.waitForHealth(t)
	return p
}

// waitForHealth is the one question "is the product ready": not a port, not a file,
// but the health endpoint the operator's own probe reads.
func (e *env) waitForHealth(t *testing.T) {
	t.Helper()
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

// stopServer asks the process to shut down the way an operator does and waits for it.
func (e *env) stopServer(t *testing.T) {
	t.Helper()
	p := e.server
	if p == nil {
		return
	}
	e.server = nil
	_ = p.cmd.Process.Signal(os.Interrupt)
	select {
	case <-p.done:
	case <-time.After(30 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
	_ = p.log.Close()
}

// killServer takes the process away without a chance to clean up: SIGKILL, which is
// what a power cut or a container stop with an expired grace period looks like.
func (e *env) killServer(t *testing.T) {
	t.Helper()
	p := e.server
	if p == nil {
		t.Fatal("acceptance: there is no server to kill")
	}
	e.server = nil
	if err := p.cmd.Process.Kill(); err != nil {
		t.Fatalf("acceptance: kill the server: %v", err)
	}
	<-p.done
	_ = p.log.Close()
}

// restartServer is the recovery: the process starts again over the same database and
// the same scratch root, which is exactly the situation the crash-recovery path was
// written for.
func (e *env) restartServer(t *testing.T) {
	t.Helper()
	e.startServer(t)
}

// buildWeb builds the frontend the binary embeds. It is a build step, not a
// precondition: the byte a checkout compiles without Node is the placeholder page, and
// the interface smoke test would then be visiting a placeholder.
func (e *env) buildWeb(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(e.Repo, "web", "package.json")); err != nil {
		t.Fatalf("acceptance: the frontend is not in this checkout, so the interface arm cannot run: %v", err)
	}
	e.npm(t, "ci", "--no-audit", "--no-fund")
	e.npm(t, "run", "build")
}

// npm runs one frontend command in web/. The output is kept on failure and else
// dropped: a successful `npm ci` writes pages nobody reads.
func (e *env) npm(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("npm", args...)
	cmd.Dir = filepath.Join(e.Repo, "web")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("acceptance: npm %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
