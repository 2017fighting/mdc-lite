package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// This file is the rerun's other half: the previous run's ledger is compared with
// this run's products, and what the previous run wrote but this one did not is
// removed (five stills last time, two this time → fanart3, fanart4 and fanart5 go).
//
// Two bounds make the deletion safe. Only rows the ledger recorded are candidates,
// so a file the operator put in the directory by hand is never touched; and only
// paths inside this task's output directory are eligible, so a template change or a
// hand-edited ledger row cannot reach outside it.

// PriorRow is one line of a previous run's ledger, as cleanup reads it.
type PriorRow struct {
	Kind   string
	Role   string
	Index  int
	Dst    string
	SHA256 string
	Action string
}

// StaleRows computes the deletions: previous rows whose destination this run no
// longer produces, limited to the task's output directory. The returned rows carry
// Action=deleted so they can be written to the new run's ledger.
func StaleRows(prior []PriorRow, current []Row, outDir string) []Row {
	kept := make(map[string]bool, len(current))
	for _, row := range current {
		kept[row.Dst] = true
	}
	seen := map[string]bool{}
	var out []Row
	for _, p := range prior {
		if p.Action == string(ActionDeleted) || p.Dst == "" || kept[p.Dst] || seen[p.Dst] {
			continue
		}
		if !withinDir(outDir, p.Dst) {
			continue
		}
		seen[p.Dst] = true
		out = append(out, Row{
			Kind:   p.Kind,
			Role:   p.Role,
			Index:  p.Index,
			Dst:    p.Dst,
			Action: ActionDeleted,
		})
	}
	return out
}

// withinDir reports whether path is inside dir. A sibling with a shared prefix
// (`/media/lib2` next to `/media/lib`) is not: the comparison is on path elements,
// not on strings.
func withinDir(dir, path string) bool {
	if dir == "" {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !filepath.IsAbs(rel) &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// RemoveStale deletes the files the cleanup decided on. A file that is already gone
// is not an error: the ledger says it was written once, and what matters is that it
// is not there now.
func RemoveStale(ctx context.Context, rows []Row) error {
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.Remove(row.Dst); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale %s: %w", row.Dst, err)
		}
	}
	return nil
}
