// REST 客户端（docs/02 §10 约定：{code,msg,data}）
// 【类级修复】token 唯一事实源=localStorage，每次请求实时读取——
// 消灭"模块级缓存与 localStorage 失同步"这一类 401 问题（登录/登出/多标签页场景全免疫）。
const BASE = '/api/v1'

export interface Resp<T = unknown> {
  code: number
  msg: string
  data: T
}

export function setToken(t: string) {
  localStorage.setItem('token', t)
}

export function clearToken() {
  localStorage.removeItem('token')
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

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const tok = liveToken() // 实时读：登录后立即可用，登出后立即失效
  const res = await fetch(BASE + path, {
    method,
    headers: {
      'Content-Type': 'application/json',
      ...(tok ? { Authorization: `Bearer ${tok}` } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  const resp = (await res.json()) as Resp<T>
  if (resp.code === 401) {
    // 会话失效（过期/服务重启）同类问题的统一恢复路径：清态回登录页
    clearToken()
    if (!location.pathname.startsWith('/login')) {
      location.href = '/login'
    }
    throw new Error('登录已过期，请重新登录')
  }
  if (resp.code !== 0) throw new Error(resp.msg || `错误 ${resp.code}`)
  return resp.data
}

export const api = {
  get: <T>(path: string) => request<T>('GET', path),
  post: <T>(path: string, body?: unknown) => request<T>('POST', path, body),
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
  ID: number
  Title: string
  Body: string
  Read: boolean
  CreatedAt: string
}

export interface FeedbackRecord {
  ID: number
  Text: string
  Status: string
  Reply: string
  CreatedAt: string
}
