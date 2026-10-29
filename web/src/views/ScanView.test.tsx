import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { PreviewResult } from '../api/types'
import { ScanView } from './ScanView'

const bootstrap = {
  path: '',
  label: '',
  roots: [{ path: '/media', label: '媒体库' }],
  dirs: [],
}

const listing = {
  path: '/media',
  label: '媒体库',
  roots: [{ path: '/media', label: '媒体库' }],
  dirs: [{ name: '2024', path: '/media/2024' }],
}

const preview: PreviewResult = {
  state: 'succeeded',
  walked_files: 3,
  matched: 2,
  groups: [
    {
      primary: '/media/2024/ABC-123.mp4',
      parts: ['/media/2024/ABC-123.mp4'],
      number: 'ABC-123',
      candidates: [],
      marker: '',
      dir: '/media/2024',
      size: 1024,
      occupied: false,
    },
    {
      primary: '/media/2024/AMB-1.mp4',
      parts: ['/media/2024/AMB-1-cd1.mp4', '/media/2024/AMB-1-cd2.mp4'],
      number: '',
      candidates: ['AMB-1', 'AMB-2'],
      marker: 'cd1',
      dir: '/media/2024',
      size: 2048,
      occupied: false,
    },
  ],
  filtered: [{ path: '/media/2024/notes.txt', rule: 'everything-else' }],
  filtered_total: 1,
}

function reply(body: unknown, status = 200) {
  return Promise.resolve(
    new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }),
  )
}

describe('ScanView', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('walks from the browse roots to a preview and creates a batch from the ticked groups', async () => {
    const createBodies: unknown[] = []
    vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.startsWith('/api/pipelines')) {
        return reply({ items: [{ id: 'main', steps: ['scan', 'filter', 'group', 'parse'] }], total: 1, page: 1, per_page: 1 })
      }
      if (url === '/api/fs/dirs?path=') return reply(bootstrap)
      if (url === '/api/fs/dirs?path=%2Fmedia') return reply(listing)
      if (url === '/api/scan/preview') return reply({ preview_id: 'p1' }, 202)
      if (url === '/api/scan/previews/p1') return reply(preview)
      if (url === '/api/tasks' && init?.method === 'POST') {
        createBodies.push(JSON.parse(String(init.body)))
        return reply({ batch_id: 7, created: 1, skipped: [] })
      }
      return reply({ error: { code: 'not_found', message: 'no such endpoint: ' + url } }, 404)
    })

    render(<ScanView />)

    // Bootstrap: the browse roots are the entry points, not invented presets.
    expect(await screen.findByRole('button', { name: '媒体库' })).toBeInTheDocument()
    // The row's own toggle adds the child directory (the current-dir button sits in
    // the path form and would add /media instead).
    const row = (await screen.findAllByRole('row'))[1]
    await userEvent.click(within(row).getByRole('button', { name: '+ 加入扫描' }))

    await userEvent.click(screen.getAllByRole('button', { name: '开始扫描' })[0])

    // The table shows the ambiguous group's candidate chips and the folded filtered list.
    expect(await screen.findByText('AMB-1')).toBeInTheDocument()
    expect(screen.getByText('ABC-123')).toBeInTheDocument()
    expect(screen.getByText(/被过滤的 1 个文件/)).toBeInTheDocument()
    expect(screen.getByText('/media/2024/notes.txt')).toBeInTheDocument()

    // Group selection is per group: ticking the checkbox counts both parts.
    await userEvent.click(screen.getByRole('checkbox', { name: /选择分组 AMB-1.mp4（2 个文件）/ }))
    expect(screen.getByText(/已选/)).toHaveTextContent('已选 1 组 / 2 个文件')

    await userEvent.click(screen.getByRole('button', { name: '建任务' }))
    await waitFor(() => expect(createBodies).toHaveLength(1))
    expect(createBodies[0]).toMatchObject({
      pipeline: 'main',
      roots: ['/media/2024'],
      exclude: [],
      groups: ['/media/2024/AMB-1.mp4'],
    })
    expect(await screen.findByText(/#7/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '查看这批任务' })).toBeInTheDocument()
  })
})
