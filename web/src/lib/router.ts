import { useSyncExternalStore } from 'react'

// A twenty-line router instead of a dependency: three views and one detail page do
// not need a routing library, and the Go static handler already serves index.html
// for any deep link. Paths are plain (`/tasks/12`), so a copied URL works.

export type RouteName = 'scan' | 'tasks' | 'numbers' | 'detail' | 'unknown'

export interface Route {
  name: RouteName
  id?: number
}

export function parseRoute(pathname: string): Route {
  const parts = pathname.split('/').filter(Boolean)
  if (parts.length === 0) return { name: 'scan' }
  switch (parts[0]) {
    case 'scan':
      return parts.length === 1 ? { name: 'scan' } : { name: 'unknown' }
    case 'numbers':
      return parts.length === 1 ? { name: 'numbers' } : { name: 'unknown' }
    case 'tasks':
      if (parts.length === 1) return { name: 'tasks' }
      if (parts.length === 2) {
        const id = Number(parts[1])
        if (Number.isInteger(id) && id > 0) return { name: 'detail', id }
      }
      return { name: 'unknown' }
    default:
      return { name: 'unknown' }
  }
}

/** routeHref builds a link; `query` is passed through untouched (`?batch=3`). */
export function routeHref(name: RouteName, query = '', id?: number): string {
  const base = name === 'detail' ? `/tasks/${id}` : name === 'scan' ? '/scan' : `/${name}`
  return base + query
}

const NAVIGATE = 'mdc:navigate'

function subscribe(onChange: () => void): () => void {
  window.addEventListener('popstate', onChange)
  window.addEventListener(NAVIGATE, onChange)
  return () => {
    window.removeEventListener('popstate', onChange)
    window.removeEventListener(NAVIGATE, onChange)
  }
}

function snapshot(): string {
  return window.location.pathname + window.location.search
}

/** navigate pushes a path without reloading; a same-URL click is a no-op. */
export function navigate(to: string): void {
  if (to === snapshot()) return
  window.history.pushState({}, '', to)
  window.dispatchEvent(new Event(NAVIGATE))
}

/**
 * replaceLocation rewrites the address bar without announcing it: the list keeps its
 * filters refreshable (and shareable) without remounting on every keystroke.
 */
export function replaceLocation(to: string): void {
  if (to === snapshot()) return
  window.history.replaceState({}, '', to)
}

/** useLocation is the current path plus query, reactive to navigate and popstate. */
export function useLocation(): string {
  return useSyncExternalStore(subscribe, snapshot, () => '/')
}

/** useQuery reads the current query string; it re-renders with the location. */
export function useQuery(): URLSearchParams {
  const location = useLocation()
  return new URLSearchParams(location.split('?')[1] ?? '')
}

/** useRoute is the parsed current path. */
export function useRoute(): Route {
  const location = useLocation()
  return parseRoute(location.split('?')[0])
}
