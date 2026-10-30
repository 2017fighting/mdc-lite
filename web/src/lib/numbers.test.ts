import { describe, expect, it } from 'vitest'
import type { TaskFileView } from '../api/types'
import { buildFills, draftFor, missingPaths } from './numbers'

function file(over: Partial<TaskFileView>): TaskFileView {
  return {
    id: 1,
    path: '/media/a.mp4',
    kind: 'video',
    role: 'primary',
    part_index: 1,
    follows_part: 0,
    size: 1,
    mtime: '',
    active: true,
    assigned_number: '',
    is_part: false,
    candidates: [],
    ...over,
  }
}

const files: TaskFileView[] = [
  file({ id: 1, path: '/media/AMB-1-cd1.mp4', candidates: ['AMB-1', 'AMB-2'] }),
  file({ id: 2, path: '/media/AMB-1-cd2.mp4', role: 'part', is_part: true }),
  file({ id: 3, path: '/media/AMB-1.srt', kind: 'companion', role: 'companion' }),
]

describe('draftFor', () => {
  it('seeds from what the row already holds and marks sibling parts by role', () => {
    const draft = draftFor(files)
    expect(draft['/media/AMB-1-cd1.mp4']).toEqual({ number: '', isPart: false })
    expect(draft['/media/AMB-1-cd2.mp4']).toEqual({ number: '', isPart: true })
  })
})

describe('missingPaths', () => {
  it('demands a number from every video and from no companion', () => {
    const draft = draftFor(files)
    expect(missingPaths(files, draft)).toEqual(['/media/AMB-1-cd1.mp4', '/media/AMB-1-cd2.mp4'])
    const filled = { ...draft, '/media/AMB-1-cd1.mp4': { number: ' amb-1 ', isPart: false } }
    expect(missingPaths(files, filled)).toEqual(['/media/AMB-1-cd2.mp4'])
  })
})

describe('buildFills', () => {
  it('trims the typed number and only submits video rows, one per path', () => {
    const draft = {
      '/media/AMB-1-cd1.mp4': { number: '  amb-1 ', isPart: false },
      '/media/AMB-1-cd2.mp4': { number: 'AMB-1', isPart: true },
      '/media/AMB-1.srt': { number: 'ignored', isPart: false },
    }
    expect(buildFills(files, draft)).toEqual([
      { path: '/media/AMB-1-cd1.mp4', assigned_number: 'amb-1', is_part: false },
      { path: '/media/AMB-1-cd2.mp4', assigned_number: 'AMB-1', is_part: true },
    ])
  })
})
