import { statusMeta } from '../lib/labels'

/** StatusPill is the one place a status becomes its colour (seven of them). */
export function StatusPill({ status, className = '' }: { status: string; className?: string }) {
  const meta = statusMeta(status)
  return (
    <span
      data-tone={meta.tone}
      className={`inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-micro font-medium whitespace-nowrap ${meta.classes} ${className}`}
    >
      <span aria-hidden="true" className={`size-1.5 rounded-full ${meta.dot}`} />
      {meta.label}
    </span>
  )
}
