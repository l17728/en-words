import type { Server, ServerResponse } from 'node:http'

export function applySchemaConnectionPolicy(schema: string, response: ServerResponse) {
  if (schema === 'missing' || schema === 'null') response.setHeader('Connection', 'close')
}

export function observePlatformTransport(server: Server) {
  const started = performance.now()
  const sockets = new WeakMap<object, number>()
  const events: { event: string; socket: number; elapsed_ms: number; reason?: string }[] = []
  let next = 0
  const record = (event: string, socket: object, reason?: string) => {
    events.push({ event, socket: sockets.get(socket) || 0, elapsed_ms: Math.max(0, Math.floor(performance.now() - started)), ...(reason ? { reason } : {}) })
  }
  const reason = (error: { code?: string }) => ['ECONNRESET', 'EPIPE', 'ETIMEDOUT'].includes(error.code || '') ? error.code! : 'other_transport_error'
  server.on('connection', socket => {
    sockets.set(socket, ++next)
    record('connect', socket)
    socket.on('error', error => record('error', socket, reason(error as { code?: string })))
    socket.on('close', () => record('close', socket))
  })
  server.on('request', (request, response) => {
    const socket = request.socket
    request.on('end', () => record('end', socket))
    request.on('aborted', () => record('error', socket, 'request_aborted'))
    response.on('finish', () => record('finish', socket))
  })
  return events
}
