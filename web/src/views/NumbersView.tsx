import { api } from '../api/client'
import type { TaskView } from '../api/types'
import { AsyncGate, EmptyState } from '../components/AsyncState'
import { NumberFillPanel } from '../components/NumberFillPanel'
import { Button, Card, Mono, SectionTitle, Skeleton } from '../components/Primitives'
import { StatusPill } from '../components/StatusPill'
import { useAsync } from '../hooks'
import { formatTimeShort } from '../lib/format'
import { navigate } from '../lib/router'
import { taskErrorLabel, triggerLabel } from '../lib/labels'

// The 待补号 page: the sidebar's queue. It reuses the detail page's panel — same
// component, same endpoint — so a number typed here and one typed there are the same
// submission (spec §15).

const NEEDS_NUMBER_QUERY = '?status=needs_number&page=1&per_page=100'

export function NumbersView() {
  const waiting = useAsync(() => api.tasks(NEEDS_NUMBER_QUERY), [])
  const items = waiting.data?.items ?? []
  const total = waiting.data?.total ?? 0

  return (
    <div className="space-y-4">
      <header className="flex flex-wrap items-end justify-between gap-2">
        <div>
          <h1 className="text-display font-semibold">待补号</h1>
          <p className="mt-1 text-cell text-text-muted">
            解析分歧、解析不出或主干重复的任务停在这里。填完号 → 任务回排队 → 新运行从 <Mono>parse</Mono> 起（不重扫目录）。
          </p>
        </div>
        <Button size="sm" onClick={waiting.reload}>
          刷新
        </Button>
      </header>

      <AsyncGate
        loading={waiting.loading && !waiting.data}
        error={waiting.error}
        onRetry={waiting.reload}
        skeleton={
          <Card>
            <Skeleton lines={6} />
          </Card>
        }
        empty={items.length === 0}
        emptyTitle="没有待补号的任务"
        emptyHint="解析唯一的组不会出现在这里。解析出多个候选、解析不出、或主干重复的任务才会停下来等人。"
        emptyAction={
          <Button onClick={() => navigate('/tasks')}>去任务列表</Button>
        }
      >
        {items.map((task) => (
          <WaitingTask key={task.id} task={task} onChanged={waiting.reload} />
        ))}
        {total > items.length ? (
          <p className="text-micro text-text-muted">
            只显示前 {items.length} 个（共 {total} 个）：其余请在任务列表里按「待补号」筛选。
          </p>
        ) : null}
      </AsyncGate>
    </div>
  )
}

function WaitingTask({ task, onChanged }: { task: TaskView; onChanged: () => void }) {
  return (
    <Card>
      <SectionTitle
        hint={
          <>
            <Mono>#{task.batch_id}</Mono> · {triggerLabel(task.trigger)} · 更新 {formatTimeShort(task.updated_at)}
          </>
        }
      >
        <span className="flex flex-wrap items-center gap-2">
          <Mono>{task.number || '（无番号）'}</Mono>
          <span className="text-cell font-normal text-text-muted">{task.title || '—'}</span>
          <StatusPill status={task.status} />
          {task.last_error ? (
            <span className="rounded-full border border-number-mark/40 bg-number-tint px-2 py-0.5 text-micro text-number-ink">
              {taskErrorLabel(task.last_error)}
            </span>
          ) : null}
        </span>
      </SectionTitle>
      {task.files?.length ? (
        <div className="p-4">
          <NumberFillPanel
            files={task.files}
            reason={task.last_error}
            onSubmit={async (fills) => {
              await api.fillNumbers(task.id, fills)
              onChanged()
            }}
            onSkip={async () => {
              await api.cancel(task.id)
              onChanged()
            }}
          />
        </div>
      ) : (
        <div className="p-4">
          <EmptyState title="这个任务没有可填的输入文件" hint="task_files 为空：入库时就没有文件行，先看详情页的落盘台账。" />
        </div>
      )}
    </Card>
  )
}
