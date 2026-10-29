import type { ReactNode } from 'react'
import { errorCode, errorMessage } from '../lib/labels'
import { Button, Skeleton } from './Primitives'

// The four-state contract (spec §15): every view can be loading, empty, failed or
// normal, and the failure always offers a retry. `AsyncGate` is the common shape; a
// view whose states differ (the scan page's idle/running/failed ladder) composes the
// same `Skeleton`, `EmptyState` and `ErrorNotice` instead of inventing its own.

export interface AsyncGateProps {
  loading: boolean
  error?: unknown
  onRetry?: () => void
  /** `empty` is "loaded, but there is nothing to show" — not "not loaded yet". */
  empty?: boolean
  emptyTitle?: string
  emptyHint?: ReactNode
  emptyAction?: ReactNode
  skeleton?: ReactNode
  children: ReactNode
}

export function AsyncGate({
  loading,
  error,
  onRetry,
  empty,
  emptyTitle = '这里还没有内容',
  emptyHint,
  emptyAction,
  skeleton,
  children,
}: AsyncGateProps) {
  if (loading) return <>{skeleton ?? <Skeleton />}</>
  if (error) return <ErrorNotice error={error} onRetry={onRetry} />
  if (empty) return <EmptyState title={emptyTitle} hint={emptyHint} action={emptyAction} />
  return <>{children}</>
}

export function ErrorNotice({ error, onRetry, title }: { error: unknown; onRetry?: () => void; title?: string }) {
  const code = errorCode(error)
  return (
    <div
      role="alert"
      className="flex flex-wrap items-start gap-3 rounded-lg border border-danger-mark/40 bg-danger-tint p-4 text-danger-ink"
    >
      <div className="min-w-0 flex-1">
        <p className="font-semibold">{title ?? '加载失败'}</p>
        <p className="mt-1 text-cell">{errorMessage(error)}</p>
        {code ? <p className="mt-1 font-mono text-micro opacity-80">{code}</p> : null}
      </div>
      {onRetry ? (
        <Button size="sm" onClick={onRetry}>
          重试
        </Button>
      ) : null}
    </div>
  )
}

export function EmptyState({ title, hint, action }: { title: string; hint?: ReactNode; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-2 px-4 py-10 text-center">
      <p className="text-lead font-semibold">{title}</p>
      {hint ? <p className="max-w-prose text-cell text-text-muted">{hint}</p> : null}
      {action ? <div className="mt-3">{action}</div> : null}
    </div>
  )
}
