// The payload types of the 15 V1 endpoints, spelled as the API spells them
// (snake_case, machine codes only). Nothing here is invented: every field exists in
// the Go response, and Chinese wording lives in `lib/labels.ts` instead.

export type TaskStatus =
  | 'queued'
  | 'running'
  | 'succeeded'
  | 'partial'
  | 'failed'
  | 'needs_number'
  | 'cancelled'

export type Trigger = 'manual' | 'monitor' | 'webhook'

/** The list envelope every collection endpoint uses. */
export interface Page<T> {
  items: T[]
  total: number
  page: number
  per_page: number
}

/** One line of the preview table. `res` does not exist: V1 probes no video stream. */
export interface PreviewGroup {
  primary: string
  parts: string[]
  number: string
  candidates: string[]
  marker: string
  dir: string
  size: number
  occupied: boolean
}

/** A file a filter rule kept out, with the rule's name (`no_match` when none did). */
export interface PreviewFiltered {
  path: string
  rule: string
}

export interface PreviewError {
  code: string
  message: string
}

export type PreviewState = 'running' | 'succeeded' | 'failed'

export interface PreviewResult {
  state: PreviewState
  walked_files: number
  matched: number
  groups: PreviewGroup[]
  filtered: PreviewFiltered[]
  filtered_total: number
  error?: PreviewError
}

export interface PreviewRequest {
  pipeline: string
  roots: string[]
  exclude: string[]
}

export interface CreateBatchRequest {
  pipeline: string
  roots: string[]
  exclude?: string[]
  /** The scan page's per-group selection: primary paths. Absent = every group. */
  groups?: string[]
}

export interface SkippedPath {
  path: string
  reason: 'path_occupied' | 'filtered' | 'not_found'
}

export interface CreateBatchResult {
  batch_id: number
  created: number
  skipped: SkippedPath[]
}

/** One input file of a task, with what `parse` offered for it. */
export interface TaskFileView {
  id: number
  path: string
  kind: string
  role: 'primary' | 'part' | 'companion' | string
  part_index: number
  follows_part: number
  size: number
  mtime: string
  active: boolean
  assigned_number: string
  is_part: boolean
  candidates: string[]
}

/** One task row plus the fields the API derives for display. */
export interface TaskView {
  id: number
  status: TaskStatus
  number: string
  batch_id: number
  pipeline_id: string
  last_error: string
  created_at: string
  updated_at: string
  stage: string
  title: string
  sources: string[]
  missing: string[]
  partial: boolean
  trigger: Trigger | string
  trigger_ref: string
  file_count: number
  out_dir: string
  /** Present only for a needs_number task: the panel's rows without a second request. */
  files?: TaskFileView[]
}

export interface Actor {
  name?: string
  role?: string
  thumb?: string
}

export interface MetadataView {
  title: string
  plot: string
  extrafanarts: string[]
  tags: string[]
  releasedate: string
  actors: Actor[]
  director: string[]
  runtime: number
  rating: number
  series: string
  studio: string
  publisher: string
  poster: string
  fanart: string
  sources: Record<string, string>
  missing: string[]
  partial: boolean
}

export interface StepView {
  id: number
  idx: number
  type: string
  name: string
  status: string
  started_at: string
  ms: number
  summary: unknown
  error: string
}

export interface SourceRunView {
  id: number
  step_run_id: number
  source_id: string
  status: string
  err: string
  reason?: string
  fields: unknown
  ms: number
}

export interface FileRowView {
  id: number
  task_id: number
  task_run_id: number
  step_run_id: number
  kind: string
  role: string
  idx: number
  src: string
  dst: string
  bytes: number
  sha256: string
  action: string
  created_at: string
}

export interface ImageView {
  role: 'poster' | 'fanart' | 'extrafanarts' | string
  idx: number
  src: string
  url: string
}

export interface TaskDetail extends TaskView {
  metadata: MetadataView
  task_files: TaskFileView[]
  step_runs: StepView[]
  source_runs: SourceRunView[]
  file_rows: FileRowView[]
  images: ImageView[]
}

export interface BatchView {
  id: number
  trigger: string
  trigger_ref: string
  pipeline_id: string
  status: string
  total: number
  tasks: number
  succeeded: number
  partial: number
  failed: number
  needs_number: number
  skipped: number
  error: string
  started_at: string
  finished_at: string | null
}

export interface PipelineView {
  id: string
  steps: string[]
}

export interface BrowseRoot {
  path: string
  label: string
}

export interface BrowseDir {
  name: string
  path: string
  /** Present only when the listing was asked for statistics (`?stats=1`). */
  entries?: number
  videos?: number
}

export interface BrowseResult {
  path: string
  label: string
  roots: BrowseRoot[]
  dirs: BrowseDir[]
}

export interface NumberFill {
  path: string
  assigned_number: string
  is_part: boolean
}

export interface NumbersResult {
  task_id: number
  status: TaskStatus
}

export interface RerunResult {
  task_id: number
  status: TaskStatus
}

export interface BatchRerunItem {
  task_id: number
  ok: boolean
  error?: string
}
