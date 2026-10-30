import { describe, expect, it } from 'vitest'
import { parseRoute, routeHref } from './router'

describe('parseRoute', () => {
  it('boots the scan view at the site root', () => {
    expect(parseRoute('/')).toEqual({ name: 'scan' })
    expect(parseRoute('/scan')).toEqual({ name: 'scan' })
  })

  it('reads the task list and one task by id', () => {
    expect(parseRoute('/tasks')).toEqual({ name: 'tasks' })
    expect(parseRoute('/tasks/12')).toEqual({ name: 'detail', id: 12 })
  })

  it('reads the needs-number page', () => {
    expect(parseRoute('/numbers')).toEqual({ name: 'numbers' })
  })

  it('does not invent a task for an id that is not a positive integer', () => {
    expect(parseRoute('/tasks/abc').name).toBe('unknown')
    expect(parseRoute('/tasks/0').name).toBe('unknown')
    expect(parseRoute('/tasks/12/13').name).toBe('unknown')
    expect(parseRoute('/nope').name).toBe('unknown')
  })
})

describe('routeHref', () => {
  it('keeps the query string a filter was carried in', () => {
    expect(routeHref('tasks', '?batch=3')).toBe('/tasks?batch=3')
    expect(routeHref('detail', '', 7)).toBe('/tasks/7')
  })
})
