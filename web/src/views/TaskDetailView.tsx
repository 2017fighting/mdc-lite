import { useCallback, useState } from 'react'
import { api, imageSrc } from '../api/client'
import type {
  FileRowView,
  MetadataView,
  SourceRunView,
  StepView,
  TaskDetail,
  TaskFileView,
} from '../api/types'
import { AsyncGate, EmptyState, ErrorNotice } from '../components/AsyncState'
import { kindIcon, CopyIcon, RerunIcon } from '../components/icons'
import { Lightbox } from '../components/Lightbox'
import { NumberFillPanel } from '../components/NumberFillPanel'
import { Badge, Button, Card, Mono, Muted, SectionTitle, Skeleton } from '../components/Primitives'
import { StatusPill } from '../components/StatusPill'
import { isLiveStatus, useAsync } from '../hooks'
import { baseName, formatBytes, formatDuration, formatTime, shortPath } from '../lib/format'
import {
  actionLabel,
  kindLabel,
  roleLabel,
  sourceStatusLabel,
  reasonLabel,
  stepLabel,
  stepStatusLabel,
  triggerLabel,
} from '../lib/labels'
import { navigate } from '../lib/router'

// The task detail: one header, one image strip, four trace tabs and — when the task
// waits for a number — the same panel the 待补号 page shows. There is no log view:
// `step_runs` and `source_runs` are the queryable trace (spec §15).

const FIELDS: { field: keyof MetadataView; cn: string }[] = [
  { field: 'title', cn: '标题' },
  { field: 'plot', cn: '简介' },
  { field: 'fanart', cn: '背景图' },
  { field: 'extrafanarts', cn: '剧照' },
  { field: 'tags', cn: '标签' },
  { field: 'releasedate', cn: '发行日期' },
  { field: 'actors', cn: '演员' },
  { field: 'poster', cn: '海报' },
  { field: 'director', cn: '导演' },
  { field: 'runtime', cn: '时长' },
  { field: 'rating', cn: '评分' },
  { field: 'series', cn: '系列' },
  { field: 'studio', cn: '制作商' },
  { field: 'publisher', cn: '发行商' },
]

const TABS = [
  { id: 'fields', label: '字段与来源' },
  { id: 'steps', label: '步骤留痕' },
  { id: 'sources', label: '源留痕' },
  { id: 'files', label: '落盘台账' },
] as const

type TabID = (typeof TABS)[number]['id']

export function TaskDetailView({ id }: { id: number }) {
  const detail = useAsync(() => api.task(id), [id], {
    ms: 2000,
    while: (t) => (t ? isLiveStatus(t.status) : false),
  })
  const [tab, setTab] = useState<TabID>('fields')
  const [lightbox, setLightbox] = useState<{ src: string; alt: string } | null>(null)
  const [busy, setBusy] = useState('')
  const [actionError, setActionError] = useState<unknown>(undefined)
  const [note, setNote] = useState('')
  const [confirmCancel, setConfirmCancel] = useState(false)

  const closeLightbox = useCallback(() => setLightbox(null), [])

  async function run(label: string, fn: () => Promise<unknown>, after?: string) {
    setBusy(label)
    setActionError(undefined)
    setNote('')
    try {
      await fn()
      if (after) setNote(after)
      detail.reload()
    } catch (err) {
      setActionError(err)
    } finally {
      setBusy('')
      setConfirmCancel(false)
    }
  }

  async function copyPath(path: string) {
    if (!path) return
    try {
      if (!navigator.clipboard?.writeText) throw new Error('no clipboard')
      await navigator.clipboard.writeText(path)
      setNote('输出目录已复制：' + path)
    } catch {
      setNote('浏览器不给剪贴板权限，请手动选中路径：' + path)
    }
  }

  const task = detail.data
  const notFound = (detail.error as { code?: string } | undefined)?.code === 'not_found'

  return (
    <div className="space-y-4">
      <AsyncGate
        loading={detail.loading && !task}
        error={notFound ? undefined : detail.error}
        onRetry={detail.reload}
        skeleton={
          <Card>
            <Skeleton lines={6} />
          </Card>
        }
        empty={notFound}
        emptyTitle="任务不存在"
        emptyHint="这个 id 没有对应的任务：链接可能过期，或库被换过。"
        emptyAction={<Button onClick={() => navigate('/tasks')}>回任务列表</Button>}
      >
        {task ? (
          <>
            <header className="space-y-2">
              <div className="flex flex-wrap items-center gap-3">
                <h1 className="font-mono text-title font-semibold">{task.number || '（无番号）'}</h1>
                <StatusPill status={task.status} />
                <span className="text-cell text-text-muted">
                  批次 <Mono>#{task.batch_id}</Mono> · {triggerLabel(task.trigger)} · 更新{' '}
                  {formatTime(task.updated_at)} · 流水线 <Mono>{task.pipeline_id}</Mono>
                </span>
              </div>
              <p className="text-lead">{task.title || <Muted>还没有标题</Muted>}</p>
              <div className="flex flex-wrap items-center gap-2">
                <Button
                  variant="primary"
                  size="sm"
                  onClick={() => void run('rerun', () => api.rerun(task.id), '已回到队列：新运行从 parse 起跑')}
                  disabled={busy !== '' || isLiveStatus(task.status) || task.status === 'needs_number'}
                  title={
                    task.status === 'needs_number'
                      ? '先填号：待补号任务要提交番号才会重跑'
                      : '任务级后缀重跑：不重扫目录，从 parse 起'
                  }
                >
                  <RerunIcon />
                  从 parse 起重跑
                </Button>
                {confirmCancel ? (
                  <>
                    <span className="text-micro text-danger-ink">取消后任务终态，路径占用释放。确定？</span>
                    <Button size="sm" variant="quiet" onClick={() => setConfirmCancel(false)} disabled={busy !== ''}>
                      再想想
                    </Button>
                    <Button
                      size="sm"
                      variant="danger"
                      onClick={() => void run('cancel', () => api.cancel(task.id), '任务已取消')}
                      disabled={busy !== ''}
                    >
                      确认取消
                    </Button>
                  </>
                ) : (
                  <Button
                    size="sm"
                    variant="secondary"
                    onClick={() => setConfirmCancel(true)}
                    disabled={busy !== '' || !isLiveStatus(task.status)}
                  >
                    取消任务
                  </Button>
                )}
                <Button
                  size="sm"
                  variant="quiet"
                  disabled={!task.out_dir}
                  onClick={() => void copyPath(task.out_dir)}
                >
                  <CopyIcon />
                  复制路径
                </Button>
                <span className="text-micro text-text-muted">
                  输出目录 <Mono title={task.out_dir}>{shortPath(task.out_dir || '（未落盘）', 48)}</Mono>
                </span>
              </div>
              {task.partial ? (
                <p className="rounded-md border border-partial-mark/40 bg-partial-tint px-3 py-2 text-cell text-partial-ink">
                  部分成功：缺 {task.missing.length} 个源级字段（{task.missing.join(' / ') || '—'}）。
                  要更严就把它加进 output.with.required_fields。
                </p>
              ) : null}
              {actionError ? <ErrorNotice error={actionError} title="操作失败" /> : null}
              {note ? (
                <p role="status" className="text-cell text-ok-ink">
                  {note}
                </p>
              ) : null}
            </header>

            <ImageStrip task={task} onOpen={(src, alt) => setLightbox({ src, alt })} />

            {task.status === 'needs_number' ? (
              <NumberFillPanel
                files={task.task_files}
                reason={task.last_error}
                onSubmit={async (fills) => {
                  await api.fillNumbers(task.id, fills)
                  setNote('番号已提交：任务回到队列，新运行从 parse 起')
                  detail.reload()
                }}
                onSkip={async () => {
                  await api.cancel(task.id)
                  setNote('任务已取消，路径占用释放')
                  detail.reload()
                }}
              />
            ) : null}

            <Card>
              <div role="tablist" aria-label="任务详情分区" className="flex flex-wrap gap-1 border-b border-line px-2 py-2">
                {TABS.map((t) => (
                  <button
                    key={t.id}
                    role="tab"
                    type="button"
                    id={`tab-${t.id}`}
                    aria-selected={tab === t.id}
                    aria-controls={`panel-${t.id}`}
                    onClick={() => setTab(t.id)}
                    className={[
                      'rounded-md px-3 py-1.5 text-cell font-medium',
                      tab === t.id ? 'bg-accent text-accent-ink' : 'text-text-muted hover:bg-surface-sunken',
                    ].join(' ')}
                  >
                    {t.label}
                  </button>
                ))}
              </div>
              <div role="tabpanel" id={`panel-${tab}`} aria-labelledby={`tab-${tab}`} tabIndex={0}>
                {tab === 'fields' ? <FieldsTab task={task} /> : null}
                {tab === 'steps' ? <StepsTab steps={task.step_runs} onRefresh={detail.reload} /> : null}
                {tab === 'sources' ? <SourcesTab sources={task.source_runs} onRefresh={detail.reload} /> : null}
                {tab === 'files' ? <LedgerTab rows={task.file_rows} onRefresh={detail.reload} /> : null}
              </div>
            </Card>

            <MediaStrip files={task.task_files} />

            {lightbox ? <Lightbox src={lightbox.src} alt={lightbox.alt} onClose={closeLightbox} /> : null}
          </>
        ) : null}
      </AsyncGate>
    </div>
  )
}

function ImageStrip({ task, onOpen }: { task: TaskDetail; onOpen: (src: string, alt: string) => void }) {
  const poster = task.images.find((i) => i.role === 'poster')
  const fanarts = task.images.filter((i) => i.role === 'fanart')
  const extras = task.images.filter((i) => i.role === 'extrafanart')
  if (!task.images.length) {
    return (
      <Card>
        <SectionTitle hint="图片走 API 端点，不直引文件路径">图片</SectionTitle>
        <p className="px-4 py-4 text-cell text-text-muted">
          还没有图片产物：任务可能还没跑到 <Mono>images</Mono>。
        </p>
      </Card>
    )
  }
  return (
    <Card>
      <SectionTitle hint="点击放大 · Esc 关闭 · 不抽视频帧">图片</SectionTitle>
      <div className="flex flex-col gap-3 p-4 sm:flex-row">
        <figure className="w-40 shrink-0">
          {poster ? (
            <button type="button" className="relative block w-full" onClick={() => onOpen(imageSrc(poster.url, 640), 'poster')}>
              <img src={imageSrc(poster.url, 320)} alt="poster" className="poster w-full rounded-md border border-line" />
              <Badge className="absolute bottom-1 left-1 border-line bg-surface/90 text-text-muted" title="✂ 封套右侧裁切 2:3">
                ✂ 封套右侧裁切 2:3
              </Badge>
            </button>
          ) : (
            <div className="poster flex w-full items-center justify-center rounded-md border border-dashed border-line text-micro text-text-subtle">
              无 poster
            </div>
          )}
          <figcaption className="mt-1 text-micro text-text-muted">poster · 2:3 · ✂ 封套右侧裁切</figcaption>
        </figure>
        <div className="min-w-0 flex-1 space-y-2">
          {fanarts.map((img, i) => (
            <figure key={`fanart-${img.idx}`}>
              <button type="button" className="block w-full" onClick={() => onOpen(imageSrc(img.url, 640), 'fanart')}>
                <img
                  src={imageSrc(img.url, 320)}
                  alt={`fanart ${i + 1}`}
                  className="fanart w-full rounded-md border border-line"
                />
              </button>
              <figcaption className="mt-1 text-micro text-text-muted">
                fanart.jpg · 16:9 · 原版横版完整封套（含正反面）
              </figcaption>
            </figure>
          ))}
          {extras.length ? (
            <div>
              <p className="mb-1 text-micro text-text-muted">剧照 {extras.length} 张 · 16:9</p>
              <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
                {extras.map((img) => (
                  <button
                    key={`extra-${img.idx}`}
                    type="button"
                    onClick={() => onOpen(imageSrc(img.url, 640), `剧照 ${img.idx + 1}`)}
                  >
                    <img
                      src={imageSrc(img.url, 160)}
                      alt={`剧照 ${img.idx + 1}`}
                      className="fanart w-full rounded border border-line"
                    />
                  </button>
                ))}
              </div>
            </div>
          ) : null}
          {!fanarts.length && !extras.length ? (
            <p className="text-cell text-text-muted">只有海报，还没有背景图或剧照。</p>
          ) : null}
        </div>
      </div>
    </Card>
  )
}

function MediaStrip({ files }: { files: TaskFileView[] }) {
  return (
    <Card>
      <SectionTitle hint="V1 不抽帧、不探分辨率：只显示类型与大小">输入文件</SectionTitle>
      <ul className="divide-y divide-line">
        {files.map((f) => (
          <li key={f.path} className="flex flex-wrap items-center gap-3 px-4 py-2">
            <span className="text-text-subtle">{kindIcon(f.kind)}</span>
            <Mono className="min-w-0 flex-1 truncate text-cell" title={f.path}>
              {baseName(f.path)}
            </Mono>
            <Badge className="border-line bg-surface-sunken text-text-muted">{roleLabel(f.role)}</Badge>
            <span className="font-mono text-micro text-text-muted">{formatBytes(f.size)}</span>
          </li>
        ))}
        {!files.length ? <li className="px-4 py-3 text-cell text-text-muted">没有输入文件。</li> : null}
      </ul>
    </Card>
  )
}

function fieldValue(task: TaskDetail, field: keyof MetadataView): string {
  const md = task.metadata
  switch (field) {
    case 'fanart':
    case 'poster':
      return task.images.some((i) => i.role === field) ? '已落盘 · 见上方图片' : ''
    case 'actors':
      return md.actors.map((a) => a.name ?? '').filter(Boolean).join('、')
    case 'director':
      return md.director.join('、')
    case 'tags':
      return md.tags.join('、')
    case 'extrafanarts':
      return md.extrafanarts.length ? `${md.extrafanarts.length} 张` : ''
    case 'runtime':
      return md.runtime ? `${md.runtime} 分钟` : ''
    case 'rating':
      return md.rating ? String(md.rating) : ''
    default: {
      const value = md[field]
      return typeof value === 'string' ? value : ''
    }
  }
}

function FieldsTab({ task }: { task: TaskDetail }) {
  const missing = new Set(task.metadata.missing)
  return (
    <div className="table-wrap">
      <table>
        <caption className="sr-only">14 个源级字段、值与胜出源</caption>
        <thead>
          <tr>
            <th scope="col">字段</th>
            <th scope="col">值</th>
            <th scope="col">来源</th>
          </tr>
        </thead>
        <tbody>
          <tr>
            <th scope="row" className="bg-surface text-left font-mono text-micro">
              number
            </th>
            <td>
              <Mono className="font-semibold">{task.number || '—'}</Mono>
            </td>
            <td className="text-micro text-text-muted">parse</td>
          </tr>
          {FIELDS.map(({ field, cn }) => {
            const isMissing = missing.has(field)
            const source = task.metadata.sources[field] ?? ''
            const value = fieldValue(task, field)
            return (
              <tr key={field} className={isMissing ? 'bg-danger-tint/40' : undefined}>
                <th scope="row" className="bg-surface text-left">
                  <Mono className="text-micro">{field}</Mono>
                  <span className="ml-1 text-micro text-text-muted">{cn}</span>
                </th>
                <td className={isMissing ? 'text-danger-ink' : 'text-cell'}>
                  {isMissing ? (
                    <span className="inline-flex items-center gap-1 font-medium">
                      <span aria-hidden="true" className="size-1.5 rounded-full bg-danger-mark" />
                      Missing
                    </span>
                  ) : value ? (
                    <span className="clamp-2">{value}</span>
                  ) : (
                    <Muted>—</Muted>
                  )}
                </td>
                <td className="font-mono text-micro text-text-muted">{source || '—'}</td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

function StepsTab({ steps, onRefresh }: { steps: StepView[]; onRefresh: () => void }) {
  if (!steps.length) {
    return (
      <EmptyState
        title="还没有步骤留痕"
        hint="任务可能还没开始跑，或使用的是不含步骤的流水线。"
        action={<Button size="sm" onClick={onRefresh}>刷新</Button>}
      />
    )
  }
  return (
    <div className="table-wrap">
      <table>
        <caption className="sr-only">步骤留痕：每步的状态、耗时与摘要</caption>
        <thead>
          <tr>
            <th scope="col">#</th>
            <th scope="col">步骤</th>
            <th scope="col">状态</th>
            <th scope="col">耗时</th>
            <th scope="col">摘要</th>
          </tr>
        </thead>
        <tbody>
          {steps.map((step) => (
            <tr key={step.id}>
              <td className="font-mono text-micro text-text-muted">{step.idx}</td>
              <td className="text-cell">
                {stepLabel(step.type)}
                <span className="ml-1 font-mono text-micro text-text-muted">{step.type}</span>
                {step.name ? <span className="ml-1 text-micro text-text-subtle">{step.name}</span> : null}
              </td>
              <td className="text-cell">{stepStatusLabel(step.status)}</td>
              <td className="font-mono text-micro">{formatDuration(step.ms)}</td>
              <td className="max-w-lg">
                <JsonCell value={step.summary} />
                {step.error ? <p className="mt-1 text-micro text-danger-ink">{step.error}</p> : null}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function SourcesTab({ sources, onRefresh }: { sources: SourceRunView[]; onRefresh: () => void }) {
  if (!sources.length) {
    return (
      <EmptyState
        title="还没有源留痕"
        hint="任务可能还没跑到 scrape（例如在等补号），或流水线里没有源。"
        action={<Button size="sm" onClick={onRefresh}>刷新</Button>}
      />
    )
  }
  return (
    <div className="table-wrap">
      <table>
        <caption className="sr-only">源留痕：每个刮削源的三态、原因码与贡献字段</caption>
        <thead>
          <tr>
            <th scope="col">源</th>
            <th scope="col">状态</th>
            <th scope="col">原因码</th>
            <th scope="col">贡献字段</th>
            <th scope="col">耗时</th>
          </tr>
        </thead>
        <tbody>
          {sources.map((s) => (
            <tr key={s.id}>
              <td>
                <Mono>{s.source_id}</Mono>
              </td>
              <td>
                <span
                  className={[
                    'rounded-full border px-2 py-0.5 text-micro',
                    s.status === 'ok'
                      ? 'border-ok-mark/40 bg-ok-tint text-ok-ink'
                      : s.status === 'failed'
                        ? 'border-danger-mark/40 bg-danger-tint text-danger-ink'
                        : 'border-idle-mark/40 bg-idle-tint text-idle-ink',
                  ].join(' ')}
                >
                  {sourceStatusLabel(s.status)}
                </span>
              </td>
              <td className="text-cell">
                {s.reason ? (
                  <span className="text-danger-ink" title={s.reason}>
                    {reasonLabel(s.reason)}
                    <span className="ml-1 font-mono text-micro text-text-subtle">{s.reason}</span>
                  </span>
                ) : (
                  <Muted>—</Muted>
                )}
              </td>
              <td className="max-w-xs">
                <JsonCell value={fieldsOf(s.fields)} />
                {s.err ? <p className="mt-1 text-micro text-danger-ink">{s.err}</p> : null}
              </td>
              <td className="font-mono text-micro">{formatDuration(s.ms)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function fieldsOf(value: unknown): unknown {
  if (typeof value !== 'object' || value === null) return value
  return Object.keys(value as Record<string, unknown>)
}

function LedgerTab({ rows, onRefresh }: { rows: FileRowView[]; onRefresh: () => void }) {
  if (!rows.length) {
    return (
      <EmptyState
        title="任务还没有落盘"
        hint="output 是唯一写盘出口：失败在它之前就不会留下台账。"
        action={<Button size="sm" onClick={onRefresh}>刷新</Button>}
      />
    )
  }
  return (
    <div className="table-wrap">
      <table>
        <caption className="sr-only">落盘台账：产物、来源、目标与动作</caption>
        <thead>
          <tr>
            <th scope="col">kind</th>
            <th scope="col">role</th>
            <th scope="col">idx</th>
            <th scope="col">源</th>
            <th scope="col">目标</th>
            <th scope="col">大小</th>
            <th scope="col">动作</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={row.id}>
              <td className="text-cell">{kindLabel(row.kind)}</td>
              <td className="text-cell">{roleLabel(row.role)}</td>
              <td className="font-mono text-micro">{row.idx}</td>
              <td className="truncate-cell font-mono text-micro text-text-muted" title={row.src}>
                {row.src || '—'}
              </td>
              <td className="truncate-cell font-mono text-micro" title={row.dst}>
                {row.dst}
              </td>
              <td className="font-mono text-micro">{formatBytes(row.bytes)}</td>
              <td className="text-cell">{actionLabel(row.action)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

/** JsonCell shows a stored JSON column compactly, expanded on demand. */
function JsonCell({ value }: { value: unknown }) {
  if (value === undefined || value === null) return <Muted>—</Muted>
  const text = JSON.stringify(value)
  if (text === '{}' || text === 'null') return <Muted>—</Muted>
  const compact = text.length > 90 ? text.slice(0, 90) + '…' : text
  return (
    <details>
      <summary className="cursor-pointer font-mono text-micro text-text-muted" title={text}>
        {compact}
      </summary>
      <pre className="mt-1 max-h-48 overflow-auto rounded bg-surface-sunken p-2 font-mono text-micro">
        {JSON.stringify(value, null, 2)}
      </pre>
    </details>
  )
}
