import { useEffect, useMemo, useState } from 'react'
import { api } from '../api/client'
import { emptyTaskQuery, PER_PAGE, STATUSES, taskQuery, TRIGGERS, type TaskFilters } from '../api/query'
import type { BatchRerunItem, BatchView, TaskStatus, TaskView, Trigger } from '../api/types'
import { AsyncGate, ErrorNotice } from '../components/AsyncState'
import { StatusPill } from '../components/StatusPill'
import { Button, Card, fieldClasses, Mono, Muted, Skeleton } from '../components/Primitives'
import { useAsync, useDebounced, isLiveStatus } from '../hooks'
import { formatTimeShort } from '../lib/format'
import { errorText, statusMeta, stepLabel, taskErrorLabel, triggerLabel } from '../lib/labels'
import { navigate, replaceLocation, useQuery } from '../lib/router'

// The task list: the seven status chips with counts, the four filters, the 8-column
// table, bulk rerun (the only bulk action) and page-number pagination. A failure
// takes the row's second line as a pill; a batch is a filter, not a view.

const TIME_RANGES = [
  { id: '', label: '全部时间' },
  { id: '24h', label: '最近 24 小时' },
  { id: '7d', label: '最近 7 天' },
] as const

type TimeRange = (typeof TIME_RANGES)[number]['id']

function initialFilters(search: URLSearchParams): { filters: TaskFilters; timeRange: TimeRange } {
  const perPage = PER_PAGE.find((n) => n === Number(search.get('per_page'))) ?? 20
  const page = Number(search.get('page') ?? 1)
  const statuses = search.getAll('status').filter((s): s is TaskStatus =>
    (STATUSES as string[]).includes(s),
  )
  const trigger = (search.get('trigger') ?? '') as Trigger | ''
  const time = search.get('time')
  return {
    filters: {
      ...emptyTaskQuery(),
      statuses,
      batchId: Number(search.get('batch') ?? 0) || 0,
      trigger: (TRIGGERS as string[]).includes(trigger) ? trigger : '',
      number: search.get('number') ?? '',
      page: Number.isFinite(page) && page > 0 ? page : 1,
      perPage,
    },
    timeRange: TIME_RANGES.some((r) => r.id === time) ? (time as TimeRange) : '',
  }
}

function createdAfter(range: TimeRange): string {
  if (!range) return ''
  const hours = range === '24h' ? 24 : 24 * 7
  return new Date(Date.now() - hours * 3600_000).toISOString()
}

async function loadCounts(filters: TaskFilters): Promise<Record<string, number>> {
  // One request per status plus one for "all": the API's envelope carries the total,
  // and a status chip without its count is a chip nobody trusts.
  const base: TaskFilters = { ...filters, statuses: [], page: 1 }
  const [all, ...each] = await Promise.all([
    api.tasks(taskQuery(base)),
    ...STATUSES.map((status) => api.tasks(taskQuery({ ...base, statuses: [status] }))),
  ])
  const counts: Record<string, number> = { all: all.total }
  STATUSES.forEach((status, i) => {
    counts[status] = each[i].total
  })
  return counts
}

export function TaskListView() {
  const search = useQuery()
  const [initial] = useState(() => initialFilters(search))
  const [filters, setFilters] = useState<TaskFilters>(initial.filters)
  const [timeRange, setTimeRange] = useState<TimeRange>(initial.timeRange)
  const [numberInput, setNumberInput] = useState(initial.filters.number)
  const debouncedNumber = useDebounced(numberInput, 300)
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [bulk, setBulk] = useState<BatchRerunItem[] | null>(null)
  const [actionError, setActionError] = useState<unknown>(undefined)
  const [actionNote, setActionNote] = useState('')

  useEffect(() => {
    setFilters((prev) =>
      prev.number === debouncedNumber ? prev : { ...prev, number: debouncedNumber, page: 1 },
    )
  }, [debouncedNumber])

  const effective = useMemo<TaskFilters>(
    () => ({ ...filters, createdAfter: createdAfter(timeRange) }),
    [filters, timeRange],
  )
  const query = taskQuery(effective)

  // The filters live in the URL: a refresh, a copied link and a trip to a task's
  // detail all come back to the same list.
  useEffect(() => {
    const params = new URLSearchParams(query)
    if (timeRange) params.set('time', timeRange)
    replaceLocation('/tasks?' + params.toString())
  }, [query, timeRange])

  const list = useAsync(() => api.tasks(query), [query], {
    ms: 3000,
    while: (page) => (page?.items ?? []).some((t) => isLiveStatus(t.status)),
  })
  const counts = useAsync(
    () => loadCounts(effective),
    [filters.batchId, filters.trigger, filters.number, effective.createdAfter],
    { ms: 3000, while: () => (list.data?.items ?? []).some((t) => isLiveStatus(t.status)) },
  )
  const batches = useAsync(() => api.batches(50), [])

  const items = list.data?.items ?? []
  const total = list.data?.total ?? 0
  const perPage = filters.perPage
  const page = filters.page
  const pageCount = Math.max(1, Math.ceil(total / perPage))

  function patch(next: Partial<TaskFilters>) {
    setFilters((prev) => ({ ...prev, ...next }))
    setSelected(new Set())
    setBulk(null)
    setActionNote('')
  }

  function toggleStatus(status: TaskStatus) {
    const has = filters.statuses.includes(status)
    patch({
      statuses: has ? filters.statuses.filter((s) => s !== status) : [...filters.statuses, status],
      page: 1,
    })
  }

  async function rerunOne(task: TaskView) {
    setActionError(undefined)
    setActionNote('')
    try {
      await api.rerun(task.id)
      setActionNote(`任务 #${task.id} 已回到队列`)
      list.reload()
      counts.reload()
    } catch (err) {
      setActionError(err)
    }
  }

  async function rerunSelected() {
    setActionError(undefined)
    setBulk(null)
    try {
      const results = await api.rerunMany([...selected])
      setBulk(results)
      setSelected(new Set())
      list.reload()
      counts.reload()
    } catch (err) {
      setActionError(err)
    }
  }

  const filtersActive =
    filters.statuses.length > 0 ||
    filters.batchId > 0 ||
    filters.trigger !== '' ||
    filters.number !== '' ||
    timeRange !== ''

  function clearFilters() {
    setNumberInput('')
    setTimeRange('')
    setFilters({ ...emptyTaskQuery(), perPage })
  }

  return (
    <div className="space-y-4">
      <header className="flex flex-wrap items-end justify-between gap-2">
        <div>
          <h1 className="text-display font-semibold">任务列表</h1>
          <p className="mt-1 text-cell text-text-muted">
            共 {total} 条 · 每页 {perPage} · 按创建时间倒序 · 进度每 3 秒轮询（只在有非终态任务时）
          </p>
        </div>
        <Button size="sm" onClick={() => { list.reload(); counts.reload() }}>
          刷新
        </Button>
      </header>

      <Card className="px-4 py-3">
        <div className="flex flex-wrap items-center gap-2" role="group" aria-label="按状态筛选">
          <button
            type="button"
            aria-pressed={filters.statuses.length === 0}
            onClick={() => patch({ statuses: [], page: 1 })}
            className={chipClasses(filters.statuses.length === 0)}
          >
            全部 {counts.data?.all ?? ''}
          </button>
          {STATUSES.map((status) => {
            const meta = statusMeta(status)
            const active = filters.statuses.includes(status)
            return (
              <button
                key={status}
                type="button"
                aria-pressed={active}
                onClick={() => toggleStatus(status)}
                className={`${chipClasses(active)} ${active ? '' : meta.classes}`}
              >
                <span aria-hidden="true" className={`size-1.5 rounded-full ${meta.dot}`} />
                {meta.label} {counts.data?.[status] ?? ''}
              </button>
            )
          })}
        </div>

        <div className="mt-3 flex flex-wrap items-end gap-3">
          <div className="flex flex-col gap-1">
            <label htmlFor="f-batch" className="text-micro font-medium text-text-muted">
              批次
            </label>
            <select
              id="f-batch"
              className={fieldClasses}
              value={filters.batchId}
              onChange={(e) => patch({ batchId: Number(e.target.value), page: 1 })}
            >
              <option value={0}>全部批次</option>
              {(batches.data?.items ?? []).map((b: BatchView) => (
                <option key={b.id} value={b.id}>
                  #{b.id} · {triggerLabel(b.trigger)} · {formatTimeShort(b.started_at)}
                </option>
              ))}
            </select>
          </div>
          <div className="flex flex-col gap-1">
            <label htmlFor="f-trigger" className="text-micro font-medium text-text-muted">
              触发源
            </label>
            <select
              id="f-trigger"
              className={fieldClasses}
              value={filters.trigger}
              onChange={(e) => patch({ trigger: e.target.value as Trigger | '', page: 1 })}
            >
              <option value="">全部触发源</option>
              {TRIGGERS.map((t) => (
                <option key={t} value={t}>
                  {triggerLabel(t)}
                </option>
              ))}
            </select>
          </div>
          <div className="flex flex-col gap-1">
            <label htmlFor="f-time" className="text-micro font-medium text-text-muted">
              时间
            </label>
            <select
              id="f-time"
              className={fieldClasses}
              value={timeRange}
              onChange={(e) => {
                setTimeRange(e.target.value as TimeRange)
                patch({ page: 1 })
              }}
            >
              {TIME_RANGES.map((r) => (
                <option key={r.id} value={r.id}>
                  {r.label}
                </option>
              ))}
            </select>
          </div>
          <div className="flex flex-col gap-1">
            <label htmlFor="f-number" className="text-micro font-medium text-text-muted">
              搜番号
            </label>
            <input
              id="f-number"
              type="search"
              className={`${fieldClasses} w-44 font-mono`}
              placeholder="SSIS-001"
              value={numberInput}
              onChange={(e) => setNumberInput(e.target.value)}
            />
          </div>
          <span className="grow" />
          {filtersActive ? (
            <Button size="sm" variant="quiet" onClick={clearFilters}>
              清空筛选
            </Button>
          ) : null}
        </div>
      </Card>

      {actionError ? <ErrorNotice error={actionError} title="操作失败" /> : null}
      {actionNote ? (
        <p role="status" className="rounded-md border border-ok-mark/40 bg-ok-tint px-3 py-2 text-cell text-ok-ink">
          {actionNote}
        </p>
      ) : null}

      <Card>
        <AsyncGate
          loading={list.loading && !list.data}
          error={list.error}
          onRetry={list.reload}
          skeleton={<Skeleton lines={8} />}
          empty={items.length === 0}
          emptyTitle="没有符合条件的任务"
          emptyHint={filtersActive ? '当前筛选下没有任务。放宽状态筛选，或清空搜索词。' : '还没有任务：去扫描页选目录建一批。'}
          emptyAction={
            filtersActive ? (
              <Button onClick={clearFilters}>清空筛选</Button>
            ) : (
              <Button variant="primary" onClick={() => navigate('/scan')}>
                去扫描
              </Button>
            )
          }
        >
          <div className="table-wrap">
            <table>
              <caption className="sr-only">任务列表：番号与标题、状态、阶段与来源、批次与触发、操作</caption>
              <thead>
                <tr>
                  <th scope="col">选择</th>
                  <th scope="col">海报</th>
                  <th scope="col">番号与标题</th>
                  <th scope="col">状态</th>
                  <th scope="col">执行阶段与来源</th>
                  <th scope="col">批次与触发</th>
                  <th scope="col">更新时间</th>
                  <th scope="col">快捷操作</th>
                </tr>
              </thead>
              <tbody>
                {items.map((task) => (
                  <TaskRow
                    key={task.id}
                    task={task}
                    checked={selected.has(task.id)}
                    onCheck={() =>
                      setSelected((prev) => {
                        const next = new Set(prev)
                        if (next.has(task.id)) next.delete(task.id)
                        else next.add(task.id)
                        return next
                      })
                    }
                    onRerun={() => void rerunOne(task)}
                  />
                ))}
              </tbody>
            </table>
          </div>

          {selected.size > 0 ? (
            <div className="sticky bottom-0 flex min-h-10 flex-wrap items-center gap-3 pointer-coarse:min-h-11 border-t border-line bg-surface/95 px-4 py-2 backdrop-blur">
              <p className="text-cell" aria-live="polite">
                已选 <b>{selected.size}</b> 个任务
                <span className="ml-2 text-text-muted">批量只做重跑，逐条返回结果</span>
              </p>
              <span className="grow" />
              <Button size="sm" variant="quiet" onClick={() => setSelected(new Set())}>
                清空选择
              </Button>
              <Button variant="primary" onClick={() => void rerunSelected()}>
                批量重跑
              </Button>
            </div>
          ) : null}

          {bulk ? (
            <div className="border-t border-line px-4 py-3">
              <p className="text-cell font-medium">批量重跑结果（逐条）</p>
              <ul className="mt-2 space-y-1">
                {bulk.map((item) => (
                  <li key={item.task_id} className="flex flex-wrap items-center gap-2 text-cell">
                    <Mono>#{item.task_id}</Mono>
                    {item.ok ? (
                      <span className="rounded-full border border-ok-mark/40 bg-ok-tint px-2 py-0.5 text-micro text-ok-ink">
                        已回到队列
                      </span>
                    ) : (
                      <span className="rounded-full border border-danger-mark/40 bg-danger-tint px-2 py-0.5 text-micro text-danger-ink">
                        {errorText(item.error ?? 'internal')}
                      </span>
                    )}
                  </li>
                ))}
              </ul>
            </div>
          ) : null}

          <Pagination
            page={page}
            pageCount={pageCount}
            total={total}
            perPage={perPage}
            onChange={(next) => patch({ page: next })}
            onPerPage={(n) => patch({ perPage: n, page: 1 })}
          />
        </AsyncGate>
      </Card>
    </div>
  )
}

function chipClasses(active: boolean): string {
  return [
    'inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-micro font-medium transition-colors',
    active ? 'border-accent bg-accent text-accent-ink' : 'border-line bg-surface text-text-muted hover:bg-surface-sunken',
  ].join(' ')
}

function TaskRow({
  task,
  checked,
  onCheck,
  onRerun,
}: {
  task: TaskView
  checked: boolean
  onCheck: () => void
  onRerun: () => void
}) {
  const [posterBroken, setPosterBroken] = useState(false)
  // A waiting task is rerun by submitting numbers, not by the rerun endpoint.
  const canRerun = !isLiveStatus(task.status)
  return (
    <>
      <tr>
        <td>
          <input type="checkbox" checked={checked} onChange={onCheck} aria-label={`选择任务 ${task.number || task.id}`} />
        </td>
        <td>
          <button
            type="button"
            className="relative block overflow-hidden rounded border border-line"
            style={{ width: 40, height: 60 }}
            onClick={() => navigate(`/tasks/${task.id}`)}
            title="✂ 封套右侧裁切 2:3"
            aria-label={`打开任务 ${task.number || task.id}`}
          >
            {posterBroken ? (
              <span className="flex h-full w-full items-center justify-center bg-surface-sunken text-micro text-text-subtle">
                {task.number ? task.number.slice(0, 4) : '—'}
              </span>
            ) : (
              <img
                src={`/api/tasks/${task.id}/thumb?role=poster`}
                alt=""
                width={40}
                height={60}
                className="poster h-full w-full"
                loading="lazy"
                onError={() => setPosterBroken(true)}
              />
            )}
            <span
              aria-hidden="true"
              className="absolute bottom-0 right-0 bg-surface/85 px-0.5 text-[10px] leading-tight text-text-muted"
            >
              ✂
            </span>
          </button>
        </td>
        <td className="max-w-72">
          <button type="button" className="text-left" onClick={() => navigate(`/tasks/${task.id}`)}>
            <span className="block font-mono text-cell font-semibold text-accent hover:underline">
              {task.number || '（无番号）'}
            </span>
            <span className="clamp-2 block text-cell text-text-muted">{task.title || '—'}</span>
          </button>
        </td>
        <td>
          <StatusPill status={task.status} />
        </td>
        <td className="text-cell">
          <span className="block">{task.stage ? stepLabel(task.stage) : '—'}</span>
          <span className="mt-0.5 flex flex-wrap gap-1 text-micro text-text-muted">
            {task.sources.length ? task.sources.map((s) => <Mono key={s}>{s}</Mono>) : <Muted>无源</Muted>}
            {task.missing.length ? (
              <span className="rounded-full border border-partial-mark/40 bg-partial-tint px-1.5 text-partial-ink">
                缺 {task.missing.length}
              </span>
            ) : null}
          </span>
        </td>
        <td className="text-cell">
          <button
            type="button"
            className="font-mono text-accent hover:underline"
            onClick={() => patchToBatch(task.batch_id)}
          >
            #{task.batch_id}
          </button>
          <span className="mt-0.5 block text-micro text-text-muted">
            {triggerLabel(task.trigger)} · {task.trigger_ref ? task.trigger_ref.split('/').pop() : '—'}
          </span>
        </td>
        <td className="whitespace-nowrap text-cell text-text-muted">{formatTimeShort(task.updated_at)}</td>
        <td>
          <span className="flex flex-wrap gap-1">
            <Button size="sm" onClick={() => navigate(`/tasks/${task.id}`)}>
              详情
            </Button>
            <Button size="sm" variant="quiet" onClick={onRerun} disabled={!canRerun}>
              重跑
            </Button>
          </span>
        </td>
      </tr>
      {task.last_error ? (
        <tr>
          <td colSpan={8} className="pt-0">
            <span
              className={[
                'inline-flex flex-wrap items-center gap-1 rounded-md border px-2 py-1 text-micro',
                task.status === 'needs_number'
                  ? 'border-number-mark/40 bg-number-tint text-number-ink'
                  : 'border-danger-mark/40 bg-danger-tint text-danger-ink',
              ].join(' ')}
            >
              {task.status === 'needs_number' ? taskErrorLabel(task.last_error) : task.last_error}
              {task.status === 'needs_number' ? (
                <button
                  type="button"
                  className="underline"
                  onClick={() => navigate(`/tasks/${task.id}`)}
                >
                  去填号
                </button>
              ) : null}
            </span>
          </td>
        </tr>
      ) : null}
    </>
  )
}

/** patchToBatch rebuilds the list URL around one batch — a batch is a filter. */
function patchToBatch(batchId: number) {
  const params = new URLSearchParams()
  params.set('batch', String(batchId))
  navigate(`/tasks?${params.toString()}`)
}

function Pagination({
  page,
  pageCount,
  total,
  perPage,
  onChange,
  onPerPage,
}: {
  page: number
  pageCount: number
  total: number
  perPage: number
  onChange: (page: number) => void
  onPerPage: (n: number) => void
}) {
  const from = total === 0 ? 0 : (page - 1) * perPage + 1
  const to = Math.min(total, page * perPage)
  const window = pageWindow(page, pageCount)
  return (
    <nav className="flex flex-wrap items-center gap-2 border-t border-line px-4 py-3" aria-label="分页">
      <span className="text-micro text-text-muted">
        显示 {from}–{to} / {total}
      </span>
      <span className="grow" />
      <Button size="sm" onClick={() => onChange(page - 1)} disabled={page <= 1}>
        上一页
      </Button>
      {window.map((p, i) =>
        p === 0 ? (
          <span key={`gap-${i}`} className="text-micro text-text-subtle">
            …
          </span>
        ) : (
          <button
            key={p}
            type="button"
            aria-current={p === page ? 'page' : undefined}
            onClick={() => onChange(p)}
            className={[
              'h-7 min-w-7 rounded-md border px-2 text-cell',
              p === page
                ? 'border-accent bg-accent text-accent-ink'
                : 'border-line bg-surface text-text-muted hover:bg-surface-sunken',
            ].join(' ')}
          >
            {p}
          </button>
        ),
      )}
      <Button size="sm" onClick={() => onChange(page + 1)} disabled={page >= pageCount}>
        下一页
      </Button>
      <label htmlFor="f-per-page" className="ml-2 text-micro text-text-muted">
        每页
      </label>
      <select
        id="f-per-page"
        className={fieldClasses}
        value={perPage}
        onChange={(e) => onPerPage(Number(e.target.value))}
      >
        {PER_PAGE.map((n) => (
          <option key={n} value={n}>
            {n}
          </option>
        ))}
      </select>
    </nav>
  )
}

/** pageWindow is the page list with 0 standing for an ellipsis. */
export function pageWindow(page: number, pageCount: number): number[] {
  if (pageCount <= 9) return Array.from({ length: pageCount }, (_, i) => i + 1)
  const out = new Set<number>([1, pageCount, page])
  if (page > 1) out.add(page - 1)
  if (page < pageCount) out.add(page + 1)
  const sorted = [...out].sort((a, b) => a - b)
  const withGaps: number[] = []
  for (let i = 0; i < sorted.length; i++) {
    if (i > 0 && sorted[i] - sorted[i - 1] > 1) withGaps.push(0)
    withGaps.push(sorted[i])
  }
  return withGaps
}
