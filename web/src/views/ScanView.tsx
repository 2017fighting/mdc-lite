import { useEffect, useMemo, useRef, useState } from 'react'
import { api } from '../api/client'
import type { CreateBatchResult, PipelineView, PreviewGroup, PreviewResult } from '../api/types'
import { EmptyState, ErrorNotice } from '../components/AsyncState'
import { DirectoryDeck } from '../components/DirectoryDeck'
import { VideoIcon } from '../components/icons'
import { Badge, Button, Card, fieldClasses, Mono, Muted, SectionTitle, Skeleton } from '../components/Primitives'
import { formatBytes, baseName, parentDir } from '../lib/format'
import { skipLabel } from '../lib/labels'
import { EMPTY_SCOPE, toggleExclude, toggleRoot, type Scope } from '../lib/scope'
import { navigate } from '../lib/router'
import { useAsync } from '../hooks'

// The scan page: the Directory Deck on top, the preview table underneath, and the
// group checkboxes that become tasks. Preview and batch share one path — the same
// walk, the same groups — so what is ticked here is what the batch contains.

export function ScanView({ initialPath }: { initialPath?: string }) {
  const pipelines = useAsync(() => api.pipelines(), [])
  const [pipeline, setPipeline] = useState('')
  useEffect(() => {
    if (!pipeline && pipelines.data?.items.length) setPipeline(pipelines.data.items[0].id)
  }, [pipelines.data, pipeline])

  const [browsePath, setBrowsePath] = useState(initialPath ?? '')
  const [stats, setStats] = useState(false)
  const [scope, setScope] = useState<Scope>(EMPTY_SCOPE)
  const [notice, setNotice] = useState('')

  const browse = useAsync(() => api.browse(browsePath, stats), [browsePath, stats])

  // The first answer is the bootstrap: one entry point per browse_roots entry. Jump
  // into the first one so the Deck opens on a real listing.
  useEffect(() => {
    const data = browse.data
    if (browsePath === '' && data?.path === '' && data.roots.length > 0) {
      setBrowsePath(data.roots[0].path)
    }
  }, [browse.data, browsePath])

  const [previewID, setPreviewID] = useState<string | null>(null)
  const [startError, setStartError] = useState<unknown>(undefined)
  const [starting, setStarting] = useState(false)
  const [selection, setSelection] = useState<Set<string>>(new Set())
  const [creating, setCreating] = useState(false)
  const [created, setCreated] = useState<CreateBatchResult | null>(null)
  const [createError, setCreateError] = useState<unknown>(undefined)
  const deckRef = useRef<HTMLDivElement>(null)

  const scopeKey = scope.roots.join('|') + '::' + scope.excludes.join('|')
  useEffect(() => {
    // A different scope is a different question: the old preview no longer describes
    // what the next walk would do.
    setPreviewID(null)
    setStartError(undefined)
    setCreated(null)
    setCreateError(undefined)
    setSelection(new Set())
  }, [scopeKey])

  // One poll through the shared hook: a preview asked for without an id answers null
  // and the loop stops.
  const previewPoll = useAsync(
    () => (previewID ? api.preview(previewID) : Promise.resolve(null)),
    [previewID],
    { ms: 700, while: (res) => res?.state === 'running' },
  )
  const preview: PreviewResult | null = previewID ? (previewPoll.data ?? null) : null
  const previewError = startError ?? previewPoll.error

  async function startScan() {
    if (!pipeline || scope.roots.length === 0) return
    setStarting(true)
    setPreviewID(null)
    setStartError(undefined)
    setCreated(null)
    setCreateError(undefined)
    setSelection(new Set())
    try {
      const started = await api.startPreview({
        pipeline,
        roots: scope.roots,
        exclude: scope.excludes,
      })
      setPreviewID(started.preview_id)
    } catch (err) {
      setStartError(err)
    } finally {
      setStarting(false)
    }
  }

  async function createTasks() {
    setCreating(true)
    setCreateError(undefined)
    try {
      const res = await api.createBatch({
        pipeline,
        roots: scope.roots,
        exclude: scope.excludes,
        groups: [...selection],
      })
      setCreated(res)
      setSelection(new Set())
    } catch (err) {
      setCreateError(err)
    } finally {
      setCreating(false)
    }
  }

  function toggleGroup(primary: string) {
    setSelection((prev) => {
      const next = new Set(prev)
      if (next.has(primary)) next.delete(primary)
      else next.add(primary)
      return next
    })
  }

  function applyScope(result: { scope: Scope; error?: string }) {
    if (result.error) {
      setNotice(result.error)
      return
    }
    setNotice('')
    setScope(result.scope)
  }

  const groups = preview?.groups ?? []
  const pickedGroups = useMemo(() => groups.filter((g) => selection.has(g.primary)), [groups, selection])
  const pickedFiles = pickedGroups.reduce((n, g) => n + g.parts.length, 0)
  const running = preview?.state === 'running' || starting
  const hasRoots = scope.roots.length > 0

  return (
    <div className="space-y-4">
      <header>
        <h1 className="text-display font-semibold">扫描预览</h1>
        <p className="mt-1 text-cell text-text-muted">
          手动模式 · 预览跑到 <Mono>parse</Mono>：不写盘、不建任务。建任务时走同一条前缀，看到的分组就是任务里的分组。
        </p>
      </header>

      <Card className="px-4 py-3">
        <div className="flex flex-wrap items-end gap-3">
          <div className="flex flex-col gap-1">
            <label htmlFor="scan-pipeline" className="text-micro font-medium text-text-muted">
              流水线
            </label>
            <select
              id="scan-pipeline"
              className={`${fieldClasses} min-w-52`}
              value={pipeline}
              onChange={(e) => setPipeline(e.target.value)}
            >
              {(pipelines.data?.items ?? []).map((p: PipelineView) => (
                <option key={p.id} value={p.id}>
                  {p.id} — {p.steps.join(' → ')}
                </option>
              ))}
            </select>
          </div>
          <Button variant="primary" onClick={() => void startScan()} disabled={!hasRoots || !pipeline || running}>
            {running ? '扫描中…' : '开始扫描'}
          </Button>
          {hasRoots ? (
            <span className="text-micro text-text-muted">
              待扫描 {scope.roots.length} 个目录 · 排除 {scope.excludes.length} 条
            </span>
          ) : (
            <span className="text-micro text-text-muted">先在下面的工作台里加入至少一个扫描目录。</span>
          )}
        </div>
        {pipelines.error ? <ErrorNotice error={pipelines.error} onRetry={pipelines.reload} title="流水线读取失败" /> : null}
      </Card>

      <div ref={deckRef}>
        <DirectoryDeck
          path={browse.data?.path || browsePath}
          browse={browse.data}
          loading={browse.loading}
          error={browse.error}
          scope={scope}
          stats={stats}
          notice={notice}
          onNavigate={(path) => setBrowsePath(path)}
          onToggleRoot={(path) => applyScope(toggleRoot(scope, path))}
          onToggleExclude={(path) => applyScope(toggleExclude(scope, path))}
          onToggleStats={() => setStats((s) => !s)}
          onRetry={browse.reload}
        />
      </div>

      {!previewID && !preview && !previewError && !starting ? (
        <Card>
          <SectionTitle hint="预览与建任务共用 scan → filter → group → parse">还没开始预览</SectionTitle>
          <EmptyState
            title="选好目录后开始扫描"
            hint="扫描预览会走完分组与番号解析，但不会写盘，也不会建任务。"
            action={
              <Button variant="primary" onClick={() => void startScan()} disabled={!hasRoots || !pipeline}>
                开始扫描
              </Button>
            }
          />
        </Card>
      ) : null}

      {previewError ? (
        <ErrorNotice error={previewError} onRetry={() => void startScan()} title="扫描失败" />
      ) : null}

      {starting || (preview?.state === 'running' && !preview.groups.length) ? (
        <Card>
          <SectionTitle hint="后台走着，界面轮询">扫描中…</SectionTitle>
          <Skeleton lines={5} />
        </Card>
      ) : null}

      {preview && preview.state === 'failed' ? (
        <Card>
          <SectionTitle>扫描失败</SectionTitle>
          <ErrorNotice
            error={{ code: preview.error?.code ?? 'internal', message: preview.error?.message ?? '' }}
            onRetry={() => void startScan()}
          />
        </Card>
      ) : null}

      {preview && preview.state === 'succeeded' ? (
        <>
          <Card>
            <SectionTitle
              hint={`走到 ${preview.walked_files} 个文件 · ${preview.matched} 个进候选 · ${preview.groups.length} 组`}
            >
              预览结果
            </SectionTitle>
            {preview.groups.length === 0 ? (
              <EmptyState
                title="这个范围里没有可进入候选的文件"
                hint={
                  preview.filtered_total > 0
                    ? `扫到 ${preview.walked_files} 个文件，全被过滤规则排除了；原因见下方清单。`
                    : '目录是空的，或者选中的目录里没有文件。'
                }
                action={
                  <Button
                    onClick={() => {
                      setPreviewID(null)
                      deckRef.current?.scrollIntoView({ behavior: 'smooth', block: 'start' })
                    }}
                  >
                    回到目录工作台
                  </Button>
                }
              />
            ) : (
              <>
                  <div className="table-wrap">
                    <table>
                      <caption className="sr-only">
                        扫描候选：按分组列出文件、解析番号与占用情况
                      </caption>
                      <thead>
                        <tr>
                          <th scope="col">选择</th>
                          <th scope="col">类型</th>
                          <th scope="col">文件名 / 父目录</th>
                          <th scope="col">解析番号</th>
                          <th scope="col">分组依据</th>
                          <th scope="col">大小</th>
                          <th scope="col">占用</th>
                        </tr>
                      </thead>
                      <tbody>
                        {preview.groups.map((group) => (
                          <GroupRows
                            key={group.primary}
                            group={group}
                            selected={selection.has(group.primary)}
                            onToggle={() => toggleGroup(group.primary)}
                          />
                        ))}
                      </tbody>
                    </table>
                  </div>
                  <div className="sticky bottom-0 flex min-h-10 flex-wrap items-center gap-3 pointer-coarse:min-h-11 border-t border-line bg-surface/95 px-4 py-2 backdrop-blur">
                    <p className="text-cell" aria-live="polite">
                      已选 <b>{pickedGroups.length}</b> 组 / {pickedFiles} 个文件
                      <span className="ml-2 text-text-muted">
                        解析分歧的组会落到「待补号」
                      </span>
                    </p>
                    <span className="grow" />
                    <Button
                      size="sm"
                      variant="quiet"
                      onClick={() => setSelection(new Set(preview.groups.map((g) => g.primary)))}
                    >
                      全选
                    </Button>
                    <Button size="sm" variant="quiet" onClick={() => setSelection(new Set())} disabled={!selection.size}>
                      清空
                    </Button>
                    <Button
                      variant="primary"
                      onClick={() => void createTasks()}
                      disabled={!selection.size || creating}
                    >
                      {creating ? '建任务中…' : '建任务'}
                    </Button>
                  </div>
              </>
            )}
            {createError ? <div className="px-4 pb-4"><ErrorNotice error={createError} title="建任务失败" /></div> : null}
            {created ? <CreateResult result={created} /> : null}
          </Card>

          {preview.filtered_total > 0 ? (
            <FilteredFiles
              total={preview.filtered_total}
              files={preview.filtered}
              walked={preview.walked_files}
            />
          ) : null}
        </>
      ) : null}
    </div>
  )
}

function GroupRows({
  group,
  selected,
  onToggle,
}: {
  group: PreviewGroup
  selected: boolean
  onToggle: () => void
}) {
  const parts = group.parts.length ? group.parts : [group.primary]
  return (
    <>
      {parts.map((path, idx) => {
        const primary = idx === 0
        return (
          <tr key={path} className={primary ? 'border-t-2 border-line-strong' : ''}>
            <td>
              {primary ? (
                <input
                  type="checkbox"
                  checked={selected}
                  onChange={onToggle}
                  aria-label={`选择分组 ${baseName(group.primary)}（${parts.length} 个文件）`}
                />
              ) : (
                <span className="sr-only">随组</span>
              )}
            </td>
            <td>
              <VideoIcon className="text-text-subtle" />
            </td>
            <td>
              <div className="flex flex-wrap items-baseline gap-2">
                <Mono className="truncate-cell">{baseName(path)}</Mono>
                <Badge className={primary ? 'border-accent/40 bg-accent-soft text-accent' : 'border-line bg-surface-sunken text-text-muted'}>
                  {primary ? '主片' : `分片 ${idx + 1}`}
                </Badge>
              </div>
              <p className="truncate-cell font-mono text-micro text-text-subtle" title={parentDir(path)}>
                {parentDir(path)}
              </p>
            </td>
            <td>
              {primary ? (
                <div className="flex flex-col gap-1">
                  {group.number ? <Mono className="font-semibold">{group.number}</Mono> : null}
                  {group.candidates.length > (group.number ? 1 : 0) ? (
                    <span className="flex flex-wrap gap-1" aria-label="候选番号">
                      {group.candidates.map((c) => (
                        <span
                          key={c}
                          className="rounded-full border border-number-mark/40 bg-number-tint px-2 py-0.5 font-mono text-micro text-number-ink"
                        >
                          {c}
                        </span>
                      ))}
                    </span>
                  ) : null}
                  {group.number ? null : (
                    <span className="text-micro text-text-muted">建任务后进「待补号」</span>
                  )}
                </div>
              ) : (
                <Muted>随组</Muted>
              )}
            </td>
            <td className="text-cell text-text-muted">
              {primary ? group.marker || <Muted>无标记</Muted> : null}
            </td>
            <td className="font-mono text-cell">{primary ? formatBytes(group.size) : null}</td>
            <td>
              {primary ? (
                group.occupied ? (
                  <Badge className="border-danger-mark/40 bg-danger-tint text-danger-ink">已有活跃任务</Badge>
                ) : (
                  <Muted>—</Muted>
                )
              ) : null}
            </td>
          </tr>
        )
      })}
    </>
  )
}

function FilteredFiles({
  total,
  files,
  walked,
}: {
  total: number
  files: PreviewResult['filtered']
  walked: number
}) {
  return (
    <Card>
      <details>
        <summary className="cursor-pointer px-4 py-3 text-cell font-medium">
          被过滤的 {total} 个文件
          <span className="ml-2 text-micro font-normal text-text-muted">
            （filter 规则，不进候选；共扫到 {walked} 个文件）
          </span>
        </summary>
        <div className="border-t border-line">
          <div className="table-wrap">
            <table>
              <caption className="sr-only">被过滤的文件与规则名</caption>
              <thead>
                <tr>
                  <th scope="col">文件</th>
                  <th scope="col">规则</th>
                </tr>
              </thead>
              <tbody>
                {files.map((f) => (
                  <tr key={f.path + f.rule}>
                    <td className="truncate-cell font-mono text-cell" title={f.path}>
                      {f.path}
                    </td>
                    <td>
                      <Mono className="text-micro">{f.rule}</Mono>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {total > files.length ? (
            <p className="px-4 py-2 text-micro text-text-muted">只列出前 {files.length} 条。</p>
          ) : null}
        </div>
      </details>
    </Card>
  )
}

function CreateResult({ result }: { result: CreateBatchResult }) {
  return (
    <div className="border-t border-line px-4 py-3" role="status">
      <p className="text-cell">
        批次 <Mono>#{result.batch_id}</Mono> 已建，{result.created} 个任务进入队列。
      </p>
      {result.skipped.length ? (
        <ul className="mt-2 space-y-1 text-micro text-text-muted">
          {result.skipped.map((s) => (
            <li key={s.path} className="flex flex-wrap gap-2">
              <Mono className="truncate">{s.path}</Mono>
              <span className="rounded-full border border-line px-2 py-0.5">{skipLabel(s.reason)}</span>
            </li>
          ))}
        </ul>
      ) : null}
      <div className="mt-3 flex gap-2">
        <Button size="sm" variant="primary" onClick={() => navigate(`/tasks?batch=${result.batch_id}`)}>
          查看这批任务
        </Button>
        <Button size="sm" onClick={() => navigate('/tasks')}>
          全部任务
        </Button>
      </div>
    </div>
  )
}
