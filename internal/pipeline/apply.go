package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// This file is the only writer in the program. `output` decides what should exist,
// `Apply` makes it exist, and every consequence of a write (its size, its hash,
// whether it was created, overwritten or skipped) comes back as a ledger row.
//
// The three properties of a write are the spec's: overwrite, but skip when the
// content is already there (so a rerun does not refresh mtime and make the media
// server rescan everything); write through a same-directory `.tmp` and rename (so a
// half-written file never appears in the library); and never fall back from
// hardlink to copy on EXDEV (a silent fallback doubles the disk usage quietly).

// Apply executes a plan. Files are checked for cancellation between writes, never in
// the middle of one: a run either leaves a whole file behind or none.
func Apply(ctx context.Context, plan *Plan) (*Manifest, error) {
	m := &Manifest{}
	if plan == nil {
		return m, nil
	}
	for _, item := range plan.Items {
		for _, w := range item.Writes {
			if err := ctx.Err(); err != nil {
				return m, err
			}
			row, err := applyWrite(w)
			if err != nil {
				return m, err
			}
			m.Rows = append(m.Rows, row)
		}
	}
	return m, nil
}

// applyWrite applies one planned write.
func applyWrite(w PlannedWrite) (Row, error) {
	row := Row{Kind: string(w.Kind), Role: w.Role, Index: w.Index, Src: w.Src, Dst: w.Dst}
	if err := os.MkdirAll(filepath.Dir(w.Dst), 0o755); err != nil {
		return row, fmt.Errorf("create %s: %w", filepath.Dir(w.Dst), err)
	}
	switch w.Mode {
	case ModeBytes, "":
		return applyBytes(row, w)
	case ModeHardlink:
		return applyLink(row, w)
	case ModeCopy:
		return applyCopy(row, w)
	case ModeMove:
		return applyMove(row, w)
	default:
		return row, fmt.Errorf("%s: unknown write mode %q", w.Dst, w.Mode)
	}
}

// applyBytes writes in-memory content: images, the NFO and the companions. Images
// and companions use this path regardless of `output.with.link` — the spec gives
// them move semantics, and their bytes are already in hand.
func applyBytes(row Row, w PlannedWrite) (Row, error) {
	sum := sha256.Sum256(w.Data)
	row.SHA256 = hex.EncodeToString(sum[:])
	row.Bytes = int64(len(w.Data))

	exists, same, err := sameContentBytes(w.Dst, sum)
	if err != nil {
		return row, err
	}
	if same {
		row.Action = ActionSkipped
		return row, nil
	}
	if err := writeAtomic(w.Dst, w.Data); err != nil {
		return row, err
	}
	row.Action = actionForExists(exists)
	return row, nil
}

// applyLink hardlinks a video. When the destination is already the same inode the
// write is skipped; that is the cheap and stronger form of "the content is already
// there" for a multi-gigabyte file.
//
// The link is made under a temporary name in the destination directory and renamed
// into place, so a link the filesystem refuses (EXDEV) never removes the output of
// the previous run: the old file is still there, and the task fails with the fix
// spelled out.
func applyLink(row Row, w PlannedWrite) (Row, error) {
	info, err := os.Stat(w.Path)
	if err != nil {
		return row, fmt.Errorf("%s: %w", w.Path, err)
	}
	row.Bytes = info.Size()
	if same, err := sameFile(w.Path, w.Dst); err != nil {
		return row, err
	} else if same {
		// The ledger records the content hash even when nothing was written: the
		// hash of a hardlink is the hash of its source, which we are about to take.
		if row.SHA256, err = fileSHA256(w.Path); err != nil {
			return row, err
		}
		row.Action = ActionSkipped
		return row, nil
	}
	if row.SHA256, err = fileSHA256(w.Path); err != nil {
		return row, err
	}
	exists, err := fileExists(w.Dst)
	if err != nil {
		return row, err
	}
	tmp := w.Dst + ".tmp"
	_ = os.Remove(tmp)
	if err := os.Link(w.Path, tmp); err != nil {
		if errors.Is(err, syscall.EXDEV) {
			return row, exdevError(w.Path, w.Dst)
		}
		return row, fmt.Errorf("link %s → %s: %w", w.Path, w.Dst, err)
	}
	if err := os.Rename(tmp, w.Dst); err != nil {
		_ = os.Remove(tmp)
		return row, fmt.Errorf("rename %s → %s: %w", tmp, w.Dst, err)
	}
	row.Action = actionForExists(exists)
	return row, nil
}

// applyCopy copies a video through a same-directory temp file. A destination with
// the same content is left alone, mtime included.
func applyCopy(row Row, w PlannedWrite) (Row, error) {
	info, err := os.Stat(w.Path)
	if err != nil {
		return row, fmt.Errorf("%s: %w", w.Path, err)
	}
	row.Bytes = info.Size()
	sum, err := fileSHA256(w.Path)
	if err != nil {
		return row, err
	}
	row.SHA256 = sum
	exists, same, err := sameContent(w.Dst, info.Size(), sum)
	if err != nil {
		return row, err
	}
	if same {
		row.Action = ActionSkipped
		return row, nil
	}
	// A copy cannot be EXDEV: the bytes travel through memory into a temp file in
	// the destination's own directory. Only a rename can cross devices.
	if err := copyAtomic(w.Path, w.Dst); err != nil {
		return row, err
	}
	row.Action = actionForExists(exists)
	return row, nil
}

// applyMove moves a video. The move is the write: rename is atomic on one
// filesystem, and across two it is EXDEV, which is a failure that names the fix.
func applyMove(row Row, w PlannedWrite) (Row, error) {
	info, err := os.Stat(w.Path)
	if err != nil {
		return row, fmt.Errorf("%s: %w", w.Path, err)
	}
	row.Bytes = info.Size()
	sum, err := fileSHA256(w.Path)
	if err != nil {
		return row, err
	}
	row.SHA256 = sum
	exists, same, err := sameContent(w.Dst, info.Size(), sum)
	if err != nil {
		return row, err
	}
	if same {
		// The destination already holds these bytes. Nothing to move: deleting the
		// source on a rerun would be a surprise, and the media library is already
		// correct.
		row.Action = ActionSkipped
		return row, nil
	}
	if err := os.Rename(w.Path, w.Dst); err != nil {
		if errors.Is(err, syscall.EXDEV) {
			return row, exdevError(w.Path, w.Dst)
		}
		return row, fmt.Errorf("move %s → %s: %w", w.Path, w.Dst, err)
	}
	row.Action = actionForExists(exists)
	return row, nil
}

// exdevError is the spec's message: the operator's fix is a link mode change, and
// the program must not perform it silently.
func exdevError(src, dst string) error {
	return fmt.Errorf("%s and %s are not on the same filesystem: change output.with.link to copy or move (EXDEV)", src, dst)
}

func actionForExists(exists bool) Action {
	if exists {
		return ActionOverwritten
	}
	return ActionCreated
}

// writeAtomic writes data to `<dst>.tmp` in the same directory and renames it into
// place, so the destination is either the old file or the complete new one.
func writeAtomic(dst string, data []byte) (err error) {
	tmp := dst + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err = os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("rename %s → %s: %w", tmp, dst, err)
	}
	return nil
}

// copyAtomic streams src into `<dst>.tmp` and renames it into place.
func copyAtomic(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()

	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy %s → %s: %w", src, tmp, err)
	}
	if err = out.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err = os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("rename %s → %s: %w", tmp, dst, err)
	}
	return nil
}

// sameContentBytes reports whether dst exists and holds these bytes.
func sameContentBytes(dst string, want [sha256.Size]byte) (exists, same bool, err error) {
	exists, err = fileExists(dst)
	if err != nil || !exists {
		return false, false, err
	}
	got, err := fileSHA256(dst)
	if err != nil {
		return true, false, err
	}
	return true, got == hex.EncodeToString(want[:]), nil
}

// sameContent compares a destination with a known size and hash: size first, then
// the hash, so a file of another size is decided without reading either file twice.
func sameContent(dst string, size int64, sum string) (exists, same bool, err error) {
	info, err := os.Stat(dst)
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("%s: %w", dst, err)
	}
	if info.Size() != size {
		return true, false, nil
	}
	dstSum, err := fileSHA256(dst)
	if err != nil {
		return true, false, err
	}
	return true, dstSum == sum, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fileExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("%s: %w", path, err)
	}
}

// sameFile reports whether two paths are the same inode.
func sameFile(a, b string) (bool, error) {
	ai, err := os.Stat(a)
	if err != nil {
		return false, fmt.Errorf("%s: %w", a, err)
	}
	bi, err := os.Stat(b)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("%s: %w", b, err)
	}
	return os.SameFile(ai, bi), nil
}
