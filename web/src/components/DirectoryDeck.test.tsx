import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { BrowseResult } from '../api/types'
import { DirectoryDeck, mountLabel } from './DirectoryDeck'

const browse: BrowseResult = {
  path: '/media',
  label: '媒体库',
  roots: [
    { path: '/media', label: '媒体库' },
    { path: '/media/vr', label: 'VR 专区' },
  ],
  dirs: [
    { name: '2024', path: '/media/2024' },
    { name: 'vr', path: '/media/vr', entries: 3, videos: 2 },
  ],
}

function deck(over: Partial<React.ComponentProps<typeof DirectoryDeck>> = {}) {
  const props = {
    path: '/media',
    browse,
    scope: { roots: ['/media/2024'], excludes: ['/media/vr'] },
    stats: false,
    onNavigate: vi.fn(),
    onToggleRoot: vi.fn(),
    onToggleExclude: vi.fn(),
    onToggleStats: vi.fn(),
    ...over,
  }
  render(<DirectoryDeck {...props} />)
  return props
}

describe('mountLabel', () => {
  it('names the innermost root that contains the path', () => {
    expect(mountLabel('/media/2024', browse.roots)).toBe('媒体库')
    expect(mountLabel('/media/vr/2024', browse.roots)).toBe('VR 专区')
    expect(mountLabel('/elsewhere', browse.roots)).toBe('')
  })
})

describe('DirectoryDeck', () => {
  it('offers one entry point per browse root and a breadcrumb that navigates', async () => {
    const props = deck({ path: '/media/2024' })
    const nav = screen.getByRole('navigation', { name: '当前路径' })
    // The entry points are the roots themselves: no preset chips exist.
    expect(screen.getByRole('button', { name: '媒体库' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'VR 专区' })).toBeInTheDocument()
    await userEvent.click(within(nav).getByRole('button', { name: '2024' }))
    expect(props.onNavigate).toHaveBeenCalledWith('/media/2024')
  })

  it('jumps to a typed container path immediately', async () => {
    const props = deck()
    const input = screen.getByLabelText('绝对路径')
    await userEvent.clear(input)
    await userEvent.type(input, '/media/vr{Enter}')
    expect(props.onNavigate).toHaveBeenCalledWith('/media/vr')
  })

  it('renders the four-state toggles from the current scope', () => {
    deck()
    const rows = screen.getAllByRole('row')
    // 2024 is a chosen root, vr is excluded: the two rows show opposite pairs.
    expect(within(rows[1]).getByRole('button', { name: '✓ 移出扫描' })).toBeInTheDocument()
    expect(within(rows[1]).getByRole('button', { name: '+ 排除' })).toBeInTheDocument()
    expect(within(rows[2]).getByRole('button', { name: '+ 加入扫描' })).toBeInTheDocument()
    expect(within(rows[2]).getByRole('button', { name: '取消排除' })).toBeInTheDocument()
  })

  it('calls the toggle for the row that was pressed', async () => {
    const props = deck({ scope: { roots: ['/media'], excludes: ['/media/2024'] } })
    const rows = screen.getAllByRole('row')
    await userEvent.click(within(rows[1]).getByRole('button', { name: '取消排除' }))
    expect(props.onToggleExclude).toHaveBeenCalledWith('/media/2024')
    await userEvent.click(within(rows[2]).getByRole('button', { name: '+ 排除' }))
    expect(props.onToggleExclude).toHaveBeenCalledWith('/media/vr')
    await userEvent.click(within(rows[2]).getByRole('button', { name: '+ 加入扫描' }))
    expect(props.onToggleRoot).toHaveBeenCalledWith('/media/vr')
  })

  it('only shows child statistics when they were asked for', async () => {
    const props = deck()
    // The API omits the counts unless `?stats=1` was asked for, so the cell says so.
    expect(screen.getByText('按需请求')).toBeInTheDocument()
    expect(screen.getByText('3 项 · 2 个视频')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '统计子目录内容' }))
    expect(props.onToggleStats).toHaveBeenCalled()
  })

  it('shows the two-column scope ledger with the counts and the protecting whitelist', () => {
    deck()
    expect(screen.getByText('待扫描目录清单')).toBeInTheDocument()
    expect(screen.getByText('排除过滤清单（整树跳过）')).toBeInTheDocument()
    expect(screen.getByText(/共 1 个扫描目录 · 1 条排除/)).toBeInTheDocument()
    expect(screen.getByText(/白名单（\/media、\/media\/vr）/)).toBeInTheDocument()
  })

  it('puts a refused toggle beside the toggles, as an alert', () => {
    deck({ notice: '排除必须是某个扫描目录的真子目录' })
    expect(screen.getByRole('alert')).toHaveTextContent('真子目录')
  })
})
