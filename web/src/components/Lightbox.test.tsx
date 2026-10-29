import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { Lightbox } from './Lightbox'

describe('Lightbox', () => {
  it('closes on Escape', async () => {
    const onClose = vi.fn()
    render(<Lightbox src="/api/tasks/1/image?role=poster&w=640" alt="poster" onClose={onClose} />)
    await userEvent.keyboard('{Escape}')
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('closes on a click anywhere, and shows the scaled endpoint it was given', async () => {
    const onClose = vi.fn()
    render(<Lightbox src="/api/tasks/1/image?role=fanart&idx=0&w=640" alt="fanart" onClose={onClose} />)
    expect(screen.getByRole('dialog', { name: '大图预览' })).toBeInTheDocument()
    expect(screen.getByRole('img')).toHaveAttribute('src', '/api/tasks/1/image?role=fanart&idx=0&w=640')
    await userEvent.click(screen.getByRole('dialog'))
    expect(onClose).toHaveBeenCalledTimes(1)
  })
})
