import { useEffect, useState } from 'react'
import type { BrowseResult } from '../api/types'
import { shortPath } from '../lib/format'
import { isStrictlyWithin, scopeState, type Scope } from '../lib/scope'
import { AsyncGate } from './AsyncState'
import { Button, Card, fieldClasses, Mono } from './Primitives'

// The inline Directory Deck (ADR-0016). It is a workbench on the scan page, not a
// dialog: breadcrumb, a path field that jumps immediately, one entry point per
// `browse_roots` entry, a child-directory table with the four-state toggles, and the
// two-column Scope Ledger underneath. Paths are container paths; there is no host
// path mapping and no drag-and-drop.

export interface DirectoryDeckProps {
  path: string
  browse?: BrowseResult
  loading?: boolean
  error?: unknown
  scope: Scope
  stats: boolean
  /** The last refused toggle, shown where the toggle was pressed. */
  notice?: string
  onNavigate: (path: string) => void
  onToggleRoot: (path: string) => void
  onToggleExclude: (path: string) => void
  onToggleStats: () => void
  onRetry?: () => void
}

/** mountLabel is the label of the innermost browse root that contains path. */
export function mountLabel(path: string, roots: BrowseResult['roots'] | undefined): string {
  let best: { path: string; label: string } | undefined
  for (const root of roots ?? []) {
    const inside = path === root.path || path.startsWith(root.path.endsWith('/') ? root.path : root.path + '/')
    if (inside && (!best || root.path.length > best.path.length)) best = root
  }
  return best?.label ?? ''
}

function crumbs(path: string): { label: string; path: string }[] {
  const parts = path.split('/').filter(Boolean)
  const out: { label: string; path: string }[] = []
  let acc = ''
  for (const part of parts) {
    acc += '/' + part
    out.push({ label: part, path: acc })
  }
  return out
}

export function DirectoryDeck({
  path,
  browse,
  loading = false,
  error,
  scope,
  stats,
  notice,
  onNavigate,
  onToggleRoot,
  onToggleExclude,
  onToggleStats,
  onRetry,
}: DirectoryDeckProps) {
  const [typed, setTyped] = useState(path)
  useEffect(() => {
    setTyped(path)
  }, [path])

  const roots = browse?.roots ?? []
  const dirs = browse?.dirs ?? []
  const label = browse?.label || mountLabel(path, roots)
  const here = scopeState(scope, path)
  const canExcludeHere = scope.roots.some((root) => isStrictlyWithin(path, root))
  const segments = crumbs(path)

  return (
    <Card>
      <div className="border-b border-line px-4 py-3">
        <div className="flex flex-wrap items-baseline gap-2">
          <h2 className="text-lead font-semibold">目录工作台</h2>
          <span className="text-micro text-text-muted">
            浏览的是<b>容器内</b>文件系统；白名单由 config.yaml 的 <Mono>browse_roots</Mono> 决定。
            不做本机拖放，也不映射宿主路径。
          </span>
        </div>

        {/* One entry point per browse root: the roots themselves replace preset chips. */}
        <div className="mt-3 flex flex-wrap items-center gap-2">
          <span className="text-micro font-medium text-text-muted">入口</span>
          {roots.map((root) => {
            const active = path === root.path || path.startsWith(root.path + '/')
            return (
              <Button
                key={root.path}
                size="sm"
                variant={active ? 'primary' : 'secondary'}
                onClick={() => onNavigate(root.path)}
                title={root.path}
              >
                {root.label}
              </Button>
            )
          })}
          {!roots.length && !loading ? (
            <span className="text-micro text-text-subtle">（配置里没有 browse_roots）</span>
          ) : null}
        </div>
      </div>

      <div className="space-y-3 px-4 py-3">
        <nav aria-label="当前路径" className="flex flex-wrap items-center gap-1 text-cell">
          <span className="mr-1 rounded-full border border-line bg-surface-sunken px-2 py-0.5 text-micro text-text-muted">
            {label || '未标注挂载点'}
          </span>
          <button type="button" className="text-accent hover:underline" onClick={() => onNavigate('/')}>
            /
          </button>
          {segments.map((seg, i) => (
            <span key={seg.path} className="flex items-center gap-1">
              {i > 0 ? <span className="text-text-subtle">/</span> : null}
              <button
                type="button"
                className="text-accent hover:underline"
                onClick={() => onNavigate(seg.path)}
              >
                {seg.label}
              </button>
            </span>
          ))}
          <span className="ml-2 grow" />
          <span className="font-mono text-micro text-text-subtle">{shortPath(path, 48)}</span>
        </nav>

        <form
          className="flex flex-wrap items-center gap-2"
          onSubmit={(e) => {
            e.preventDefault()
            if (typed.trim()) onNavigate(typed.trim())
          }}
        >
          <label htmlFor="deck-path" className="text-micro font-medium text-text-muted">
            绝对路径
          </label>
          <input
            id="deck-path"
            className={`${fieldClasses} w-full max-w-md font-mono`}
            value={typed}
            placeholder="/media/2024"
            onChange={(e) => setTyped(e.target.value)}
          />
          <Button size="sm" type="submit" disabled={!typed.trim()}>
            前往
          </Button>
          <Button
            size="sm"
            variant={here === 'root' ? 'primary' : 'secondary'}
            onClick={() => onToggleRoot(path)}
            disabled={!path}
            title={here === 'root' ? '把当前目录移出待扫描清单' : '把当前目录加入待扫描清单'}
          >
            {here === 'root' ? '✓ 移出扫描' : '+ 加入扫描'}
          </Button>
          <Button
            size="sm"
            variant={here === 'exclude' ? 'primary' : 'secondary'}
            onClick={() => onToggleExclude(path)}
            disabled={!canExcludeHere && here !== 'exclude'}
            title={
              canExcludeHere || here === 'exclude'
                ? '整棵子树跳过（当前目录）'
                : '排除必须是某个待扫描目录的真子目录'
            }
          >
            {here === 'exclude' ? '取消排除' : '+ 排除'}
          </Button>
          <span className="grow" />
          <Button size="sm" variant="quiet" onClick={onToggleStats} aria-pressed={stats}>
            {stats ? '✓ 已统计子目录' : '统计子目录内容'}
          </Button>
        </form>

        {stats ? (
          <p className="text-micro text-text-subtle">
            统计每个子目录各一次 <Mono>readdir</Mono>（不递归）；目录多时这会变慢，所以默认不请求。
          </p>
        ) : null}

        {notice ? (
          <p role="alert" className="rounded-md border border-danger-mark/40 bg-danger-tint px-3 py-2 text-cell text-danger-ink">
            {notice}
          </p>
        ) : null}

        <AsyncGate
          loading={loading && !browse}
          error={error}
          onRetry={onRetry}
          empty={!loading && dirs.length === 0}
          emptyTitle="这个目录下没有子目录"
          emptyHint="可以直接在上面的路径框里输入容器内的绝对路径，或回到某个 browse_roots 入口。"
          emptyAction={
            roots[0] ? (
              <Button size="sm" onClick={() => onNavigate(roots[0].path)}>
                回到入口 {roots[0].label}
              </Button>
            ) : undefined
          }
          skeleton={
            <div className="space-y-2" aria-hidden="true">
              {Array.from({ length: 4 }, (_, i) => (
                <div key={i} className="skeleton h-8 rounded" />
              ))}
            </div>
          }
        >
          <div className="table-wrap rounded-md border border-line">
            <table className="record-table">
              <caption className="sr-only">子目录：目录名、包含内容统计、容器内完整路径与目录操作</caption>
              <thead>
                <tr>
                  <th scope="col">目录名</th>
                  <th scope="col">包含内容</th>
                  <th scope="col">容器内完整路径</th>
                  <th scope="col">目录操作</th>
                </tr>
              </thead>
              <tbody>
                {dirs.map((dir) => {
                  const state = scopeState(scope, dir.path)
                  const canExclude = scope.roots.some((root) => isStrictlyWithin(dir.path, root))
                  return (
                    <tr key={dir.path}>
                      <td data-label="目录名">
                        <button
                          type="button"
                          className="font-mono text-cell text-accent hover:underline"
                          onClick={() => onNavigate(dir.path)}
                        >
                          {dir.name}/
                        </button>
                      </td>
                      <td data-label="包含内容" className="text-cell text-text-muted">
                        {dir.entries === undefined ? (
                          <span className="text-text-subtle">按需请求</span>
                        ) : (
                          <>
                            {dir.entries} 项 · {dir.videos ?? 0} 个视频
                          </>
                        )}
                      </td>
                      <td data-label="完整路径" className="truncate-cell font-mono text-micro text-text-subtle" title={dir.path}>
                        {dir.path}
                      </td>
                      <td data-label="操作">
                        <span className="flex flex-wrap gap-1">
                          <Button
                            size="sm"
                            variant={state === 'root' ? 'primary' : 'secondary'}
                            onClick={() => onToggleRoot(dir.path)}
                          >
                            {state === 'root' ? '✓ 移出扫描' : '+ 加入扫描'}
                          </Button>
                          <Button
                            size="sm"
                            variant={state === 'exclude' ? 'primary' : 'secondary'}
                            onClick={() => onToggleExclude(dir.path)}
                            disabled={!canExclude && state !== 'exclude'}
                            title={
                              canExclude || state === 'exclude'
                                ? '整棵子树跳过'
                                : '排除必须是某个待扫描目录的真子目录'
                            }
                          >
                            {state === 'exclude' ? '取消排除' : '+ 排除'}
                          </Button>
                        </span>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        </AsyncGate>
      </div>

      <ScopeLedger scope={scope} roots={roots} onToggleRoot={onToggleRoot} onToggleExclude={onToggleExclude} />
    </Card>
  )
}

/**
 * ScopeLedger is the two-column summary of what the next scan will walk: the chosen
 * directories on the left, the subtrees to skip on the right, and one footer that
 * says which whitelist protects it.
 */
export function ScopeLedger({
  scope,
  roots,
  onToggleRoot,
  onToggleExclude,
}: {
  scope: Scope
  roots: BrowseResult['roots']
  onToggleRoot: (path: string) => void
  onToggleExclude: (path: string) => void
}) {
  return (
    <div className="border-t border-line">
      <div className="grid gap-0 sm:grid-cols-2">
        <div className="p-4">
          <h3 className="text-cell font-semibold">待扫描目录清单</h3>
          <p className="mt-1 text-micro text-text-muted">多根并存：勾中的每个目录都会被走一遍。</p>
          <ul className="mt-2 space-y-1">
            {scope.roots.length === 0 ? (
              <li className="text-micro text-text-subtle">（空）从上面的子目录表里加入一个。</li>
            ) : (
              scope.roots.map((root) => (
                <li
                  key={root}
                  className="flex flex-wrap items-center gap-2 rounded-md border border-line bg-surface-raised px-2 py-1"
                >
                  <span className="rounded-full bg-accent-soft px-2 py-0.5 text-micro text-accent">
                    {mountLabel(root, roots) || '未标注'}
                  </span>
                  <Mono className="min-w-0 flex-1 truncate text-cell" title={root}>
                    {root}
                  </Mono>
                  <Button size="sm" variant="quiet" onClick={() => onToggleRoot(root)}>
                    ✓ 移出扫描
                  </Button>
                </li>
              ))
            )}
          </ul>
        </div>
        <div className="border-t border-line p-4 sm:border-t-0 sm:border-l">
          <h3 className="text-cell font-semibold">排除过滤清单（整树跳过）</h3>
          <p className="mt-1 text-micro text-text-muted">
            只认子树，不认 glob；必须是某个扫描目录的真子目录。
          </p>
          <ul className="mt-2 space-y-1">
            {scope.excludes.length === 0 ? (
              <li className="text-micro text-text-subtle">（空）暂时不跳过任何目录。</li>
            ) : (
              scope.excludes.map((ex) => (
                <li
                  key={ex}
                  className="flex flex-wrap items-center gap-2 rounded-md border border-line bg-surface-raised px-2 py-1"
                >
                  <Mono className="min-w-0 flex-1 truncate text-cell" title={ex}>
                    {ex}
                  </Mono>
                  <Button size="sm" variant="quiet" onClick={() => onToggleExclude(ex)}>
                    取消排除
                  </Button>
                </li>
              ))
            )}
          </ul>
        </div>
      </div>
      <p className="border-t border-line px-4 py-2 text-micro text-text-muted" aria-live="polite">
        共 {scope.roots.length} 个扫描目录 · {scope.excludes.length} 条排除；范围受{' '}
        <Mono>browse_roots</Mono> 白名单（{roots.map((r) => r.path).join('、') || '空'}）保护。
      </p>
    </div>
  )
}
