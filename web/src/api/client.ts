// REST 客户端（docs/02 §10 约定：{code,msg,data}）
const BASE = '/api/v1'

export interface Resp<T = unknown> {
  code: number
  msg: string
  data: T
}

let token = localStorage.getItem('token') || ''

export function setToken(t: string) {
  token = t
  localStorage.setItem('token', t)
}

export function clearToken() {
  token = ''
  localStorage.removeItem('token')
}

export function hasToken() {
  return !!token
}

export function currentRole(): string {
  return localStorage.getItem('role') || ''
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(BASE + path, {
    method,
    headers: {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  const resp = (await res.json()) as Resp<T>
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
