import { BrowserRouter, Routes, Route, Navigate, Link, useLocation } from 'react-router-dom'
import { Component, lazy, Suspense, useEffect, type ComponentType, type ReactNode } from 'react'
import { Modal } from 'antd'
import { useAuthStore } from './stores/auth'

function buildId() {
  const entry = [...document.scripts].map((script) => script.src).find((src) => src.includes('/assets/'))
  return entry?.match(/-[A-Za-z0-9_-]{6,}\.js(?:\?|$)/)?.[0] ?? entry ?? location.pathname
}

const RETRY_BUILD_KEY = 'route-chunk-retry-build-id'

function lazyRoute(load: () => Promise<{ default: ComponentType<any> }>) {
  return lazy(load)
}

const LoginPage = lazyRoute(() => import('./pages/Login'))
const BooksPage = lazyRoute(() => import('./pages/Books'))
const DashboardPage = lazyRoute(() => import('./pages/Dashboard'))
const PlacementPage = lazyRoute(() => import('./pages/Placement'))
const ClassroomPage = lazyRoute(() => import('./pages/Classroom'))
const StatsPage = lazyRoute(() => import('./pages/Stats'))
const AdminPage = lazyRoute(() => import('./pages/Admin'))
const SettingsPage = lazyRoute(() => import('./pages/Settings'))
const VoicePreflight = lazyRoute(() => import('./pages/VoicePreflight'))

class RouteErrorBoundary extends Component<{ children: ReactNode }> {
  state = { failed: false }
  static getDerivedStateFromError() { return { failed: true } }

  render() {
    if (!this.state.failed) return this.props.children
    const currentBuild = buildId()
    let retried = false
    try { retried = sessionStorage.getItem(RETRY_BUILD_KEY) === currentBuild }
    catch { retried = history.state?.routeChunkRetryBuildId === currentBuild }
    return (
      <main role="alert" aria-live="assertive" style={{ padding: 24 }}>
        <h1>页面暂时无法加载</h1>
        <p>{retried ? '更新后仍无法加载此页面，请稍后重试。' : '请检查网络后重试。'}</p>
        {!retried && <button type="button" onClick={() => {
          try { sessionStorage.setItem(RETRY_BUILD_KEY, currentBuild) }
          catch { history.replaceState({ ...(history.state ?? {}), routeChunkRetryBuildId: currentBuild }, '') }
          location.reload()
        }}>重试</button>}
        <p><Link to="/login">返回登录</Link>{' · '}<Link to="/dashboard">返回学习首页</Link></p>
      </main>
    )
  }
}

function RouteLoading() {
  return (
    <main style={{ padding: 24 }}>
      <p role="status" aria-live="polite">正在加载页面…</p>
      <button type="button" onClick={() => history.back()}>返回</button>
      {' · '}<Link to="/dashboard">返回学习首页</Link>
    </main>
  )
}

function RouteContent({ token, role, userId }: { token: string | null; role: string | null; userId: number | null }) {
  const location = useLocation()
  return (
    <RouteErrorBoundary key={`${userId}:${token}:${location.pathname}`}>
      <Suspense fallback={<RouteLoading />}>
        <Routes key={`${userId}:${token}`}>
          <Route path="/login" element={<LoginPage />} />
          {token ? (
            <>
              <Route path="/admin" element={role === 'admin' ? <AdminPage /> : <Navigate to="/dashboard" replace />} />
              <Route path="/books" element={<BooksPage />} />
              <Route path="/dashboard" element={<DashboardPage />} />
              <Route path="/placement/:bookId" element={<PlacementPage />} />
              <Route path="/classroom/:lessonId/voice-preflight" element={<VoicePreflight />} />
              <Route path="/classroom/:lessonId" element={<ClassroomPage />} />
              <Route path="/stats" element={<StatsPage />} />
              <Route path="/settings" element={<SettingsPage />} />
              <Route path="*" element={<Navigate to="/dashboard" replace />} />
            </>
          ) : (
            <Route path="*" element={<Navigate to="/login" replace />} />
          )}
        </Routes>
      </Suspense>
    </RouteErrorBoundary>
  )
}

export default function App() {
  const token = useAuthStore((s) => s.token)
  const role = useAuthStore((s) => s.role)
  const userId = useAuthStore((s) => s.userId)
  useEffect(() => () => Modal.destroyAll(), [userId, token])
  return (
    <BrowserRouter>
      <RouteContent token={token} role={role} userId={userId} />
    </BrowserRouter>
  )
}
