import type { NumberFill, TaskFileView } from '../api/types'

// The needs-number panel's form state. Two entrances (the sidebar page and the task
// detail) render the same panel over the same endpoint, so the draft, the
// "everything must be filled" rule and the request body live here once.

export interface DraftEntry {
  number: string
  isPart: boolean
}

export type NumberDraft = Record<string, DraftEntry>

/** draftFor seeds the panel from the rows the API already carries. */
export function draftFor(files: TaskFileView[]): NumberDraft {
  const draft: NumberDraft = {}
  for (const f of files) {
    draft[f.path] = { number: f.assigned_number, isPart: f.is_part || f.role === 'part' }
  }
  return draft
}

/**
 * missingPaths is the client half of the API's own gate: every video needs a
 * number, a companion never does. Submitting is disabled until this is empty, and
 * the same paths come back as `detail.missing_paths` if the check is bypassed.
 */
export function missingPaths(files: TaskFileView[], draft: NumberDraft): string[] {
  return files
    .filter((f) => f.kind === 'video' && (draft[f.path]?.number ?? '').trim() === '')
    .map((f) => f.path)
}

/** buildFills is the request body: one entry per video, in table order. */
export function buildFills(files: TaskFileView[], draft: NumberDraft): NumberFill[] {
  return files
    .filter((f) => f.kind === 'video')
    .map((f) => ({
      path: f.path,
      assigned_number: (draft[f.path]?.number ?? '').trim(),
      is_part: draft[f.path]?.isPart ?? false,
    }))
}
