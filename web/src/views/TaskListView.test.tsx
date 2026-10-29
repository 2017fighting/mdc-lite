import { describe, expect, it } from 'vitest'
import { pageWindow } from './TaskListView'

describe('pageWindow', () => {
  it('lists every page when they fit', () => {
    expect(pageWindow(1, 1)).toEqual([1])
    expect(pageWindow(3, 9)).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9])
  })

  it('keeps the first, the last and the neighbours, with 0 standing for an ellipsis', () => {
    expect(pageWindow(1, 20)).toEqual([1, 2, 0, 20])
    expect(pageWindow(10, 20)).toEqual([1, 0, 9, 10, 11, 0, 20])
    expect(pageWindow(20, 20)).toEqual([1, 0, 19, 20])
  })
})
