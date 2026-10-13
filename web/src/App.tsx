import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom'
import { useAuthStore } from './stores/auth'
import LoginPage from './pages/Login'
import BooksPage from './pages/Books'
import DashboardPage from './pages/Dashboard'
import PlacementPage from './pages/Placement'
import ClassroomPage from './pages/Classroom'
import StatsPage from './pages/Stats'
import AdminPage from './pages/Admin'
import SettingsPage from './pages/Settings'

export default function App() {
  const token = useAuthStore((s) => s.token)
  const role = useAuthStore((s) => s.role)
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        {token ? (
          <>
            <Route path="/admin" element={role === 'admin' ? <AdminPage /> : <Navigate to="/dashboard" replace />} />
            <Route path="/books" element={<BooksPage />} />
            <Route path="/dashboard" element={<DashboardPage />} />
            <Route path="/placement/:bookId" element={<PlacementPage />} />
            <Route path="/classroom/:lessonId" element={<ClassroomPage />} />
            <Route path="/stats" element={<StatsPage />} />
            <Route path="/settings" element={<SettingsPage />} />
            <Route path="*" element={<Navigate to="/dashboard" replace />} />
          </>
        ) : (
          <Route path="*" element={<Navigate to="/login" replace />} />
        )}
      </Routes>
    </BrowserRouter>
  )
}
