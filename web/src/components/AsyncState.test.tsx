import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api/client'
import { AsyncGate } from './AsyncState'
import { StatusPill } from './StatusPill'

describe('AsyncGate', () => {
  it('shows the skeleton while loading, and neither empty nor error', () => {
    render(
      <AsyncGate loading error={undefined} empty skeleton={<div data-testid="custom-skeleton" />}>
        <p>content</p>
      </AsyncGate>,
    )
    expect(screen.getByTestId('custom-skeleton')).toBeInTheDocument()
    expect(screen.queryByText('content')).not.toBeInTheDocument()
  })

  it('renders a failure as role=alert with the Chinese sentence, the code, and a retry', async () => {
    const onRetry = vi.fn()
    render(
      <AsyncGate loading={false} error={new ApiError('root_not_browsable', 'outside', 400)} onRetry={onRetry}>
        <p>content</p>
      </AsyncGate>,
    )
    const alert = screen.getByRole('alert')
    expect(alert).toHaveTextContent('browse_roots')
    expect(alert).toHaveTextContent('root_not_browsable')
    expect(screen.queryByText('content')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '重试' }))
    expect(onRetry).toHaveBeenCalledTimes(1)
  })

  it('offers the empty state an exit action instead of a dead end', async () => {
    const action = vi.fn()
    render(
      <AsyncGate
        loading={false}
        empty
        emptyTitle="没有可进入候选的文件"
        emptyAction={
          <button type="button" onClick={action}>
            换一个目录
          </button>
        }
      >
        <p>content</p>
      </AsyncGate>,
    )
    expect(screen.getByText('没有可进入候选的文件')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '换一个目录' }))
    expect(action).toHaveBeenCalled()
  })

  it('renders its children once data is ready', () => {
    render(
      <AsyncGate loading={false} empty={false}>
        <p>content</p>
      </AsyncGate>,
    )
    expect(screen.getByText('content')).toBeInTheDocument()
  })
})

describe('StatusPill', () => {
  it('gives each status its label and its tint token', () => {
    const { container, rerender } = render(<StatusPill status="needs_number" />)
    expect(screen.getByText('待补号')).toBeInTheDocument()
    expect(container.firstElementChild).toHaveAttribute('data-tone', 'number')
    expect(container.firstElementChild?.className).toContain('bg-number-tint')

    rerender(<StatusPill status="failed" />)
    expect(screen.getByText('失败')).toBeInTheDocument()
    expect(container.firstElementChild?.className).toContain('bg-danger-tint')
  })

  it('still renders a status it has never seen', () => {
    render(<StatusPill status="brand_new" />)
    expect(screen.getByText('brand_new')).toBeInTheDocument()
  })
})
