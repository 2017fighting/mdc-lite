// The Scope Ledger's rules. They mirror the service's, so the Deck refuses the same
// things the server would (ADR-0016): roots may not be equal or nested, an exclusion
// must be a true child of a root, and removing a root cannot leave an exclusion
// behind that the submit would then reject.

export interface Scope {
  roots: string[]
  excludes: string[]
}

export const EMPTY_SCOPE: Scope = { roots: [], excludes: [] }

export type ScopeState = 'root' | 'exclude' | 'none'

/** normalizePath trims a container path; the server does the same with filepath.Abs. */
export function normalizePath(path: string): string {
  const trimmed = path.trim()
  if (!trimmed) return ''
  if (trimmed === '/') return '/'
  return trimmed.replace(/\/+$/, '')
}

/** isWithin is lexical containment: `/media2` is not inside `/media`. */
export function isWithin(child: string, root: string): boolean {
  if (!child || !root) return false
  if (child === root) return true
  const prefix = root === '/' ? '/' : root + '/'
  return child.startsWith(prefix)
}

/** isStrictlyWithin refuses equality: excluding a whole root is "do not select it". */
export function isStrictlyWithin(child: string, root: string): boolean {
  return child !== root && isWithin(child, root)
}

/** scopeState is what the four-state toggle renders for one path. */
export function scopeState(scope: Scope, path: string): ScopeState {
  const p = normalizePath(path)
  if (scope.roots.includes(p)) return 'root'
  if (scope.excludes.includes(p)) return 'exclude'
  return 'none'
}

export interface ScopeResult {
  scope: Scope
  error?: string
}

/** addRoot adds one more root, refusing the same path twice or a nested pair. */
export function addRoot(scope: Scope, path: string): ScopeResult {
  const p = normalizePath(path)
  if (!p) return { scope, error: '没有选择目录。' }
  for (const root of scope.roots) {
    if (root === p) return { scope, error: '这个目录已在待扫描清单里，不能重复加入。' }
    if (isWithin(p, root) || isWithin(root, p)) {
      return { scope, error: '扫描目录不能相同或互相嵌套：先移出外层目录，再加入这个。' }
    }
  }
  return { scope: { ...scope, roots: [...scope.roots, p] } }
}

/** removeRoot also drops the exclusions that only made sense under that root. */
export function removeRoot(scope: Scope, path: string): Scope {
  const p = normalizePath(path)
  const roots = scope.roots.filter((root) => root !== p)
  const excludes = scope.excludes.filter((ex) => roots.some((root) => isStrictlyWithin(ex, root)))
  return { roots, excludes }
}

/** addExclude adds an exclusion that is a true child of one of the roots. */
export function addExclude(scope: Scope, path: string): ScopeResult {
  const p = normalizePath(path)
  if (!p) return { scope, error: '没有选择目录。' }
  if (!scope.roots.some((root) => isStrictlyWithin(p, root))) {
    return {
      scope,
      error: '排除必须是某个扫描目录的真子目录：先加入它上层的扫描目录。',
    }
  }
  if (scope.excludes.includes(p)) return { scope }
  return { scope: { ...scope, excludes: [...scope.excludes, p] } }
}

export function removeExclude(scope: Scope, path: string): Scope {
  const p = normalizePath(path)
  return { ...scope, excludes: scope.excludes.filter((ex) => ex !== p) }
}

/** toggleRoot is `+ 加入扫描` / `✓ 移出扫描`. */
export function toggleRoot(scope: Scope, path: string): ScopeResult {
  if (scopeState(scope, path) === 'root') return { scope: removeRoot(scope, path) }
  return addRoot(scope, path)
}

/** toggleExclude is `+ 排除` / `取消排除`. */
export function toggleExclude(scope: Scope, path: string): ScopeResult {
  if (scopeState(scope, path) === 'exclude') return { scope: removeExclude(scope, path) }
  return addExclude(scope, path)
}
