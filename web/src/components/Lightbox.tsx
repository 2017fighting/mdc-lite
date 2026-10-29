import { useEffect } from 'react'
import { CloseIcon } from './icons'

/**
 * Lightbox is the image viewer: Esc or a click anywhere closes it. It renders the
 * scaled endpoint (`w=640`), never a file path — the bytes only ever come from
 * `GET /api/tasks/{id}/image`.
 */
export function Lightbox({ src, alt, onClose }: { src: string; alt: string; onClose: () => void }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-label="大图预览"
      className="fixed inset-0 z-50 flex flex-col items-center justify-center gap-3 bg-black/80 p-4"
      onClick={onClose}
    >
      <img src={src} alt={alt} className="max-h-[85vh] max-w-[92vw] rounded-md object-contain" />
      <div className="flex items-center gap-3 text-micro text-white/80">
        <span>{alt} · 点击任意处或按 Esc 关闭</span>
        <button
          type="button"
          className="inline-flex items-center gap-1 rounded-md border border-white/30 px-2 py-1 text-white hover:bg-white/10"
          onClick={onClose}
        >
          <CloseIcon />
          关闭
        </button>
      </div>
    </div>
  )
}
