// Display helpers. They are pure on purpose: every view formats the same numbers,
// paths and stamps the same way, and a test can hold them still without a browser.

const UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB']

/** formatBytes renders a stored byte count in binary units (the sizes on disk). */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < UNITS.length - 1) {
    value /= 1024
    unit++
  }
  if (unit === 0) return `${Math.round(value)} B`
  const digits = value < 10 ? 1 : 0
  return `${value.toFixed(digits)} ${UNITS[unit]}`
}

function parse(iso: string): Date | null {
  if (!iso) return null
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? null : d
}

function pad(n: number): string {
  return n < 10 ? '0' + n : String(n)
}

/** formatTime renders `created_at`/`updated_at` in local time, minute precision. */
export function formatTime(iso: string): string {
  const d = parse(iso)
  if (!d) return '—'
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** formatTimeShort drops the year: the list column is dense. */
export function formatTimeShort(iso: string): string {
  const d = parse(iso)
  if (!d) return '—'
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** formatDuration renders a step's `ms`; zero means the step did not run. */
export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '—'
  if (ms < 1000) return `${Math.round(ms)} ms`
  return `${(ms / 1000).toFixed(1)} s`
}

/** baseName is the file name of a container path. */
export function baseName(path: string): string {
  if (!path) return ''
  const trimmed = path.endsWith('/') ? path.slice(0, -1) : path
  return trimmed.slice(trimmed.lastIndexOf('/') + 1)
}

/** parentDir is the file's directory, as the scan table's second line shows it. */
export function parentDir(path: string): string {
  if (!path) return ''
  const trimmed = path.endsWith('/') ? path.slice(0, -1) : path
  const slash = trimmed.lastIndexOf('/')
  return slash <= 0 ? '/' : trimmed.slice(0, slash)
}

/** shortPath keeps the tail of a long container path readable in one cell. */
export function shortPath(path: string, max = 34): string {
  if (path.length <= max) return path
  const tail = path.slice(path.length - (max - 1))
  const slash = tail.indexOf('/')
  return '…' + (slash >= 0 ? tail.slice(slash) : tail)
}

/** clamp keeps a page or an index inside its bounds. */
export function clamp(n: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, n))
}
