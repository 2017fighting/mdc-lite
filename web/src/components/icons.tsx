// Inline SVG icons, authored here rather than pulled from a library (spec §15:
// no component library). They inherit `currentColor` and are decorative unless the
// caller gives them a name.

const COMMON = {
  width: 16,
  height: 16,
  viewBox: '0 0 24 24',
  fill: 'none',
  stroke: 'currentColor',
  strokeWidth: 1.8,
  strokeLinecap: 'round' as const,
  strokeLinejoin: 'round' as const,
  'aria-hidden': true,
}

export function VideoIcon({ className = '' }: { className?: string }) {
  return (
    <svg {...COMMON} className={className}>
      <rect x="3" y="5" width="18" height="14" rx="2" />
      <path d="M10 9.5v5l4-2.5z" />
    </svg>
  )
}

export function ImageIcon({ className = '' }: { className?: string }) {
  return (
    <svg {...COMMON} className={className}>
      <rect x="3" y="4" width="18" height="16" rx="2" />
      <circle cx="9" cy="10" r="1.6" />
      <path d="M4 18l5-5 4 4 3-3 4 4" />
    </svg>
  )
}

export function DocIcon({ className = '' }: { className?: string }) {
  return (
    <svg {...COMMON} className={className}>
      <path d="M6 3h8l4 4v14H6z" />
      <path d="M14 3v4h4M9 12h6M9 16h6" />
    </svg>
  )
}

export function FolderIcon({ className = '' }: { className?: string }) {
  return (
    <svg {...COMMON} className={className}>
      <path d="M3 6a1 1 0 011-1h5l2 2h8a1 1 0 011 1v10a1 1 0 01-1 1H4a1 1 0 01-1-1z" />
    </svg>
  )
}

export function CopyIcon({ className = '' }: { className?: string }) {
  return (
    <svg {...COMMON} className={className}>
      <rect x="9" y="9" width="11" height="11" rx="1.5" />
      <path d="M15 9V5a1 1 0 00-1-1H5a1 1 0 00-1 1v9a1 1 0 001 1h4" />
    </svg>
  )
}

export function RerunIcon({ className = '' }: { className?: string }) {
  return (
    <svg {...COMMON} className={className}>
      <path d="M20 11a8 8 0 10-2.6 5.9" />
      <path d="M20 5v6h-6" />
    </svg>
  )
}

export function CloseIcon({ className = '' }: { className?: string }) {
  return (
    <svg {...COMMON} className={className}>
      <path d="M6 6l12 12M18 6L6 18" />
    </svg>
  )
}

export function kindIcon(kind: string, className = '') {
  switch (kind) {
    case 'video':
      return <VideoIcon className={className} />
    case 'image':
      return <ImageIcon className={className} />
    default:
      return <DocIcon className={className} />
  }
}
