import { api } from './api/client'
import { EmptyState } from './components/AsyncState'
import { Button, Mono } from './components/Primitives'
import { useAsync } from './hooks'
import { navigate, parseRoute, useLocation } from './lib/router'
import { NumbersView } from './views/NumbersView'
import { ScanView } from './views/ScanView'
import { TaskDetailView } from './views/TaskDetailView'
import { TaskListView } from './views/TaskListView'

const NAV = [
  { name: 'scan' as const, href: '/scan', label: '扫描预览' },
  { name: 'tasks' as const, href: '/tasks', label: '任务' },
  { name: 'numbers' as const, href: '/numbers', label: '待补号' },
]

export function App() {
  const location = useLocation()
  const query = new URLSearchParams(location.split('?')[1] ?? '')
  const route = parseRoute(location.split('?')[0])
  // The sidebar's count is the queue size: one cheap request, refreshed on a slower
  // cadence than the list's own 3s poll.
  const waiting = useAsync(() => api.tasks('?status=needs_number&page=1&per_page=20'), [], {
    ms: 10000,
    while: () => true,
  })
  const waitingCount = waiting.data?.total ?? 0

  return (
    <div className="min-h-screen bg-canvas text-text">
      <header className="sticky top-0 z-30 border-b border-line bg-surface/95 backdrop-blur">
        <div className="mx-auto flex max-w-[1400px] flex-wrap items-center gap-4 px-4 py-3 lg:px-6">
          <button
            type="button"
            className="text-lead font-semibold tracking-tight"
            onClick={() => navigate('/scan')}
          >
            mdc<span className="text-accent">·</span>lite
          </button>
          <nav aria-label="视图" className="flex flex-wrap gap-1 lg:hidden">
            {NAV.map((item) => (
              <NavLink key={item.name} item={item} active={route.name === item.name || (item.name === 'tasks' && route.name === 'detail')} count={item.name === 'numbers' ? waitingCount : 0} />
            ))}
          </nav>
          <span className="grow" />
          <span className="hidden text-micro text-text-muted sm:block">
            单副本 · <Mono>SQLite</Mono> · 进度面 = 轮询
          </span>
        </div>
      </header>

      <div className="mx-auto flex max-w-[1400px] gap-6 px-4 py-5 lg:px-6">
        <aside className="hidden w-44 shrink-0 lg:block">
          <nav aria-label="视图" className="sticky top-20 flex flex-col gap-1">
            {NAV.map((item) => (
              <NavLink key={item.name} item={item} active={route.name === item.name || (item.name === 'tasks' && route.name === 'detail')} count={item.name === 'numbers' ? waitingCount : 0} />
            ))}
            <p className="mt-4 px-2 text-micro text-text-subtle">
              留痕查询只在详情页的四个 tab 里，没有单独的日志视图。
            </p>
          </nav>
        </aside>

        <main className="min-w-0 flex-1">
          {route.name === 'scan' ? <ScanView initialPath={query.get('path') ?? undefined} /> : null}
          {route.name === 'tasks' ? <TaskListView key={location} /> : null}
          {route.name === 'detail' && route.id ? <TaskDetailView key={route.id} id={route.id} /> : null}
          {route.name === 'numbers' ? <NumbersView /> : null}
          {route.name === 'unknown' ? (
            <EmptyState
              title="页面不存在"
              hint="这个地址没有对应的视图。三个视图是扫描预览、任务列表与待补号。"
              action={<Button onClick={() => navigate('/tasks')}>回任务列表</Button>}
            />
          ) : null}
        </main>
      </div>
    </div>
  )
}

function NavLink({
  item,
  active,
  count,
}: {
  item: (typeof NAV)[number]
  active: boolean
  count: number
}) {
  return (
    <button
      type="button"
      onClick={() => navigate(item.href)}
      aria-current={active ? 'page' : undefined}
      className={[
        'flex items-center justify-between gap-2 rounded-md px-3 py-2 text-cell font-medium transition-colors',
        active ? 'bg-accent-soft text-accent' : 'text-text-muted hover:bg-surface-sunken',
      ].join(' ')}
    >
      {item.label}
      {count > 0 ? (
        <span className="rounded-full bg-number-tint px-1.5 text-micro text-number-ink">{count}</span>
      ) : null}
    </button>
  )
}
