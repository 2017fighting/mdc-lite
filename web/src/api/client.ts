import type {
  BatchRerunItem,
  BatchView,
  BrowseResult,
  CreateBatchRequest,
  CreateBatchResult,
  NumberFill,
  NumbersResult,
  Page,
  PipelineView,
  PreviewRequest,
  PreviewResult,
  RerunResult,
  TaskDetail,
  TaskView,
} from './types'

/**
 * ApiError is one failed request. The payload only ever carries a machine code, so
 * the UI can pick its wording from `lib/labels.ts` instead of parsing prose; the
 * status is here for the cases that differ (422 vs 400 vs 409).
 */
export class ApiError extends Error {
  constructor(
    readonly code: string,
    message: string,
    readonly status: number,
    readonly detail?: unknown,
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

interface ErrorEnvelope {
  error: { code: string; message: string; detail?: unknown }
}

function isErrorEnvelope(v: unknown): v is ErrorEnvelope {
  return (
    typeof v === 'object' &&
    v !== null &&
    'error' in v &&
    typeof (v as ErrorEnvelope).error?.code === 'string'
  )
}

/**
 * call is the only fetch in the app. It always sends/accepts JSON, because the API
 * refuses a write that did not declare it (415), and it turns every failure into an
 * ApiError so no caller has to inspect `response.ok`.
 */
async function call<T>(path: string, init?: { method?: 'GET' | 'POST'; body?: unknown }): Promise<T> {
  const method = init?.method ?? 'GET'
  let response: Response
  try {
    response = await fetch(path, {
      method,
      headers: init?.body === undefined ? { Accept: 'application/json' } : { Accept: 'application/json', 'Content-Type': 'application/json' },
      body: init?.body === undefined ? undefined : JSON.stringify(init.body),
    })
  } catch (err) {
    throw new ApiError('network', err instanceof Error ? err.message : String(err), 0)
  }
  const text = await response.text()
  let parsed: unknown = undefined
  if (text) {
    try {
      parsed = JSON.parse(text)
    } catch {
      throw new ApiError('bad_response', `响应不是 JSON：${text.slice(0, 120)}`, response.status)
    }
  }
  if (!response.ok) {
    if (isErrorEnvelope(parsed)) {
      throw new ApiError(parsed.error.code, parsed.error.message, response.status, parsed.error.detail)
    }
    throw new ApiError('http_' + response.status, `请求失败（HTTP ${response.status}）`, response.status)
  }
  return parsed as T
}

/** missingPaths reads a 422 `numbers_incomplete` body's `detail.missing_paths`. */
export function missingPaths(err: unknown): string[] {
  if (!(err instanceof ApiError)) return []
  const detail = err.detail
  if (typeof detail !== 'object' || detail === null) return []
  const paths = (detail as { missing_paths?: unknown }).missing_paths
  return Array.isArray(paths) ? paths.filter((p): p is string => typeof p === 'string') : []
}

export const api = {
  pipelines: () => call<Page<PipelineView>>('/api/pipelines'),

  browse: (path: string, stats = false) =>
    call<BrowseResult>(`/api/fs/dirs?path=${encodeURIComponent(path)}${stats ? '&stats=1' : ''}`),

  startPreview: (req: PreviewRequest) =>
    call<{ preview_id: string }>('/api/scan/preview', { method: 'POST', body: req }),

  preview: (id: string) => call<PreviewResult>(`/api/scan/previews/${encodeURIComponent(id)}`),

  createBatch: (req: CreateBatchRequest) =>
    call<CreateBatchResult>('/api/tasks', { method: 'POST', body: req }),

  tasks: (query: string) => call<Page<TaskView>>(`/api/tasks${query}`),

  task: (id: number) => call<TaskDetail>(`/api/tasks/${id}`),

  rerun: (id: number) => call<RerunResult>(`/api/tasks/${id}/rerun`, { method: 'POST', body: {} }),

  cancel: (id: number) => call<{ status: string }>(`/api/tasks/${id}/cancel`, { method: 'POST', body: {} }),

  rerunMany: (ids: number[]) =>
    call<BatchRerunItem[]>('/api/tasks/rerun', { method: 'POST', body: { ids } }),

  fillNumbers: (id: number, files: NumberFill[]) =>
    call<NumbersResult>(`/api/tasks/${id}/numbers`, { method: 'POST', body: { files } }),

  batches: (limit = 50) => call<Page<BatchView>>(`/api/batches?limit=${limit}`),
}

/**
 * imageSrc is one image's endpoint. `w` must be one of the API's allowed widths or
 * absent (the original); the list's thumbnails are 40px by the server's own default.
 */
export function imageSrc(url: string, width?: number): string {
  if (!width) return url
  return `${url}&w=${width}`
}
