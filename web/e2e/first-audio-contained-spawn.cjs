'use strict'

// Playwright normally starts Chromium in a detached POSIX process group so it
// can kill that group itself. The outer matrix runner owns the browser process
// group / a Windows Job Object; keep non-shell browser children in that owned
// group. Playwright's shell-based web server remains detached and is tracked by
// the parent's Linux descendant-group monitor.
const childProcess = require('node:child_process')
const fs = require('node:fs')
const marker = Symbol.for('en-words.first-audio-contained-spawn')

if (!childProcess.spawn[marker]) {
  const originalSpawn = childProcess.spawn
  const containedSpawn = function (...args) {
    const optionsIndex = args.length - 1
    const options = args[optionsIndex]
    const isOptions = options && typeof options === 'object' && !Array.isArray(options)
    const detachedShell = isOptions && options.detached === true && options.shell === true
    if (isOptions && options.detached === true && !detachedShell) {
      args[optionsIndex] = { ...options, detached: false }
    }
    const child = Reflect.apply(originalSpawn, this, args)
    if (detachedShell && child.pid && process.platform === 'linux') {
      const manifest = process.env.FIRST_AUDIO_MATRIX_DETACHED_GROUPS_FILE
      if (!manifest) {
        try { process.kill(-child.pid, 'SIGKILL') } catch {}
        throw new Error('detached web-server process manifest is not configured')
      }
      try {
        const stat = fs.readFileSync(`/proc/${child.pid}/stat`, 'ascii')
        const fields = stat.slice(stat.lastIndexOf(')') + 2).trim().split(/\s+/)
        const processGroup = Number(fields[2])
        const startTicks = fields[19]
        if (processGroup !== child.pid || !/^\d+$/.test(startTicks || ''))
          throw new Error('detached web-server process identity is invalid')
        fs.appendFileSync(manifest, `${child.pid}:${startTicks}\n`, { encoding: 'ascii', mode: 0o600 })
      } catch (error) {
        try { process.kill(-child.pid, 'SIGKILL') } catch {}
        throw new Error('failed to register detached web-server process group', { cause: error })
      }
    }
    return child
  }
  Object.defineProperty(containedSpawn, marker, { value: true })
  childProcess.spawn = containedSpawn
}
