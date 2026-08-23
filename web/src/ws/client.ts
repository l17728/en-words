// WS 客户端（SDD §11）：连接/重连退避/帧分发（docs/02 协议）
export type Frame = Record<string, any>

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
    this.ws = new WebSocket(this.url())
    this.ws.onopen = () => {
      this.retry = 0
      this.handlers.onOpen?.()
    }
    this.ws.onmessage = (ev) => {
      try {
        this.handlers.onFrame(JSON.parse(ev.data))
      } catch {
        /* 非 JSON 帧忽略 */
      }
    }
    this.ws.onclose = () => {
      this.handlers.onClose?.()
      if (this.closed) return
      // 指数退避重连 1s..60s（SDD §11.3）
      const delay = Math.min(1000 * Math.pow(2, this.retry++), 60000)
      setTimeout(() => this.connect(), delay)
    }
  }

  send(m: Frame): boolean {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(m))
      return true
    }
    return false
  }

  sendEvent(event: string, text?: string): boolean {
    return this.send({ type: 'student_event', event, text })
  }

  sendVoiceText(text: string): boolean {
    return this.send({ type: 'voice_text', text })
  }

  close(): void {
    this.closed = true
    this.ws?.close()
  }
}
