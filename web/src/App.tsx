import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom'
import { useAuthStore } from './stores/auth'
import LoginPage from './pages/Login'
import BooksPage from './pages/Books'
import DashboardPage from './pages/Dashboard'
import PlacementPage from './pages/Placement'
import ClassroomPage from './pages/Classroom'
import StatsPage from './pages/Stats'

export default function App() {
  const token = useAuthStore((s) => s.token)
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        {token ? (
          <>
            <Route path="/books" element={<BooksPage />} />
            <Route path="/dashboard" element={<DashboardPage />} />
            <Route path="/placement/:bookId" element={<PlacementPage />} />
            <Route path="/classroom/:lessonId" element={<ClassroomPage />} />
            <Route path="/stats" element={<StatsPage />} />
            <Route path="*" element={<Navigate to="/dashboard" replace />} />
          </>
        ) : (
          <Route path="*" element={<Navigate to="/login" replace />} />
        )}
      </Routes>
    </BrowserRouter>
  )
}
