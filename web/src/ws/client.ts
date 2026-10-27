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
  private heartbeat: ReturnType<typeof setTimeout> | null = null
  private heartbeatDeadline: ReturnType<typeof setTimeout> | null = null
  private awaitingHeartbeat = false
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

  private clearHeartbeat(): void {
    if (this.heartbeat) clearTimeout(this.heartbeat)
    if (this.heartbeatDeadline) clearTimeout(this.heartbeatDeadline)
    this.heartbeat = null
    this.heartbeatDeadline = null
    this.awaitingHeartbeat = false
  }

  private lost(socket: WebSocket, reason?: string): void {
    if (this.ws !== socket) return
    this.ws = null // Invalidate before close: a black hole may never return its close handshake.
    this.clearHeartbeat()
    this.handlers.onClose?.()
    if (reason) this.handlers.onError?.(reason, true)
    try { socket.close() } catch { /* already unusable */ }
    if (this.sessionToken !== localStorage.getItem('token')) { this.close(); return }
    this.scheduleReconnect()
  }

  private ping(socket: WebSocket): void {
    if (this.closed || this.ws !== socket || this.awaitingHeartbeat) return
    this.heartbeat = null
    this.awaitingHeartbeat = true
    this.heartbeatDeadline = setTimeout(() => {
      console.warn('[ws_liveness]', { module: 'ws', event: 'heartbeat_timeout',
        trace_id: `lesson-${this.lessonId}`, lesson_id: this.lessonId, timeout_ms: 10000 })
      this.lost(socket, '课堂连接暂时无响应，正在重新连接。')
    }, 10000)
    if (!this.send({ type: 'heartbeat' })) this.lost(socket)
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
        if (!current() || this.ws !== socket) { socket.close(); return }
        this.retry = 0
        this.clearHeartbeat()
        this.handlers.onOpen?.()
        this.ping(socket)
      }
      socket.onmessage = ev => {
        if (!current() || this.ws !== socket) return
        try {
          const frame = JSON.parse(ev.data)
          if (frame.type === 'heartbeat_ack' && this.awaitingHeartbeat) {
            this.awaitingHeartbeat = false
            if (this.heartbeatDeadline) clearTimeout(this.heartbeatDeadline)
            this.heartbeatDeadline = null
            this.heartbeat = setTimeout(() => this.ping(socket), 10000)
          }
          if (frame.type === 'kick') this.close()
          this.handlers.onFrame(frame)
        } catch { /* malformed frames are ignored */ }
      }
      socket.onerror = () => this.lost(socket)
      socket.onclose = () => this.lost(socket)
    } catch (error) {
      this.handlers.onClose?.()
      const code = (error as { code?: number }).code
      if (!current()) { this.close(); return }
      if ((error as { error_code?: string }).error_code === 'transaction_outcome_uncertain') {
        this.close()
        this.handlers.onError?.('课堂状态无法确认，为避免重复操作已停止本课。请返回课程表查看进度。', false)
        return
      }
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
      const socket = this.ws
      try { socket.send(JSON.stringify(m)); return true } catch {
        console.warn('[ws_liveness]', { module: 'ws', event: 'send_failed',
          trace_id: `lesson-${this.lessonId}`, lesson_id: this.lessonId })
        this.lost(socket, '课堂消息暂未发出，正在重新连接。')
      }
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
    this.clearHeartbeat()
    if (this.reconnect) clearTimeout(this.reconnect)
    this.reconnect = null
    const socket = this.ws
    this.ws = null
    if (socket) {
      this.handlers.onClose?.()
      try { socket.close() } catch { /* no active transport remains */ }
    }
  }
}
