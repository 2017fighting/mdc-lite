import { describe, expect, it } from 'vitest'
import {
  addExclude,
  addRoot,
  EMPTY_SCOPE,
  isStrictlyWithin,
  isWithin,
  normalizePath,
  removeExclude,
  removeRoot,
  scopeState,
  toggleExclude,
  toggleRoot,
} from './scope'

describe('normalizePath', () => {
  it('trims, absolutizes the tail slash and keeps the root slash', () => {
    expect(normalizePath('  /media/2024/ ')).toBe('/media/2024')
    expect(normalizePath('/media')).toBe('/media')
    expect(normalizePath('/')).toBe('/')
    expect(normalizePath('')).toBe('')
  })
})

describe('isWithin', () => {
  it('treats a path as inside itself and refuses a prefix that is only a name prefix', () => {
    expect(isWithin('/media', '/media')).toBe(true)
    expect(isWithin('/media/2024', '/media')).toBe(true)
    expect(isWithin('/media2', '/media')).toBe(false)
    expect(isStrictlyWithin('/media', '/media')).toBe(false)
    expect(isStrictlyWithin('/media/2024', '/media')).toBe(true)
  })
})

describe('the scan scope', () => {
  it('adds several non-overlapping roots: multi-root coexistence is the point', () => {
    let scope = addRoot(EMPTY_SCOPE, '/media/2024').scope
    scope = addRoot(scope, '/media/vr').scope
    expect(scope.roots).toEqual(['/media/2024', '/media/vr'])
  })

  it('refuses the same root twice and a nested one in either direction', () => {
    const scope = addRoot(EMPTY_SCOPE, '/media/2024').scope
    expect(addRoot(scope, '/media/2024').error).toMatch('扫描清单')
    expect(addRoot(scope, '/media').error).toMatch('嵌套')
    expect(addRoot(scope, '/media/2024/sub').error).toMatch('嵌套')
    expect(scope.roots).toEqual(['/media/2024'])
  })

  it('toggles a chosen directory out again', () => {
    const scope = addRoot(EMPTY_SCOPE, '/media').scope
    expect(toggleRoot(scope, '/media').scope.roots).toEqual([])
    expect(toggleRoot(EMPTY_SCOPE, '/media').scope.roots).toEqual(['/media'])
  })

  it('drops an exclusion when the root that contained it is removed', () => {
    let scope = addRoot(EMPTY_SCOPE, '/media/a').scope
    scope = addRoot(scope, '/media/b').scope
    scope = addExclude(scope, '/media/a/junk').scope
    scope = addExclude(scope, '/media/b/junk').scope
    const after = removeRoot(scope, '/media/a')
    expect(after.roots).toEqual(['/media/b'])
    expect(after.excludes).toEqual(['/media/b/junk'])
  })

  it('refuses an exclusion outside every root, at a root itself, or outside all roots', () => {
    const scope = addRoot(EMPTY_SCOPE, '/media').scope
    expect(addExclude(scope, '/media').error).toMatch('真子目录')
    expect(addExclude(scope, '/other/x').error).toMatch('真子目录')
    expect(addExclude(EMPTY_SCOPE, '/media/x').error).toMatch('真子目录')
    expect(addExclude(scope, '/media/junk').scope.excludes).toEqual(['/media/junk'])
  })

  it('toggles an exclusion off and reports the four-state of a path', () => {
    let scope = addRoot(EMPTY_SCOPE, '/media').scope
    expect(scopeState(scope, '/media')).toBe('root')
    expect(scopeState(scope, '/media/2024')).toBe('none')
    scope = toggleExclude(scope, '/media/2024').scope
    expect(scopeState(scope, '/media/2024')).toBe('exclude')
    scope = toggleExclude(scope, '/media/2024').scope
    expect(scope.excludes).toEqual([])
    expect(removeExclude(scope, '/media/2024').excludes).toEqual([])
  })
})
