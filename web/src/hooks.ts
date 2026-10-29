import { useCallback, useEffect, useRef, useState } from 'react'

// One data-loading hook for the whole UI. It carries the four-state contract's
// "loading" only while there is nothing to show, so a poll never flashes a skeleton
// over data that is already on screen.

export interface AsyncState<T> {
  data?: T
  error?: unknown
  loading: boolean
  reload: () => void
}

export function useAsync<T>(
  load: () => Promise<T>,
  deps: unknown[],
  poll?: { ms: number; while: (data: T | undefined) => boolean },
): AsyncState<T> {
  const [state, setState] = useState<{ data?: T; error?: unknown; loading: boolean }>({ loading: true })
  const [nonce, setNonce] = useState(0)
  const loadRef = useRef(load)
  loadRef.current = load
  const pollRef = useRef(poll)
  pollRef.current = poll

  useEffect(() => {
    let cancelled = false
    let timer: number | undefined

    const run = async () => {
      setState((prev) => ({ data: prev.data, error: undefined, loading: prev.data === undefined }))
      try {
        const data = await loadRef.current()
        if (cancelled) return
        setState({ data, error: undefined, loading: false })
        const p = pollRef.current
        if (p && p.while(data)) timer = window.setTimeout(() => void run(), p.ms)
      } catch (err) {
        if (cancelled) return
        setState((prev) => ({ data: prev.data, error: err, loading: false }))
      }
    }

    void run()
    return () => {
      cancelled = true
      if (timer !== undefined) window.clearTimeout(timer)
    }
    // The caller's deps are the query: when one changes, the request changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, nonce])

  const reload = useCallback(() => setNonce((n) => n + 1), [])
  return { ...state, reload }
}

/** useDebounced delays a value so a search field does not fire per keystroke. */
export function useDebounced<T>(value: T, ms = 300): T {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    const timer = window.setTimeout(() => setDebounced(value), ms)
    return () => window.clearTimeout(timer)
  }, [value, ms])
  return debounced
}

/** isLive is true while a task can still change: the poll's own condition. */
export function isLiveStatus(status: string): boolean {
  return status === 'queued' || status === 'running'
}
