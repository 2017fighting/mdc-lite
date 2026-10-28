package pipeline

import "time"

// This file is the artifact side of the step contract: what one step hands to
// the next. The names and the fields are the spec's §3 contract, so they are
// spelled the way the spec and the glossary spell them.

// FileKind says what a file is in this pipeline's terms.
//
// scan cannot know: only the extension whitelist in
// `group.with.companion_extensions` separates a video from a companion, and that
// table belongs to `group`. So scan hands out KindUnknown and `group` sets the
// kind on the copies it puts inside a group.
type FileKind string

// The three file kinds a candidate can have.
const (
	KindUnknown   FileKind = ""
	KindVideo     FileKind = "video"
	KindCompanion FileKind = "companion"
)

// FileRef is one file on disk. A video passes through the pipeline as a path
// plus a stat, never as bytes: the only bytes that ride in memory are images
// and the rendered NFO.
type FileRef struct {
	Path string
	// Size is the bytes on disk; SizeKnown is false only when the size could not
	// be read, which is why the size is not a plain int64.
	Size      int64
	SizeKnown bool
	// Ext is lowercase and includes the dot; empty when the name has none.
	Ext     string
	ModTime time.Time
	Kind    FileKind
}

// Skip reasons. They are the machine codes that land in a step's summary, so
// they are stable strings, not prose.
const (
	// SkipHidden is a dot-file or a dot-directory: the walk never enters one.
	SkipHidden = "hidden"
	// SkipExcluded is a directory (with its whole subtree) listed in `exclude`.
	SkipExcluded = "excluded"
	// SkipPartial is a download in progress: `x.part` or `x.!qB`.
	SkipPartial = "partial"
	// SkipSymlinkDir is a symlink to a directory. The walk never follows one, so a
	// cycle cannot hang it. A symlink to a regular file is taken as that file.
	SkipSymlinkDir = "symlink_dir"
	// SkipUnreadable is an entry the walk could not stat or read.
	SkipUnreadable = "unreadable"
)

// SkippedFile is a path the scan saw and did not take, with the reason why.
type SkippedFile struct {
	Path   string
	Reason string
}

// FileSet is what `scan` produces: the files the batch starts from.
type FileSet struct {
	Roots   []string
	Files   []FileRef
	Skipped []SkippedFile
}

// Candidate is a file that made it through the filter, tagged with the rule
// that let it in.
type Candidate struct {
	File FileRef
	Rule string
}

// RejectedFile is a file some rule kept out. Rule is that rule's name, or
// SkipNoMatch when no rule matched at all.
type RejectedFile struct {
	File FileRef
	Rule string
}

// SkipNoMatch is the reason a file is rejected when no rule matched it. It is a
// reason, not a rule name, which is why it is spelled with an underscore like
// the other reason codes.
const SkipNoMatch = "no_match"

// Candidates is what `filter` produces: the files that enter processing, and
// the ones that do not with the rule that said so.
type Candidates struct {
	Files    []Candidate
	Rejected []RejectedFile
}

// Marker is the part evidence found at the end of a file's stripped name.
//
// The zero value means "no marker": the name claims to be nothing but itself.
// Index is 1-based and comes from the text as written (`cd10` is 10, not 2).
type Marker struct {
	// Text is the marker as written, minus its separator: "cd1", "A", "1".
	Text string
	// Index is the 1-based part number the marker names. 0 when it names none.
	Index int
	// Confident is the tier: true for the unconditional table (`cd1`, `-A`),
	// false for a conditional one (a trailing `-1`, a glued `A`) that only counts
	// because a sibling in the same directory agreed with it.
	Confident bool
}

// Part is one video inside a group.
type Part struct {
	File FileRef
	// Stem is the name with the noise and the effective marker stripped off. It
	// is computed here and nowhere else: `parse` reads it, it does not re-derive
	// it.
	Stem   string
	Marker Marker
}

// Index is the part number used for ordering. An unmarked file is part 1.
func (p Part) Index() int {
	if p.Marker.Index > 0 {
		return p.Marker.Index
	}
	return 1
}

// Companion is a subtitle or an image that belongs to a group without being
// part evidence.
type Companion struct {
	File   FileRef
	Stem   string
	Marker Marker
	// FollowsPart is the part index this companion follows when it gets renamed.
	// 0 means the primary.
	FollowsPart int
}

// Group is one work: the primary file plus its other parts and companions.
// Everything in a group shares one directory — crossing directories never
// merges, so the same number in two directories is two groups.
type Group struct {
	Dir string
	// Primary is the part with the smallest part index; it decides the number and
	// the output directory name.
	Primary FileRef
	// PrimaryStem is the primary's stem, i.e. the group's name once stripped.
	PrimaryStem string
	// DistanceMerged is true when the group exists only because both sides carry
	// a marker and their stems are within max_distance (`SSVR-122-cd1` +
	// `SSVR-123-cd2`). It is false when the stems are literally equal.
	DistanceMerged bool
	Parts          []Part
	Companions     []Companion
}

// DuplicateStem is a set of files that strip to the same stem while none of
// them carries an effective part marker (`ABC-123.mp4` + `ABC-123-U.mp4`). They
// are not merged into a group: a copy is not a part, so a human decides.
type DuplicateStem struct {
	Dir   string
	Stem  string
	Files []FileRef
}

// Warning is a pair that looks like two parts of one work but did not merge
// because their stems are further apart than max_distance. It is advisory: the
// summary reports it, nothing acts on it.
type Warning struct {
	Dir string
	A   FileRef
	B   FileRef
}

// UnmatchedFile is a companion that found no group to belong to.
type UnmatchedFile struct {
	File   FileRef
	Reason string
}

// SkipNoGroup is the reason a companion is left out: no group in its directory
// has a stem it matches.
const SkipNoGroup = "no_group"

// Groups is what `group` produces: the works to process, the duplicate stems a
// human has to settle, the companions that belong nowhere, and the advisory
// warnings.
type Groups struct {
	Groups     []Group
	Duplicates []DuplicateStem
	Unmatched  []UnmatchedFile
	Warnings   []Warning
}

// VideoCount is the number of videos across every group.
func (g *Groups) VideoCount() int {
	n := 0
	for _, grp := range g.Groups {
		n += len(grp.Parts)
	}
	return n
}

// CompanionCount is the number of companions that found a group.
func (g *Groups) CompanionCount() int {
	n := 0
	for _, grp := range g.Groups {
		n += len(grp.Companions)
	}
	return n
}
