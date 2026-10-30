// Package fixtures is the one definition of what a capture directory is.
//
// A capture is an answer a real site gave, kept byte for byte, with a manifest row
// saying which address answered, with what status, how many bytes, when it was
// fetched and its sha256. Tests read captures; nothing writes them at test time.
// The point is falsifiability: a page written to match a parser's selectors agrees
// with that parser by construction and proves nothing, so the discipline is that a
// capture is never edited, and that the edit is visible when it happens.
//
// The package is separate from the sources because two consumers need the same
// rules: each source's own tests, and the acceptance environment, which replays the
// same files at a local address. Two copies of "what a fixture is" would be two
// chances to disagree.
package fixtures

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/2017fighting/mdc-lite/internal/source"
)

// ManifestName is the file each capture directory carries its rows in.
const ManifestName = "fixtures.json"

// The closed set of reasons a constructed fixture may exist for. Everything else is
// a real capture or it is not a fixture:
//
//   - defect-reproduction: a page kept because it reproduces a bug the parser has to
//     answer for, and the site will not serve that shape again;
//   - parser-shape: a page that exercises a shape the site serves rarely (or only to
//     some visitors), kept so the parser has a specimen.
const (
	ReasonDefectReproduction = "defect-reproduction"
	ReasonParserShape        = "parser-shape"
)

// ReasonIsKnown reports whether a constructed fixture's reason is one of the two.
func ReasonIsKnown(reason string) bool {
	return reason == ReasonDefectReproduction || reason == ReasonParserShape
}

// Load reads a capture directory's manifest, sorted by path so two runs of every
// consumer see the same order.
func Load(dir string) ([]source.CaptureInfo, error) {
	data, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if err != nil {
		return nil, fmt.Errorf("read the capture manifest: %w", err)
	}
	var rows []source.CaptureInfo
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("%s is not a manifest: %w", filepath.Join(dir, ManifestName), err)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Path < rows[j].Path })
	return rows, nil
}

// Problems verifies one capture directory and returns what is wrong with it, as
// lines a failure message can print as they are. An empty slice means the directory
// keeps the discipline: every file is recorded, every row is a file, the bytes are
// the bytes the row claims, and anything constructed says so and says why.
func Problems(dir string) []string {
	rows, err := Load(dir)
	if err != nil {
		return []string{err.Error()}
	}

	var problems []string
	seen := map[string]bool{}
	for _, row := range rows {
		if row.Path == "" {
			problems = append(problems, fmt.Sprintf("%s: a row has no path", dir))
			continue
		}
		if seen[row.Path] {
			problems = append(problems, fmt.Sprintf("%s: %s is recorded twice", dir, row.Path))
			continue
		}
		seen[row.Path] = true
		problems = append(problems, rowProblems(dir, row)...)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return append(problems, fmt.Sprintf("%s: %v", dir, err))
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == ManifestName {
			continue
		}
		if !seen[entry.Name()] {
			problems = append(problems,
				fmt.Sprintf("%s: %s has no manifest row, so nothing says where it came from", dir, entry.Name()))
		}
	}
	return problems
}

// rowProblems checks one manifest row against the file it names.
func rowProblems(dir string, row source.CaptureInfo) []string {
	var problems []string
	name := row.Path
	if row.Reason == "" {
		// A capture: the row is the evidence of where it came from, so every one of
		// these has to be there.
		if row.URL == "" {
			problems = append(problems, fmt.Sprintf("%s: %s has no url, so nothing says which page it is", dir, name))
		}
		if row.Status == 0 {
			problems = append(problems, fmt.Sprintf("%s: %s has no status, so nothing says what the site answered", dir, name))
		}
		if row.Captured == "" {
			problems = append(problems, fmt.Sprintf("%s: %s does not say when it was captured", dir, name))
		}
	} else {
		if !ReasonIsKnown(row.Reason) {
			problems = append(problems, fmt.Sprintf(
				"%s: %s is constructed for reason %q, which is not one of %s / %s",
				dir, name, row.Reason, ReasonDefectReproduction, ReasonParserShape))
		}
		if strings.TrimSpace(row.Note) == "" {
			problems = append(problems, fmt.Sprintf("%s: %s is constructed but does not say why", dir, name))
		}
	}

	body, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		problems = append(problems, fmt.Sprintf("%s: %s is recorded but not on disk: %v", dir, name, err))
		return problems
	}
	if len(body) != row.Bytes {
		problems = append(problems, fmt.Sprintf("%s: %s is %d bytes on disk, the manifest says %d",
			dir, name, len(body), row.Bytes))
	}
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != row.SHA256 {
		problems = append(problems, fmt.Sprintf("%s: %s has sha256 %s, the manifest says %s — a capture is never edited",
			dir, name, got, row.SHA256))
	}
	return problems
}
