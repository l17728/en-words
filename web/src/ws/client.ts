// WS 客户端（SDD §11）：连接/重连退避/帧分发（docs/02 协议）
export type Frame = Record<string, any>
export type TurnContext = { epoch: string; turn: string }
export type Envelope = { context: TurnContext; event_id: string }

type Handlers = {
  onFrame: (f: Frame) => void
  onOpen?: () => void
  onClose?: () => void
  onError?: (message: string, retryable: boolean) => void
}

export class ClassroomWS {
  private ws: WebSocket | null = null
  private lessonId: number
  private authorize: () => Promise<string>
  private sessionToken = localStorage.getItem('token')
  private handlers: Handlers
  private retry = 0
  private closed = false
  private heartbeat: ReturnType<typeof setInterval> | null = null
  private reconnect: ReturnType<typeof setTimeout> | null = null

  constructor(lessonId: number, authorize: () => Promise<string>, handlers: Handlers) {
    this.lessonId = lessonId
    this.authorize = authorize
    this.handlers = handlers
    this.connect()
  }

  private url(ticket: string): string {
    const proto = location.protocol === 'https:' ? 'wss' : 'ws'
    return `${proto}://${location.host}/ws/classroom?lessonId=${this.lessonId}&ticket=${encodeURIComponent(ticket)}`
  }

  private scheduleReconnect(): void {
    if (this.closed || this.reconnect) return
    const delay = Math.min(1000 * Math.pow(2, this.retry++), 60000)
    this.reconnect = setTimeout(() => { this.reconnect = null; void this.connect() }, delay)
  }

  private async connect(): Promise<void> {
    const current = () => !this.closed && !!this.sessionToken && localStorage.getItem('token') === this.sessionToken
    if (!current()) { this.close(); return }
    try {
      // Tickets are short-lived and bound to the authenticated session; every
      // attempt must refresh one, including after a backend restart.
      const ticket = await this.authorize()
      if (!current()) { this.close(); return }
      const socket = new WebSocket(this.url(ticket))
      this.ws = socket
      socket.onopen = () => {
        if (!current()) { socket.close(); return }
        this.retry = 0
        if (this.heartbeat) clearInterval(this.heartbeat)
        this.heartbeat = setInterval(() => this.send({ type: 'heartbeat' }), 25000)
        this.handlers.onOpen?.()
      }
      socket.onmessage = ev => {
        if (!current() || this.ws !== socket) return
        try {
          const frame = JSON.parse(ev.data)
          if (frame.type === 'kick') this.close()
          this.handlers.onFrame(frame)
        } catch { /* malformed frames are ignored */ }
      }
      socket.onerror = () => socket.close()
      socket.onclose = () => {
        if (this.ws !== socket) return
        if (this.heartbeat) clearInterval(this.heartbeat)
        this.heartbeat = null
        this.handlers.onClose?.()
        if (!current()) { this.close(); return }
        this.scheduleReconnect()
      }
    } catch (error) {
      this.handlers.onClose?.()
      const code = (error as { code?: number }).code
      if (!current()) { this.close(); return }
      if (code === 401 || code === 403 || code === 404) {
        this.close()
        this.handlers.onError?.('课堂暂不可进入，请返回课程表确认当前账号和课程。', false)
        return
      }
      this.handlers.onError?.('课堂连接暂未恢复，请重试或返回课程表。', true)
      this.scheduleReconnect()
    }
  }

  send(m: Frame): boolean {
    if (!this.closed && this.sessionToken === localStorage.getItem('token') && this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(m))
      return true
    }
    return false
  }

  sendEvent(event: string, text: string | undefined, envelope: Envelope): boolean {
    return this.send({ type: 'student_event', event, text, ...envelope })
  }

  sendVoiceText(text: string, envelope: Envelope, submissionId?: string): boolean {
    return this.send({ type: 'voice_text', text, submission_id: submissionId, ...envelope })
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
