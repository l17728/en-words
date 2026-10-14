import { create } from 'zustand'
import { clearFeedbackPending } from '../feedback/pending'

interface AuthState {
  token: string
  role: string
  nickname: string
  userId: number
  login: (token: string, role: string, nickname: string, userId: number) => void
  logout: () => void
}

const readLS = (k: string) => {
  try { return localStorage.getItem(k) || '' } catch { return '' }
}

export const useAuthStore = create<AuthState>((set) => ({
  token: readLS('token'),
  role: readLS('role'),
  nickname: readLS('nickname'),
  userId: Number(readLS('userId') || 0),
  login: (token, role, nickname, userId) => {
    localStorage.setItem('token', token)
    localStorage.setItem('role', role)
    localStorage.setItem('nickname', nickname)
    localStorage.setItem('userId', String(userId))
    set({ token, role, nickname, userId })
  },
  logout: () => {
    clearFeedbackPending()
    localStorage.removeItem('token')
    localStorage.removeItem('role')
    localStorage.removeItem('nickname')
    localStorage.removeItem('userId')
    set({ token: '', role: '', nickname: '', userId: 0 })
  },
}))

// R5-P1：多标签页会话同步（storage 事件跨标签页广播）
// 其他标签页登录/登出 → 本页 store 实时跟随（登出一致性）
if (typeof window !== 'undefined') {
  window.addEventListener('storage', (ev) => {
    if (ev.key === 'token' && !ev.newValue) clearFeedbackPending()
    if (['token', 'role', 'nickname', 'userId'].includes(ev.key || '')) {
      useAuthStore.setState({
        token: readLS('token'),
        role: readLS('role'),
        nickname: readLS('nickname'),
        userId: Number(readLS('userId') || 0),
      })
    }
  })
}
