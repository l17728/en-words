import { create } from 'zustand'

interface AuthState {
  token: string
  role: string
  nickname: string
  userId: number
  login: (token: string, role: string, nickname: string, userId: number) => void
  logout: () => void
}

export const useAuthStore = create<AuthState>((set) => ({
  token: localStorage.getItem('token') || '',
  role: localStorage.getItem('role') || '',
  nickname: localStorage.getItem('nickname') || '',
  userId: Number(localStorage.getItem('userId') || 0),
  login: (token, role, nickname, userId) => {
    localStorage.setItem('token', token)
    localStorage.setItem('role', role)
    localStorage.setItem('nickname', nickname)
    localStorage.setItem('userId', String(userId))
    set({ token, role, nickname, userId })
  },
  logout: () => {
    localStorage.removeItem('token')
    localStorage.removeItem('role')
    localStorage.removeItem('nickname')
    localStorage.removeItem('userId')
    set({ token: '', role: '', nickname: '', userId: 0 })
  },
}))
