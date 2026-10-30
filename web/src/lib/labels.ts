import { ApiError } from '../api/client'
import type { TaskStatus } from '../api/types'

// The Chinese wording lives here and only here. The API transports machine codes, so
// this table is the single place a code becomes a sentence — and the same table the
// error panel, the status pill and the reason column all read.

export type Tone = 'ok' | 'partial' | 'danger' | 'running' | 'queued' | 'number' | 'idle'

export interface StatusMeta {
  label: string
  tone: Tone
  /** Literal Tailwind classes: one of these is scanned by the build per status. */
  classes: string
  dot: string
}

/**
 * STATUS_META gives each of the seven statuses its own semantic colour. The values
 * come from the `@theme` tokens, never from here: this file only says which role a
 * status uses.
 */
export const STATUS_META: Record<TaskStatus, StatusMeta> = {
  succeeded: {
    label: '成功',
    tone: 'ok',
    classes: 'bg-ok-tint text-ok-ink border-ok-mark/40',
    dot: 'bg-ok-mark',
  },
  partial: {
    label: '部分',
    tone: 'partial',
    classes: 'bg-partial-tint text-partial-ink border-partial-mark/40',
    dot: 'bg-partial-mark',
  },
  failed: {
    label: '失败',
    tone: 'danger',
    classes: 'bg-danger-tint text-danger-ink border-danger-mark/40',
    dot: 'bg-danger-mark',
  },
  running: {
    label: '运行中',
    tone: 'running',
    classes: 'bg-running-tint text-running-ink border-running-mark/40',
    dot: 'bg-running-mark',
  },
  queued: {
    label: '排队',
    tone: 'queued',
    classes: 'bg-queued-tint text-queued-ink border-queued-mark/40',
    dot: 'bg-queued-mark',
  },
  needs_number: {
    label: '待补号',
    tone: 'number',
    classes: 'bg-number-tint text-number-ink border-number-mark/40',
    dot: 'bg-number-mark',
  },
  cancelled: {
    label: '已取消',
    tone: 'idle',
    classes: 'bg-idle-tint text-idle-ink border-idle-mark/40',
    dot: 'bg-idle-mark',
  },
}

/** statusMeta never throws on an unknown status: a new code still renders. */
export function statusMeta(status: string): StatusMeta {
  return (
    STATUS_META[status as TaskStatus] ?? {
      label: status,
      tone: 'idle',
      classes: 'bg-idle-tint text-idle-ink border-idle-mark/40',
      dot: 'bg-idle-mark',
    }
  )
}

const REASON_LABELS: Record<string, string> = {
  egress_unavailable: '出网不可用',
  timeout: '超时',
  blocked: '被拒绝',
  cooldown: '冷却中',
  http_5xx: '上游 5xx',
  parse: '解析失败',
}

/** reasonLabel is the six-code set of `source_runs.reason`. */
export function reasonLabel(code: string): string {
  return REASON_LABELS[code] ?? code
}

const TASK_ERROR_LABELS: Record<string, string> = {
  no_candidate: '解析不出番号',
  ambiguous: '多个候选番号',
  duplicate_stem: '主干重复，需人判定',
}

/** taskErrorLabel is `tasks.last_error` on a needs_number task. */
export function taskErrorLabel(code: string): string {
  return TASK_ERROR_LABELS[code] ?? code
}

const SKIP_LABELS: Record<string, string> = {
  path_occupied: '已有活跃任务占用',
  filtered: '被过滤规则排除',
  not_found: '路径不存在',
}

/** skipLabel is `POST /api/tasks`'s `skipped[].reason`. */
export function skipLabel(reason: string): string {
  return SKIP_LABELS[reason] ?? reason
}

const TRIGGER_LABELS: Record<string, string> = {
  manual: '手动',
  monitor: '监控',
  webhook: '回调',
}

export function triggerLabel(trigger: string): string {
  return TRIGGER_LABELS[trigger] ?? trigger
}

const STEP_LABELS: Record<string, string> = {
  scan: '扫描',
  filter: '过滤',
  group: '分组',
  parse: '解析',
  scrape: '刮削',
  images: '图片',
  nfo: 'NFO',
  output: '落盘',
}

/** stepLabel is a step type in Chinese; the type itself stays visible in the table. */
export function stepLabel(type: string): string {
  return STEP_LABELS[type] ?? type
}

const STEP_STATUS_LABELS: Record<string, string> = {
  ok: '完成',
  succeeded: '完成',
  running: '运行中',
  failed: '失败',
  skipped: '跳过',
}

export function stepStatusLabel(status: string): string {
  return STEP_STATUS_LABELS[status] ?? status
}

const SOURCE_STATUS_LABELS: Record<string, string> = {
  ok: '有贡献',
  notfound: '未找到',
  failed: '失败',
}

/** sourceStatusLabel is the scrape three-state: ok / notfound / failed. */
export function sourceStatusLabel(status: string): string {
  return SOURCE_STATUS_LABELS[status] ?? status
}

const ROLE_LABELS: Record<string, string> = {
  primary: '主片',
  part: '分片',
  companion: '伴随',
  poster: '海报',
  fanart: '背景图',
  extrafanarts: '剧照',
  extrafanart: '剧照',
}

export function roleLabel(role: string): string {
  return ROLE_LABELS[role] ?? role
}

const KIND_LABELS: Record<string, string> = {
  video: '视频',
  companion: '伴随',
  image: '图片',
  nfo: 'NFO',
}

export function kindLabel(kind: string): string {
  return KIND_LABELS[kind] ?? kind
}

const ACTION_LABELS: Record<string, string> = {
  created: '新建',
  overwritten: '覆盖',
  skipped: '跳过（内容相同）',
  deleted: '清理旧产物',
}

export function actionLabel(action: string): string {
  return ACTION_LABELS[action] ?? action
}

const ERROR_MESSAGES: Record<string, string> = {
  bad_request: '请求本身不对（参数或取值不合法）。',
  empty_selection: '没有选中任何目录或文件。',
  root_not_browsable: '这个路径不在 browse_roots 白名单里：只能浏览配置允许的容器内路径。',
  exclude_not_under_root: '排除必须是某个扫描目录的真子目录；请先加入扫描目录再排除它的一部分。',
  monitor_root_nested: '监控目录不能相同或互相嵌套。',
  unknown_pipeline: '找不到这条流水线：配置文件可能已被改动，刷新后重试。',
  not_found: '这个对象不存在：预览可能已过期，或任务已被清理。',
  image_missing: '台账里没有这张图，或磁盘上的文件已丢失。',
  task_running: '任务正在运行：等它结束，或先取消。',
  task_terminal: '任务已经结束，没有可取消的动作。',
  unsupported_media_type: '写接口只接受 application/json。',
  numbers_incomplete: '还有视频没填番号：每一行都要有号才能提交。',
  unauthorized: '未授权（V1 由前置代理负责鉴权）。',
  internal: '服务端内部错误：重试一次，仍失败就看进程日志。',
  db_unavailable: '数据库不可用：进程起来了但读不到库，检查 db_path 是否可写。',
  network: '连不上服务：确认 mdc serve 还在跑，网络没有断开。',
  bad_response: '服务端返回了非 JSON 响应：多半是版本不一致或前面挡了代理。',
}

/** errorCodeOf reads the machine code off either an ApiError or a stored error body. */
function errorCodeOf(err: unknown): string {
  if (err instanceof ApiError) return err.code
  if (typeof err === 'object' && err !== null && 'code' in err) {
    const code = (err as { code?: unknown }).code
    if (typeof code === 'string') return code
  }
  return ''
}

/** errorText is the sentence for a machine code on its own (a bulk rerun's item). */
export function errorText(code: string): string {
  return ERROR_MESSAGES[code] ?? code
}

/** errorMessage turns a failure into a sentence plus the code, never just prose. */
export function errorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    const text = ERROR_MESSAGES[err.code]
    return text ? text : `${err.code}：${err.message}`
  }
  if (typeof err === 'object' && err !== null && 'code' in err) {
    // A stored error body (`preview.error`, `{code, message}`): map the code the same
    // way, so a failed preview is not reported as "未知错误".
    const code = errorCodeOf(err)
    const message = (err as { message?: unknown }).message
    const text = ERROR_MESSAGES[code]
    if (text) return text
    return typeof message === 'string' && message ? `${code}：${message}` : code
  }
  if (err instanceof Error) return err.message
  if (typeof err === 'string' && err) return err
  return '未知错误'
}

/** errorCode is the machine code an error carries, for the panel's small print. */
export function errorCode(err: unknown): string {
  return errorCodeOf(err)
}
