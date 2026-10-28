package pipeline

import (
	"time"

	"github.com/2017fighting/mdc-lite/internal/field"
	"github.com/2017fighting/mdc-lite/internal/meta"
)

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
	// Lang is the subtitle's own language token, separators included (".zh"), when
	// its name carried one. The rename keeps it: a subtitle follows its video's new
	// base name but not its language.
	Lang string
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

// Number is one group's 番号 as `parse` decided it: Raw is the text the winning
// rule read, Normalized is its canonical form, and Family is the rule's label
// (fc2 / date-code / dmm-content-id / generic, or a user rule's own).
type Number struct {
	Raw        string
	Normalized string
	Family     string
}

// ParsedGroup is one group with its number.
//
// Parts is index-aligned with Group.Parts, and Parts[0] is the primary: each
// entry lists the distinct numbers that part's own stem produced. Zero entries
// means that file's text offered no number; more than one means it offered
// several, and the primary's list is what decides the group.
type ParsedGroup struct {
	Group  Group
	Number Number
	Parts  [][]string
	// PrimaryFailed says the number came from another part because the primary
	// produced no candidate of its own.
	PrimaryFailed bool
}

// Unresolved reasons, the machine codes that also land in `tasks.last_error` and
// in the needs-number panel. They are stable strings, not prose.
const (
	// ReasonNoCandidate means no rule read a number anywhere in the group.
	ReasonNoCandidate = "no_candidate"
	// ReasonAmbiguous means the group offered several numbers and none is the
	// less specific reading of another. Candidates carries what to show the user.
	ReasonAmbiguous = "ambiguous"
	// ReasonDuplicateStem means the files strip to the same stem with no part
	// marker on either side, so a human decides whether they are two parts or two
	// versions. No number is read at all.
	ReasonDuplicateStem = "duplicate_stem"
)

// Unresolved is a set of files waiting for a human-typed number. A group whose
// number did not resolve arrives as its files (primary first); a duplicate stem
// arrives as the files that never formed a group.
type Unresolved struct {
	Dir    string
	Stem   string
	Reason string
	Files  []FileRef
	// Candidates is what the rules offered, sorted — the chips the panel shows.
	// Empty for no_candidate and duplicate_stem: there is nothing to offer.
	Candidates []string
}

// Parsed is what `parse` produces: the groups with a number, and the files a
// human has to number. Nothing is dropped: a file with no number goes to
// Unresolved rather than being guessed at or silently skipped.
type Parsed struct {
	Items      []ParsedGroup
	Unresolved []Unresolved
}

// ImageBlob is one image whose bytes are in hand, before it is named for the
// output directory. Ext is the engine's own sniffing of the magic bytes, never the
// Content-Type the source claimed.
type ImageBlob struct {
	Role  field.Name
	Index int
	Ext   string
	Bytes []byte
	// Source is the id of the source that won the field; URL is where the bytes
	// came from, kept for the ledger and the detail page.
	Source string
	URL    string
}

// ScrapedItem is one group after scraping: its number, what the sources agreed on,
// and the images whose bytes arrived.
type ScrapedItem struct {
	Group    Group
	Number   Number
	Metadata meta.Metadata
	Images   []ImageBlob
}

// Scraped is what `scrape` produces.
type Scraped struct {
	Items []ScrapedItem
}

// ImageFile is one image bound for the output directory: its field, its index,
// the name it will be written under (relative to the output directory), and the
// bytes.
type ImageFile struct {
	Role   field.Name
	Index  int
	Name   string
	Ext    string
	Bytes  []byte
	Source string
	URL    string
	// Cropped marks the poster that `images` cut out of the fanart, so the traces
	// can say so.
	Cropped bool
}

// ImagesItem is one group's images: what will be written, and what is still
// missing after the poster crop had its chance.
type ImagesItem struct {
	Group    Group
	Number   Number
	Metadata meta.Metadata
	Files    []ImageFile
	Missing  []field.Name
	// Partial is true when a required field is missing; it is the image step's own
	// contribution to the run's partial status (the crop failing does not fail the
	// task, it only leaves the poster missing).
	Partial bool
}

// Images is what `images` produces.
type Images struct {
	Items []ImagesItem
}

// NfoItem is one group's rendered NFO. It carries the group and the metadata so
// `output` can write the file next to the videos without re-reading anything.
type NfoItem struct {
	Group    Group
	Number   Number
	Metadata meta.Metadata
	Content  string
	Warnings []string
}

// NfoDoc is what `nfo` produces: text, never a file. Writing belongs to `output`,
// which is the only step allowed to touch the disk.
type NfoDoc struct {
	Items []NfoItem
}

// WriteKind is what a planned write becomes in the ledger's `kind` column.
type WriteKind string

// The four kinds a write can have.
const (
	WriteVideo     WriteKind = "video"
	WriteImage     WriteKind = "image"
	WriteNfo       WriteKind = "nfo"
	WriteCompanion WriteKind = "companion"
)

// Ledger roles, i.e. the `file_rows.role` values.
const (
	RoleVideo       = "video"
	RolePoster      = "poster"
	RoleFanart      = "fanart"
	RoleExtrafanart = "extrafanart"
	RoleNfo         = "nfo"
	RoleCompanion   = "companion"
)

// WriteMode says how a video reaches its destination. Images, the NFO and the
// companions always go through memory (WriteBytes): they are small and the spec
// gives them move semantics regardless of `output.with.link`.
type WriteMode string

// The four ways one planned write can be applied.
const (
	ModeHardlink WriteMode = "hardlink"
	ModeCopy     WriteMode = "copy"
	ModeMove     WriteMode = "move"
	ModeBytes    WriteMode = "bytes"
)

// PlannedWrite is one file the `output` step intends to produce. `Src` is
// provenance (a source path, or an image's URL) and is what lands in the ledger's
// `src` column; `Path` is the local file to read for a video, and `Data` holds the
// bytes for everything written through memory.
type PlannedWrite struct {
	Kind  WriteKind
	Role  string
	Index int
	Mode  WriteMode
	Src   string
	Path  string
	Dst   string
	Data  []byte
}

// PlanItem is one group's set of writes. Grouping them keeps a task's plan
// selectable: a task is one group, so its run applies exactly one item.
type PlanItem struct {
	Group    Group
	Number   Number
	Metadata meta.Metadata
	// Dir is the absolute output directory this item writes into.
	Dir    string
	Writes []PlannedWrite
}

// Plan is what `output` produces instead of writing: the whole batch's intent.
type Plan struct {
	Items []PlanItem
}

// Writes flattens every planned write, in item order.
func (p *Plan) Writes() []PlannedWrite {
	if p == nil {
		return nil
	}
	var out []PlannedWrite
	for _, item := range p.Items {
		out = append(out, item.Writes...)
	}
	return out
}

// Action is what applying one write actually did.
type Action string

// The four ledger actions.
const (
	ActionCreated     Action = "created"
	ActionOverwritten Action = "overwritten"
	ActionSkipped     Action = "skipped"
	ActionDeleted     Action = "deleted"
)

// Row is one applied write, i.e. one line of the run's ledger once the caller has
// attached the step run it belongs to.
type Row struct {
	Kind   string
	Role   string
	Index  int
	Src    string
	Dst    string
	Bytes  int64
	SHA256 string
	Action Action
}

// Manifest is what applying a plan produced: one row per file, including the ones
// that were skipped because their content was already there.
type Manifest struct {
	Rows []Row
}

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
