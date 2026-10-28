// Placeholder shell. The three views (scan preview, task list, task detail) land
// in their own ticket; what ships now proves the embed path end to end: the
// binary carries the built interface and /api/health answers from the same
// process.
export function App() {
  return (
    <main className="grid min-h-screen place-items-center bg-surface-sunken text-text">
      <section className="max-w-prose rounded-lg border border-line bg-surface p-8 shadow-sm">
        <h1 className="text-xl font-semibold">mdc-lite</h1>
        <p className="mt-2 text-text-muted">
          骨架就位：二进制起来了，SQLite 迁移跑完了，健康检查在{' '}
          <code className="font-mono">/api/health</code>。
        </p>
        <p className="mt-1 text-text-muted">
          界面三视图与 REST 端点随后续工单落地，这里暂时是占位页。
        </p>
      </section>
    </main>
  )
}
