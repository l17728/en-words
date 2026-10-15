import { clientId } from '../interaction/client-id'
import { speechCache } from '../audio/speech-cache'
import { clearFeedbackPending } from '../feedback/pending'
// REST 客户端（docs/02 §10 约定：{code,msg,data}）
// 【类级修复】token 唯一事实源=localStorage，每次请求实时读取——
// 消灭"模块级缓存与 localStorage 失同步"这一类 401 问题（登录/登出/多标签页场景全免疫）。
const BASE = '/api/v1'

export interface Resp<T = unknown> {
  code: number
  msg: string
  data: T
  error_code?: string
  submission_id?: string
  retryable?: boolean
}

export class ApiError extends Error {
  readonly code: number
  readonly error_code?: string
  readonly data?: unknown
  readonly submission_id?: string
  readonly retryable?: boolean
  constructor(message: string, code: number, error_code?: string,
    data?: unknown, submission_id?: string, retryable?: boolean) {
    super(message)
    this.name = 'ApiError'
    this.code = code; this.error_code = error_code; this.data = data
    this.submission_id = submission_id; this.retryable = retryable
  }
}

export function setToken(t: string) {
  if (t !== liveToken()) speechCache.clear()
  localStorage.setItem('token', t)
}

export function clearToken() {
  speechCache.clear()
  localStorage.removeItem('token')
  clearFeedbackPending()
}

function liveToken(): string {
  try {
    return localStorage.getItem('token') || ''
  } catch {
    return ''
  }
}

export function hasToken() {
  return !!liveToken()
}

export function currentRole(): string {
  try {
    return localStorage.getItem('role') || ''
  } catch {
    return ''
  }
}

export interface RequestOptions { signal?: AbortSignal; timeoutMs?: number }

async function request<T>(method: string, path: string, body?: unknown, options: RequestOptions = {}): Promise<T> {
  const tok = liveToken()
  const startedAt = Date.now()
  let requestId = 'unavailable'
  try { requestId = clientId() } catch { /* Diagnostics must not prevent ordinary reads. */ }
  const safePath = path.split('?')[0].split('#')[0]
  const lesson = safePath.match(/\/classroom\/lessons\/(\d+)/)?.[1]
  let serverTraceId: string | undefined
  const writing = method !== 'GET'
  const uncertain = writing ? '；操作结果待确认，请先查询状态，勿重复提交' : '，请重试'
  const controller = new AbortController()
  let rejectStop: (error: ApiError) => void = () => {}
  const stop = new Promise<never>((_, reject) => { rejectStop = reject })
  const cancel = () => { rejectStop(new ApiError('请求已取消' + uncertain, 0, 'request_canceled')); controller.abort() }
  const requestedTimeout = options.timeoutMs ?? 30_000
  const timeout = Number.isFinite(requestedTimeout) && requestedTimeout > 0 ? requestedTimeout : 30_000
  const timer = setTimeout(() => { rejectStop(new ApiError('请求超时' + uncertain, 0, 'request_timeout')); controller.abort() }, timeout)
  options.signal?.addEventListener('abort', cancel, { once: true })
  if (options.signal?.aborted) cancel()
  try {
    return await Promise.race([stop, (async () => {
      if (controller.signal.aborted) return await stop
      let res: Response
      try {
        res = await fetch(BASE + path, { method, signal: controller.signal,
          headers: { 'Content-Type': 'application/json', ...(tok ? { Authorization: `Bearer ${tok}` } : {}) },
          body: body !== undefined ? JSON.stringify(body) : undefined })
      } catch {
        if (controller.signal.aborted) return await stop
        throw new ApiError('网络连接失败' + uncertain, 0, 'network_error')
      }
      const returnedTrace = res.headers?.get('X-Trace-ID')
      if (returnedTrace && /^[A-Za-z0-9_-]{1,128}$/.test(returnedTrace)) serverTraceId = returnedTrace
      let resp: Resp<T>
      try { resp = await res.json() } catch {
        if (controller.signal.aborted) return await stop
        throw new ApiError('服务响应暂不可用' + uncertain, 0, 'invalid_response')
      }
      // Body parsing may cross a session change; an old 401 must never log out the new account.
      if (tok !== liveToken()) throw new ApiError('当前会话已改变，请重试当前操作', 409, 'session_changed')
      if (controller.signal.aborted) return await stop
      if (!resp || typeof resp !== 'object' || !Number.isFinite(resp.code) || (resp.code === 0 && !res.ok)) {
        throw new ApiError('服务响应暂不可用' + uncertain, 0, 'invalid_response')
      }
      if (resp.code === 401) {
        clearToken()
        if (!location.pathname.startsWith('/login')) location.href = '/login'
        throw new ApiError('登录已过期，请重新登录', 401, resp.error_code, resp.data, resp.submission_id, resp.retryable)
      }
      if (resp.code !== 0) {
        // Server failures can happen after a write; do not expose raw upstream bodies or imply rollback.
        const message = resp.code >= 500 ? '服务暂不可用' + uncertain : typeof resp.msg === 'string' ? resp.msg : `错误 ${resp.code}`
        throw new ApiError(message, resp.code, resp.error_code, resp.data, resp.submission_id, resp.retryable)
      }
      return resp.data
    })()])
  } catch (error) {
    const localReasons = ['request_timeout', 'request_canceled', 'network_error', 'invalid_response', 'session_changed']
    const reason = error instanceof ApiError ? (localReasons.includes(error.error_code || '') ? error.error_code : 'application_rejected') : 'unexpected_failure'
    console.warn('[api_request_failed]', { module: 'api', event: 'request_failed', method, path: safePath,
      request_id: requestId, server_trace_id: serverTraceId, trace_id: lesson ? `lesson-${lesson}` : `tr-${requestId}`, reason, duration_ms: Date.now() - startedAt })
    throw error
  } finally {
    clearTimeout(timer)
    options.signal?.removeEventListener('abort', cancel)
  }
}

export const api = {
  get: <T>(path: string, options?: RequestOptions) => request<T>('GET', path, undefined, options),
  post: <T>(path: string, body?: unknown, options?: RequestOptions) => request<T>('POST', path, body, options),
  del: <T>(path: string, options?: RequestOptions) => request<T>('DELETE', path, undefined, options),
}

// ---- 领域类型 ----

export interface LoginResult {
  token: string
  role: string
  nickname: string
  user_id: number
}

export interface Book {
  ID: number
  Name: string
  Grade: string
  Description: string
  Status: string
  WordCount: number
}

export interface Lesson {
  ID: number
  StudentID: number
  BookID: number
  Status: string
  OpenAt: string
  BatchWordIDs: number[]
}

export interface Notification {
  id: number
  title: string
  body: string
  read: boolean
  created_at: string
}

export interface FeedbackRecord {
  parent_submission_id?: string
  conversation_id?: string
  clarification_answered?: boolean
  id: number
  text: string
  status: string
  reply: string
  created_at: string
  student_id?: number
  submission_id?: string
  task_id?: string
  version?: number
  results?: FeedbackIntentResult[]
  retryable?: boolean
  refresh_pending?: boolean
  updated_at?: string
  idempotency?: string
  retry_after_ms?: number
  error_code?: string
}

export interface FeedbackIntentResult {
  index: number
  kind: string
  status: string
  result_code: string
  reply: string
  retryable: boolean
  refresh_pending: boolean
  actual?: unknown
}

export interface FeedbackTodo {
  id: number
  task_id: string
  intent_index: number
  student_id: number
  status: string
  text: string
  created_at: string
  legacy?: boolean
  reply?: string
}
