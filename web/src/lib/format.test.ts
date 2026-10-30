import { describe, expect, it } from 'vitest'
import { baseName, clamp, formatBytes, formatDuration, formatTime, formatTimeShort, shortPath } from './format'

describe('formatBytes', () => {
  it('shows bytes below 1 KiB as a plain number', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(1023)).toBe('1023 B')
  })

  it('switches to binary units at 1024 and keeps one decimal below ten', () => {
    expect(formatBytes(1024)).toBe('1.0 KiB')
    expect(formatBytes(1536)).toBe('1.5 KiB')
    expect(formatBytes(10 * 1024 * 1024)).toBe('10 MiB')
    expect(formatBytes(3.25 * 1024 * 1024 * 1024)).toBe('3.3 GiB')
  })

  it('treats an unknown size as zero rather than NaN', () => {
    expect(formatBytes(-1)).toBe('0 B')
  })
})

describe('formatTime', () => {
  it('renders an RFC 3339 stamp as local date and minute', () => {
    const iso = '2026-10-28T03:14:09.123456789Z'
    expect(formatTime(iso)).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/)
    expect(formatTimeShort(iso)).toMatch(/^\d{2}-\d{2} \d{2}:\d{2}$/)
  })

  it('uses a dash for a missing or unparseable stamp', () => {
    expect(formatTime('')).toBe('—')
    expect(formatTime('not a time')).toBe('—')
  })
})

describe('formatDuration', () => {
  it('keeps milliseconds under a second and switches to seconds above', () => {
    expect(formatDuration(0)).toBe('—')
    expect(formatDuration(41)).toBe('41 ms')
    expect(formatDuration(1500)).toBe('1.5 s')
  })
})

describe('paths', () => {
  it('takes the last segment off an absolute container path', () => {
    expect(baseName('/media/2024/SSIS-001.mp4')).toBe('SSIS-001.mp4')
    expect(baseName('/media')).toBe('media')
    expect(baseName('')).toBe('')
  })

  it('keeps the tail of a long path readable', () => {
    expect(shortPath('/media/2024/2026/SSIS-001.mp4', 18)).toBe('…/SSIS-001.mp4')
    expect(shortPath('/media/2024/2026/SSIS-001.mp4', 24)).toBe('…/2024/2026/SSIS-001.mp4')
    expect(shortPath('/a/b.mp4', 30)).toBe('/a/b.mp4')
  })

  it('clamps a count into a range', () => {
    expect(clamp(-1, 0, 10)).toBe(0)
    expect(clamp(11, 0, 10)).toBe(10)
    expect(clamp(5, 0, 10)).toBe(5)
  })
})
