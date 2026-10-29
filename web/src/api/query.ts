import type { TaskStatus, Trigger } from './types'

/** The page sizes the API accepts. Anything else is a 400, so the select is these. */
export const PER_PAGE = [20, 50, 100] as const

/** The seven statuses, in the order the list's chips show them. */
export const STATUSES: TaskStatus[] = [
  'queued',
  'running',
  'succeeded',
  'partial',
  'failed',
  'needs_number',
  'cancelled',
]

export const TRIGGERS: Trigger[] = ['manual', 'monitor', 'webhook']

/**
 * TaskFilters is the list's whole query surface. `statuses` is a set because the
 * API ORs repeated `status` parameters; empty means "every status".
 */
export interface TaskFilters {
  statuses: TaskStatus[]
  batchId: number
  trigger: Trigger | ''
  number: string
  createdAfter: string
  createdBefore: string
  page: number
  perPage: number
}

export function emptyTaskQuery(): TaskFilters {
  return {
    statuses: [],
    batchId: 0,
    trigger: '',
    number: '',
    createdAfter: '',
    createdBefore: '',
    page: 1,
    perPage: 20,
  }
}

/**
 * taskQuery renders the filters as the query string `GET /api/tasks` expects.
 * Defaults are omitted rather than sent as empty: the service treats an empty
 * parameter as absent, and a URL that names only what differs is easier to read in
 * a bug report.
 */
export function taskQuery(filters: TaskFilters): string {
  const params = new URLSearchParams()
  for (const status of filters.statuses) params.append('status', status)
  if (filters.batchId > 0) params.set('batch', String(filters.batchId))
  if (filters.trigger) params.set('trigger', filters.trigger)
  const number = filters.number.trim().toUpperCase()
  if (number) params.set('number', number)
  if (filters.createdAfter) params.set('created_after', filters.createdAfter)
  if (filters.createdBefore) params.set('created_before', filters.createdBefore)
  params.set('page', String(Math.max(1, Math.trunc(filters.page))))
  params.set('per_page', String(filters.perPage))
  return '?' + params.toString()
}

/** pages is the pagination control's window: every page when there are few. */
export function pages(total: number, perPage: number): number[] {
  const last = Math.max(1, Math.ceil(total / Math.max(1, perPage)))
  return Array.from({ length: last }, (_, i) => i + 1)
}
