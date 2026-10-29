import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api/client'
import type { TaskFileView } from '../api/types'
import { NumberFillPanel } from './NumberFillPanel'

function file(over: Partial<TaskFileView>): TaskFileView {
  return {
    id: 1,
    path: '/media/AMB-1-cd1.mp4',
    kind: 'video',
    role: 'primary',
    part_index: 1,
    follows_part: 0,
    size: 1,
    mtime: '',
    active: true,
    assigned_number: '',
    is_part: false,
    candidates: ['AMB-1', 'AMB-2'],
    ...over,
  }
}

const files: TaskFileView[] = [
  file({}),
  file({ id: 2, path: '/media/AMB-1-cd2.mp4', role: 'part', is_part: true }),
  file({ id: 3, path: '/media/AMB-1.srt', kind: 'companion', role: 'companion' }),
]

describe('NumberFillPanel', () => {
  it('renders one number input per video, none per companion, and a chip fills the field', async () => {
    render(<NumberFillPanel files={files} onSubmit={vi.fn()} onSkip={vi.fn()} />)
    expect(screen.getAllByPlaceholderText('或手填番号')).toHaveLength(2)
    expect(screen.getByText(/伴随文件不需要填号/)).toBeInTheDocument()

    const chip = screen.getAllByRole('button', { name: 'AMB-1' })[0]
    await userEvent.click(chip)
    expect(chip).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getAllByPlaceholderText('或手填番号')[0]).toHaveValue('AMB-1')
  })

  it('cannot submit until every video has a number, then sends trimmed fills', async () => {
    const onSubmit = vi.fn().mockResolvedValue(undefined)
    render(<NumberFillPanel files={files} onSubmit={onSubmit} onSkip={vi.fn()} />)
    const submit = screen.getByRole('button', { name: '提交并重跑' })
    expect(submit).toBeDisabled()
    expect(screen.getByText(/还有 2 个视频没有号/)).toBeInTheDocument()

    const inputs = screen.getAllByPlaceholderText('或手填番号')
    await userEvent.type(inputs[0], ' amb-1 ')
    await userEvent.type(inputs[1], 'AMB-1')
    await userEvent.click(screen.getAllByLabelText('同一作品的分片')[1])
    expect(submit).toBeEnabled()

    await userEvent.click(submit)
    expect(onSubmit).toHaveBeenCalledWith([
      { path: '/media/AMB-1-cd1.mp4', assigned_number: 'amb-1', is_part: false },
      { path: '/media/AMB-1-cd2.mp4', assigned_number: 'AMB-1', is_part: false },
    ])
  })

  it('marks the rows a 422 says are still missing, and explains the failure', async () => {
    const onSubmit = vi
      .fn()
      .mockRejectedValue(
        new ApiError('numbers_incomplete', 'every video needs a number', 422, {
          missing_paths: ['/media/AMB-1-cd2.mp4'],
        }),
      )
    render(<NumberFillPanel files={files} onSubmit={onSubmit} onSkip={vi.fn()} />)
    const inputs = screen.getAllByPlaceholderText('或手填番号')
    await userEvent.type(inputs[0], 'AMB-1')
    await userEvent.type(inputs[1], 'AMB-1')
    await userEvent.click(screen.getByRole('button', { name: '提交并重跑' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('还有视频没填番号')
    const row = document.querySelector('[data-path="/media/AMB-1-cd2.mp4"]')
    expect(row).toHaveAttribute('data-missing', 'true')
  })

  it('asks before skipping, because skipping cancels the task and frees the paths', async () => {
    const onSkip = vi.fn().mockResolvedValue(undefined)
    render(<NumberFillPanel files={files} onSubmit={vi.fn()} onSkip={onSkip} />)
    await userEvent.click(screen.getByRole('button', { name: '跳过（取消任务）' }))
    expect(onSkip).not.toHaveBeenCalled()
    expect(screen.getByText(/跳过 = 取消任务/)).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '确认跳过' }))
    expect(onSkip).toHaveBeenCalledTimes(1)
  })

  it('shows the reason the task is waiting when the API knows it', () => {
    render(<NumberFillPanel files={files} reason="duplicate_stem" onSubmit={vi.fn()} onSkip={vi.fn()} />)
    const panel = screen.getByRole('region', { name: '待补号' })
    expect(within(panel).getByText('主干重复，需人判定')).toBeInTheDocument()
  })
})
