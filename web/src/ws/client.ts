// WS 客户端（SDD §11）：连接/重连退避/帧分发（docs/02 协议）
export type Frame = Record<string, any>
export type TurnContext = { epoch: string; turn: string }
export type Envelope = { context: TurnContext; event_id: string }

type Handlers = {
  onFrame: (f: Frame) => void
  onOpen?: () => void
  onClose?: () => void
}

export class ClassroomWS {
  private ws: WebSocket | null = null
  private lessonId: number
  private ticket: string
  private handlers: Handlers
  private retry = 0
  private closed = false
  private heartbeat: ReturnType<typeof setInterval> | null = null
  private reconnect: ReturnType<typeof setTimeout> | null = null

  constructor(lessonId: number, ticket: string, handlers: Handlers) {
    this.lessonId = lessonId
    this.ticket = ticket
    this.handlers = handlers
    this.connect()
  }

  private url(): string {
    const proto = location.protocol === 'https:' ? 'wss' : 'ws'
    return `${proto}://${location.host}/ws/classroom?lessonId=${this.lessonId}&ticket=${this.ticket}`
  }

  private connect(): void {
    if (this.closed) return
    // R5-P1：token 感知——401 登出后停止重连（防旧 ticket 僵尸连接）
    try {
      if (!localStorage.getItem('token')) {
        this.closed = true
        this.handlers.onClose?.()
        return
      }
    } catch { /* 存储不可用时继续尝试连接 */ }
    this.ws = new WebSocket(this.url())
    this.ws.onopen = () => {
      if (this.closed) { this.ws?.close(); return }
      this.retry = 0
      if (this.heartbeat) clearInterval(this.heartbeat)
      this.heartbeat = setInterval(() => this.send({ type: 'heartbeat' }), 25000)
      this.handlers.onOpen?.()
    }
    this.ws.onmessage = (ev) => {
      try {
        const frame = JSON.parse(ev.data)
        if (frame.type === 'kick') this.close()
        this.handlers.onFrame(frame)
      } catch {
        /* 非 JSON 帧忽略 */
      }
    }
    this.ws.onclose = () => {
      if (this.heartbeat) clearInterval(this.heartbeat)
      this.heartbeat = null
      this.handlers.onClose?.()
      if (this.closed) return
      // 指数退避重连 1s..60s（SDD §11.3）
      const delay = Math.min(1000 * Math.pow(2, this.retry++), 60000)
      this.reconnect = setTimeout(() => this.connect(), delay)
    }
  }

  send(m: Frame): boolean {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(m))
      return true
    }
    return false
  }

  sendEvent(event: string, text: string | undefined, envelope: Envelope): boolean {
    return this.send({ type: 'student_event', event, text, ...envelope })
  }

  sendVoiceText(text: string, envelope: Envelope): boolean {
    return this.send({ type: 'voice_text', text, ...envelope })
  }

  close(): void {
    this.closed = true
    if (this.heartbeat) clearInterval(this.heartbeat)
    if (this.reconnect) clearTimeout(this.reconnect)
    this.heartbeat = null
    this.reconnect = null
    this.ws?.close()
  }
}
