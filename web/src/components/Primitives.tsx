import type { ButtonHTMLAttributes, ReactNode } from 'react'

// Small, unopinionated primitives. There is no component library (spec §15): a
// button, a card, a badge and a skeleton are all three views need, and each one
// reads its colours from the `@theme` tokens.

type Variant = 'primary' | 'secondary' | 'quiet' | 'danger' | 'number'

const VARIANTS: Record<Variant, string> = {
  primary: 'bg-accent text-accent-ink border border-transparent hover:brightness-110',
  secondary: 'bg-surface text-text border border-line-strong hover:bg-surface-sunken',
  quiet: 'bg-transparent text-text-muted border border-transparent hover:bg-surface-sunken',
  danger: 'bg-danger-mark text-white border border-transparent hover:brightness-110',
  number: 'bg-number-mark text-white border border-transparent hover:brightness-110',
}

type Size = 'sm' | 'md'

const SIZES: Record<Size, string> = {
  sm: 'h-7 px-2 text-micro gap-1',
  md: 'h-9 px-3 text-cell gap-1.5',
}

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: Variant
  size?: Size
}

export function Button({ variant = 'secondary', size = 'md', className = '', type = 'button', ...rest }: ButtonProps) {
  return (
    <button
      type={type}
      className={[
        'inline-flex items-center justify-center rounded-md font-medium transition-colors',
        'disabled:cursor-not-allowed disabled:opacity-50',
        'pointer-coarse:min-h-11',
        VARIANTS[variant],
        SIZES[size],
        className,
      ].join(' ')}
      {...rest}
    />
  )
}

export function Card({ children, className = '' }: { children: ReactNode; className?: string }) {
  return <section className={`rounded-lg border border-line bg-surface ${className}`}>{children}</section>
}

export function SectionTitle({ children, hint }: { children: ReactNode; hint?: ReactNode }) {
  return (
    <div className="flex flex-wrap items-baseline gap-2 border-b border-line px-4 py-3">
      <h2 className="text-lead font-semibold">{children}</h2>
      {hint ? <span className="text-micro text-text-muted">{hint}</span> : null}
    </div>
  )
}

export function Badge({ children, className = '', title }: { children: ReactNode; className?: string; title?: string }) {
  return (
    <span
      title={title}
      className={`inline-flex items-center rounded-full border px-2 py-0.5 text-micro font-medium ${className}`}
    >
      {children}
    </span>
  )
}

export function Mono({ children, className = '', title }: { children: ReactNode; className?: string; title?: string }) {
  return (
    <code title={title} className={`font-mono text-[0.95em] ${className}`}>
      {children}
    </code>
  )
}

export function Muted({ children }: { children: ReactNode }) {
  return <span className="text-text-muted">{children}</span>
}

/** Skeleton is the loading half of the four-state contract. */
export function Skeleton({ lines = 4 }: { lines?: number }) {
  return (
    <div className="space-y-2 p-4" aria-hidden="true" data-testid="skeleton">
      {Array.from({ length: lines }, (_, i) => (
        <div key={i} className="skeleton h-4 rounded" style={{ width: `${90 - (i % 3) * 15}%` }} />
      ))}
    </div>
  )
}

export function Label({ htmlFor, children }: { htmlFor?: string; children: ReactNode }) {
  return (
    <label htmlFor={htmlFor} className="text-micro font-medium text-text-muted">
      {children}
    </label>
  )
}

/** fieldClasses is shared by the deck's inputs and the list's filters. */
export const fieldClasses =
  'h-8 rounded-md border border-line-strong bg-surface px-2 text-cell text-text placeholder:text-text-subtle pointer-coarse:min-h-11'
