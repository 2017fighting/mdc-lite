//go:build acceptance

package acceptance

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// The acceptance environment mounts one filesystem of its own.
//
// ③ is "a hardlink across two filesystems fails, and the task fails naming the fix".
// The only honest way to produce that error is to have two filesystems: a simulated
// branch inside the product would prove that the branch is reachable, which nobody
// doubts, and would say nothing about whether a real `link(2)` returns EXDEV where the
// product expects it, or whether the message survives the trip to the task row.
//
// So the run mounts a tmpfs under its own scratch root. That makes the second mount
// point part of the environment (visible in `acceptance/scratch/exdev-fs`, gone when
// the run ends) and it makes the device ids differ for a reason the kernel decided.
// Mounting needs root or passwordless sudo, and a run that cannot mount fails with the
// reason rather than skipping the arm: a skipped assertion is a green gate that
// asserted nothing.
const secondFS = "exdev-fs"

// mountSecond mounts the environment's second filesystem and returns its path.
func (e *env) mountSecond(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(e.Scratch, secondFS)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("acceptance: create the second mount point %s: %v", dir, err)
	}
	if err := mountTmpfs(dir); err != nil {
		t.Fatalf("acceptance: mount a tmpfs at %s: %v\n"+
			"the cross-device assertion (③) needs a second real filesystem; run this gate as root, "+
			"or give this user passwordless sudo for mount/umount", dir, err)
	}
	e.secondFS = dir
	// The mount is released before the container is torn down, and always: a scratch
	// root the next run cannot delete is worse than a failed assertion.
	t.Cleanup(func() { e.umountSecond(t) })

	// The point of the mount, asserted rather than assumed: if the two paths were on
	// one device after all, the EXDEV arm below would be asserting nothing.
	scratchDev, err := deviceOf(e.Scratch)
	if err != nil {
		t.Fatalf("acceptance: read the scratch root's device: %v", err)
	}
	secondDev, err := deviceOf(dir)
	if err != nil {
		t.Fatalf("acceptance: read %s's device: %v", dir, err)
	}
	if scratchDev == secondDev {
		t.Fatalf("acceptance: %s and %s are both on device %d, so no hardlink between them can be EXDEV",
			e.Scratch, dir, scratchDev)
	}
	t.Logf("acceptance: second filesystem at %s (device %d, scratch root is %d)", dir, secondDev, scratchDev)
	return dir
}

// umountSecond releases the mount. Best effort on purpose: this runs on a path out of
// a failed run as well as a clean one, and an unmount that was already done is not a
// problem.
func (e *env) umountSecond(t *testing.T) {
	t.Helper()
	if e.secondFS == "" {
		return
	}
	if err := umount(e.secondFS); err != nil {
		t.Logf("acceptance: release %s: %v", e.secondFS, err)
	}
	e.secondFS = ""
}

// unmountLeftover clears a mount a killed run left behind, before the scratch root is
// reset: `os.RemoveAll` on a mount point fails with EBUSY, and the next run would then
// start on top of the previous one's state.
func (e *env) unmountLeftover() {
	dir := filepath.Join(e.Scratch, secondFS)
	if _, err := os.Stat(dir); err != nil {
		return
	}
	_ = umount(dir)
}

// mountTmpfs mounts a tmpfs at dir. The first attempt is plain `mount` (the gate may
// run as root, as it does in the repository's own WSL2 setup); the second asks sudo,
// which is how the GitHub runner does it.
func mountTmpfs(dir string) error {
	if out, err := exec.Command("mount", "-t", "tmpfs", "tmpfs", dir).CombinedOutput(); err == nil {
		return nil
	} else if !canSudo() {
		return &mountError{out: out, err: err}
	}
	if out, err := exec.Command("sudo", "-n", "mount", "-t", "tmpfs", "tmpfs", dir).CombinedOutput(); err != nil {
		return &mountError{out: out, err: err}
	}
	return nil
}

func umount(dir string) error {
	if out, err := exec.Command("umount", dir).CombinedOutput(); err == nil {
		return nil
	} else if !canSudo() {
		return &mountError{out: out, err: err}
	}
	if out, err := exec.Command("sudo", "-n", "umount", dir).CombinedOutput(); err != nil {
		return &mountError{out: out, err: err}
	}
	return nil
}

// canSudo reports whether this process may ask sudo without a password. `sudo -n true`
// is the question itself; guessing from the uid would be a second, wrong answer (the
// GitHub runner is not root and yet can mount).
func canSudo() bool {
	if os.Geteuid() == 0 {
		return false // plain mount already had its chance and failed
	}
	return exec.Command("sudo", "-n", "true").Run() == nil
}

// mountError carries the command's own words: "mount failed" with no output is a
// failure nobody can act on.
type mountError struct {
	out []byte
	err error
}

func (m *mountError) Error() string {
	text := strings.TrimSpace(string(m.out))
	if text == "" {
		return m.err.Error()
	}
	return text + ": " + m.err.Error()
}

// deviceOf is a path's filesystem id. Two paths with different ids are on different
// filesystems, which is the fact a cross-device hardlink is made of.
func deviceOf(path string) (uint64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	sys, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, &mountError{err: os.ErrInvalid}
	}
	return uint64(sys.Dev), nil
}
