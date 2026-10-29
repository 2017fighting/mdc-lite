import { describe, expect, it } from 'vitest'
import { emptyTaskQuery, taskQuery } from './query'

describe('taskQuery', () => {
  it('sends nothing but the page for the default filters', () => {
    expect(taskQuery(emptyTaskQuery())).toBe('?page=1&per_page=20')
  })

  it('repeats every selected status, because the API ORs them', () => {
    const q = taskQuery({ ...emptyTaskQuery(), statuses: ['failed', 'partial'] })
    const params = new URLSearchParams(q)
    expect(params.getAll('status')).toEqual(['failed', 'partial'])
  })

  it('trims and upper-cases the number search: only a number is ever searched', () => {
    const q = taskQuery({ ...emptyTaskQuery(), number: '  ssis-1 ' })
    expect(new URLSearchParams(q).get('number')).toBe('SSIS-1')
  })

  it('leaves absent filters out of the query instead of sending empty values', () => {
    const q = taskQuery({ ...emptyTaskQuery(), batchId: 0, trigger: '' })
    const params = new URLSearchParams(q)
    expect(params.has('batch')).toBe(false)
    expect(params.has('trigger')).toBe(false)
  })

  it('sends the batch, trigger and creation window when they are set', () => {
    const q = taskQuery({
      ...emptyTaskQuery(),
      batchId: 1043,
      trigger: 'manual',
      createdAfter: '2026-10-01T00:00:00.000Z',
    })
    const params = new URLSearchParams(q)
    expect(params.get('batch')).toBe('1043')
    expect(params.get('trigger')).toBe('manual')
    expect(params.get('created_after')).toBe('2026-10-01T00:00:00.000Z')
  })

  it('never sends a page below one', () => {
    expect(new URLSearchParams(taskQuery({ ...emptyTaskQuery(), page: 0 })).get('page')).toBe('1')
    expect(new URLSearchParams(taskQuery({ ...emptyTaskQuery(), page: -3 })).get('page')).toBe('1')
  })
})
