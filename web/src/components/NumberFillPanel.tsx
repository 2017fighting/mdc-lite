import { useEffect, useMemo, useState } from 'react'
import { missingPaths as detailPaths } from '../api/client'
import type { NumberFill, TaskFileView } from '../api/types'
import { baseName } from '../lib/format'
import { roleLabel, taskErrorLabel } from '../lib/labels'
import { buildFills, draftFor, missingPaths, type NumberDraft } from '../lib/numbers'
import { ErrorNotice } from './AsyncState'
import { Button, fieldClasses, Mono } from './Primitives'

// The needs-number panel. It is one component used from two entrances — the 待补号
// page and the task detail — over one endpoint (`POST /api/tasks/{id}/numbers`), so
// the form state, the "all videos numbered" gate and the skip-confirmation exist
// once (spec §15).

export interface NumberFillPanelProps {
  files: TaskFileView[]
  /** `tasks.last_error` while waiting: no_candidate / ambiguous / duplicate_stem. */
  reason?: string
  onSubmit: (fills: NumberFill[]) => Promise<void>
  /** Skip is `POST /cancel`: it releases the paths. It asks first. */
  onSkip: () => Promise<void>
  className?: string
}

export function NumberFillPanel({ files, reason, onSubmit, onSkip, className = '' }: NumberFillPanelProps) {
  const seedKey = files.map((f) => `${f.path}\u0000${f.assigned_number}\u0000${f.is_part}`).join('\u0001')
  const [draft, setDraft] = useState<NumberDraft>(() => draftFor(files))
  const [busy, setBusy] = useState(false)
  const [failure, setFailure] = useState<unknown>(undefined)
  const [serverMissing, setServerMissing] = useState<string[]>([])
  const [confirmSkip, setConfirmSkip] = useState(false)

  // Re-seed when the server's rows change (a reload after a submit or another
  // operator's edit), but never on a poll that returned the same rows: that would
  // wipe what is being typed.
  useEffect(() => {
    setDraft(draftFor(files))
  }, [seedKey])

  const videos = useMemo(() => files.filter((f) => f.kind === 'video'), [files])
  const companions = useMemo(() => files.filter((f) => f.kind !== 'video'), [files])
  const missing = missingPaths(files, draft)
  const highlighted = new Set([...missing, ...serverMissing])

  function setNumber(path: string, number: string) {
    setDraft((prev) => ({ ...prev, [path]: { number, isPart: prev[path]?.isPart ?? false } }))
  }

  function setIsPart(path: string, isPart: boolean) {
    setDraft((prev) => ({ ...prev, [path]: { number: prev[path]?.number ?? '', isPart } }))
  }

  async function submit() {
    setBusy(true)
    setFailure(undefined)
    setServerMissing([])
    try {
      await onSubmit(buildFills(files, draft))
    } catch (err) {
      setFailure(err)
      // A 422 names the paths it is still missing; mark exactly those rows.
      setServerMissing(detailPaths(err))
    } finally {
      setBusy(false)
    }
  }

  async function skip() {
    setBusy(true)
    setFailure(undefined)
    try {
      await onSkip()
    } catch (err) {
      setFailure(err)
    } finally {
      setBusy(false)
      setConfirmSkip(false)
    }
  }

  return (
    <section
      className={`rounded-lg border border-number-mark/40 bg-number-tint/50 ${className}`}
      aria-label="待补号"
    >
      <div className="border-b border-number-mark/30 px-4 py-3">
        <h2 className="flex flex-wrap items-center gap-2 text-lead font-semibold text-number-ink">
          解析分歧，需要你填号
          {reason ? (
            <span className="rounded-full border border-number-mark/40 bg-surface px-2 py-0.5 text-micro">
              {taskErrorLabel(reason)}
            </span>
          ) : null}
        </h2>
        <p className="mt-1 text-cell text-text-muted">
          填完即提交并重跑：任务回到排队，新运行从 <Mono>parse</Mono> 起，不重扫目录。未填期间这些路径仍被占用。
        </p>
      </div>

      <div className="space-y-2 px-4 py-3">
        {videos.map((f) => {
          const entry = draft[f.path] ?? { number: '', isPart: false }
          const isMissing = highlighted.has(f.path)
          const inputId = `num-${f.id}`
          return (
            <div
              key={f.path}
              data-path={f.path}
              data-missing={isMissing ? 'true' : undefined}
              className={[
                'grid gap-2 rounded-md border bg-surface p-2 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center',
                isMissing ? 'border-danger-mark/50' : 'border-line',
              ].join(' ')}
            >
              <div className="min-w-0">
                <p className="truncate text-cell font-medium" title={f.path}>
                  <Mono>{baseName(f.path)}</Mono>
                  <span className="ml-2 rounded border border-line px-1 text-micro text-text-muted">
                    {roleLabel(f.role)}
                  </span>
                </p>
                <p className="truncate text-micro text-text-subtle" title={f.path}>
                  {f.path}
                </p>
              </div>
              <div className="flex flex-wrap items-center gap-2">
                {f.candidates.length ? (
                  <span className="flex flex-wrap gap-1" aria-label="候选番号">
                    {f.candidates.map((c) => (
                      <button
                        key={c}
                        type="button"
                        aria-pressed={entry.number === c}
                        onClick={() => setNumber(f.path, c)}
                        className={[
                          'rounded-full border px-2 py-0.5 font-mono text-micro',
                          entry.number === c
                            ? 'border-number-mark bg-number-mark text-white'
                            : 'border-line-strong bg-surface text-text hover:bg-surface-sunken',
                        ].join(' ')}
                      >
                        {c}
                      </button>
                    ))}
                  </span>
                ) : null}
                <label className="sr-only" htmlFor={inputId}>
                  {baseName(f.path)} 的番号
                </label>
                <input
                  id={inputId}
                  className={`${fieldClasses} w-40 font-mono`}
                  value={entry.number}
                  placeholder="或手填番号"
                  aria-invalid={isMissing}
                  onChange={(e) => setNumber(f.path, e.target.value)}
                />
                <label className="flex items-center gap-1.5 text-micro text-text-muted">
                  <input
                    type="checkbox"
                    checked={entry.isPart}
                    onChange={(e) => setIsPart(f.path, e.target.checked)}
                  />
                  同一作品的分片
                </label>
              </div>
            </div>
          )
        })}

        {companions.length ? (
          <p className="text-micro text-text-subtle">
            伴随文件不需要填号：
            {companions.map((f) => baseName(f.path)).join('、')}
          </p>
        ) : null}

        {missing.length ? (
          <p className="text-cell text-danger-ink">
            还有 {missing.length} 个视频没有号：{missing.map(baseName).join('、')}
          </p>
        ) : (
          <p className="text-cell text-text-muted">都填好了。任务号取主片那一行的号。</p>
        )}

        {failure ? <ErrorNotice error={failure} title="提交失败" /> : null}
      </div>

      <div className="flex flex-wrap items-center gap-2 border-t border-number-mark/30 px-4 py-3">
        <span className="grow" />
        {confirmSkip ? (
          <>
            <span className="text-cell text-danger-ink">跳过 = 取消任务并释放这些路径，确定？</span>
            <Button size="sm" variant="quiet" onClick={() => setConfirmSkip(false)} disabled={busy}>
              再想想
            </Button>
            <Button size="sm" variant="danger" onClick={() => void skip()} disabled={busy}>
              确认跳过
            </Button>
          </>
        ) : (
          <>
            <Button size="sm" variant="quiet" onClick={() => setConfirmSkip(true)} disabled={busy}>
              跳过（取消任务）
            </Button>
            <Button
              size="sm"
              variant="number"
              onClick={() => void submit()}
              disabled={busy || missing.length > 0}
            >
              提交并重跑
            </Button>
          </>
        )}
      </div>
    </section>
  )
}
