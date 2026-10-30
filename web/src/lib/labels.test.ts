import { describe, expect, it } from 'vitest'
import { ApiError } from '../api/client'
import type { TaskStatus } from '../api/types'
import {
  errorCode,
  errorMessage,
  errorText,
  reasonLabel,
  skipLabel,
  statusMeta,
  STATUS_META,
  stepLabel,
  taskErrorLabel,
  triggerLabel,
} from './labels'

const SEVEN: TaskStatus[] = [
  'queued',
  'running',
  'succeeded',
  'partial',
  'failed',
  'needs_number',
  'cancelled',
]

describe('statusMeta', () => {
  it('gives each of the seven statuses its own label and semantic colour tone', () => {
    const tones = new Set<string>()
    for (const status of SEVEN) {
      const meta = STATUS_META[status]
      expect(meta.label.length).toBeGreaterThan(0)
      expect(meta.classes).toContain('bg-')
      expect(meta.classes).toContain('text-')
      tones.add(meta.tone)
    }
    // 成功绿 / 部分琥珀 / 失败红 / 运行蓝 / 排队灰 / 待补号紫 / 已取消灰 ——
    // seven states, seven token sets.
    expect(tones.size).toBe(7)
  })

  it('a status met for the first time still renders, with the code as its label', () => {
    const meta = statusMeta('some_future_status')
    expect(meta.label).toBe('some_future_status')
    expect(meta.tone).toBe('idle')
  })
})

describe('machine-code labels', () => {
  it('maps the six source reason codes', () => {
    expect(reasonLabel('egress_unavailable')).toBe('出网不可用')
    expect(reasonLabel('timeout')).toBe('超时')
    expect(reasonLabel('blocked')).toBe('被拒绝')
    expect(reasonLabel('cooldown')).toBe('冷却中')
    expect(reasonLabel('http_5xx')).toBe('上游 5xx')
    expect(reasonLabel('parse')).toBe('解析失败')
  })

  it('maps the three task-level needs-number reasons', () => {
    expect(taskErrorLabel('no_candidate')).toBe('解析不出番号')
    expect(taskErrorLabel('ambiguous')).toBe('多个候选番号')
    expect(taskErrorLabel('duplicate_stem')).toBe('主干重复，需人判定')
  })

  it('maps the three skipped-path reasons of a created batch', () => {
    expect(skipLabel('path_occupied')).toBe('已有活跃任务占用')
    expect(skipLabel('filtered')).toBe('被过滤规则排除')
    expect(skipLabel('not_found')).toBe('路径不存在')
  })

  it('falls back to the raw code rather than inventing a sentence', () => {
    expect(reasonLabel('mystery')).toBe('mystery')
    expect(skipLabel('mystery')).toBe('mystery')
    expect(taskErrorLabel('mystery')).toBe('mystery')
    expect(triggerLabel('mystery')).toBe('mystery')
    expect(stepLabel('mystery')).toBe('mystery')
  })
})

describe('errorMessage', () => {
  it('translates the codes the user can actually meet', () => {
    expect(errorMessage(new ApiError('root_not_browsable', 'x', 400))).toContain('browse_roots')
    expect(errorMessage(new ApiError('numbers_incomplete', 'x', 422))).toContain('番号')
    expect(errorMessage(new ApiError('task_running', 'x', 409))).toContain('运行')
    expect(errorMessage(new ApiError('exclude_not_under_root', 'x', 400))).toContain('排除')
  })

  it('maps a stored error body the same way as an ApiError', () => {
    // `preview.error` is `{code, message}`, not an ApiError.
    expect(errorMessage({ code: 'internal', message: 'boom' })).toContain('内部错误')
    expect(errorCode({ code: 'internal', message: 'boom' })).toBe('internal')
  })

  it('renders a bare code the way a bulk rerun item needs it', () => {
    expect(errorText('task_running')).toContain('运行')
    expect(errorText('mystery')).toBe('mystery')
  })

  it('keeps an unmapped code visible instead of swallowing it', () => {
    expect(errorMessage(new ApiError('mystery', '服务端说明', 500))).toBe('mystery：服务端说明')
    expect(errorMessage(new Error('boom'))).toBe('boom')
    expect(errorMessage(undefined)).toBe('未知错误')
  })
})
