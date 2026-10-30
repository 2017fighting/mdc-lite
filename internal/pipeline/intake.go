package pipeline

import "github.com/2017fighting/mdc-lite/internal/tasks"

// This file is the seam between the engine's artifacts and the store's rows: the
// fan-out point's output as the batch a trigger is about to persist.
//
// The engine itself still touches no database (see Engine.Run) — it only says what
// it found. The caller runs the prefix, turns the result into an intake, and lets
// `tasks.CreateBatch` store it. A needs_number group keeps its companions: the
// subtitle of a group whose number a human still has to type belongs to that task,
// so the path stays held and the eventual run finds it.

// ToIntake turns one run's artifacts into the batch a trigger stores.
func ToIntake(trigger tasks.Trigger, triggerRef string, res *Result) tasks.Intake {
	in := tasks.Intake{Trigger: trigger, TriggerRef: triggerRef}
	if res == nil {
		return in
	}
	in.PipelineID = res.Pipeline
	if res.State == nil || res.State.Parsed == nil {
		return in
	}

	parsed := res.State.Parsed
	for _, work := range ParsedWorks(res.State) {
		in.Groups = append(in.Groups, tasks.Group{
			Number: work.Number.Normalized,
			Files:  intakeFiles(work.Group),
		})
	}
	for _, u := range parsed.Unresolved {
		in.Groups = append(in.Groups, tasks.Group{NeedsNumber: u.Reason, Files: filesOfRefs(u.Files)})
	}
	// A duplicate stem never became a group: it is a human's call, so it enters the
	// intake as its own needs_number set rather than as a numbered work.
	if res.State.Groups != nil {
		for _, d := range res.State.Groups.Duplicates {
			in.Groups = append(in.Groups, tasks.Group{
				NeedsNumber: ReasonDuplicateStem,
				Files:       filesOfRefs(d.Files),
			})
		}
	}
	// The files filter turned away are input the trigger handed over, so they are
	// part of the batch's `skipped` list with the reason the API reports. Scan's own
	// skips (hidden, excluded, a download in progress) are not here: they are not
	// part of the selection, they are the walk's own boundaries.
	if res.State.Candidates != nil {
		for _, r := range res.State.Candidates.Rejected {
			in.Skipped = append(in.Skipped, tasks.SkippedPath{Path: r.File.Path, Reason: tasks.ReasonFiltered})
		}
	}
	return in
}

// intakeFiles lists a group's files the way `task_files` stores them: the primary
// first, the other parts by index, the companions last.
func intakeFiles(g Group) []tasks.InputFile {
	out := make([]tasks.InputFile, 0, len(g.Parts)+len(g.Companions))
	for _, p := range g.Parts {
		role := tasks.RolePart
		if p.File.Path == g.Primary.Path {
			role = tasks.RolePrimary
		}
		out = append(out, tasks.InputFile{
			Path:      p.File.Path,
			Kind:      tasks.KindVideo,
			Role:      role,
			PartIndex: p.Index(),
			Size:      p.File.Size,
			ModTime:   p.File.ModTime,
		})
	}
	for _, c := range g.Companions {
		out = append(out, tasks.InputFile{
			Path:        c.File.Path,
			Kind:        tasks.KindCompanion,
			Role:        tasks.RoleCompanion,
			FollowsPart: c.FollowsPart,
			Size:        c.File.Size,
			ModTime:     c.File.ModTime,
		})
	}
	return out
}

// filesOfRefs is the fallback for a set that never became a group — a per-file
// parse miss or a duplicate stem waiting for a human.
//
// Nothing here may claim part evidence the names do not have (ADR-0003: a
// duplicate stem is not merged automatically), so part_index stays 0 for every
// file: the row's role has no "maybe" value, and the first file is the primary
// only because it is the alphabetically first, which is what the spec says a
// primary is. Whether the second file is a part of the same work or another
// version is the human's tick (`task_files.is_part`), not this function's guess.
func filesOfRefs(files []FileRef) []tasks.InputFile {
	out := make([]tasks.InputFile, len(files))
	for i, f := range files {
		role := tasks.RolePart
		if i == 0 {
			role = tasks.RolePrimary
		}
		out[i] = tasks.InputFile{
			Path:    f.Path,
			Kind:    tasks.KindVideo,
			Role:    role,
			Size:    f.Size,
			ModTime: f.ModTime,
		}
	}
	return out
}
