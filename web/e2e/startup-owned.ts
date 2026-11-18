import { spawn, type ChildProcess } from 'node:child_process'
import { createWriteStream, type WriteStream } from 'node:fs'
import { resolve } from 'node:path'
import { performance } from 'node:perf_hooks'
import { ownedExited, stopOwned, cleanupOwned, detachOwnedOutput } from './owned-process'

/** Only this test's processes. Expected startup failures never enter a ready fixture. */
export class StartupOwned {
  readonly records: { pid?: number; binary: string; port: number; startedAt: number; readyMs?: number; exitCode?: number | null; signalCode?: string | null; spawnFailed?: boolean }[] = []
  private children: { child: ChildProcess; log: WriteStream; record: StartupOwned['records'][number]; graceful: boolean; closed: Promise<void> }[] = []

  start(binary: string, db: string, port: number, logPath: string, harnessEnv?: Record<string, string>) {
    const log = createWriteStream(logPath)
    const record = { binary: resolve(binary), port, startedAt: performance.now() } as StartupOwned['records'][number]
    const harnessTest = harnessEnv?.ENWORDS_STARTUP_CLEANUP_HARNESS === '1' ? 'TestStartupCleanupHarness' : 'TestStartupFailureHarness'
    const child = spawn(record.binary, harnessEnv
      ? [`-test.run=^${harnessTest}$`, '-test.timeout=60s', '-test.v']
      : ['-testmode', '-providers', 'mock', '-port', String(port), '-db', db], {
      cwd: resolve('../server'), windowsHide: true, stdio: ['pipe', 'pipe', 'pipe'],
      env: { ...process.env, ENWORDS_DUPLEX_PILOT: '0', ...harnessEnv },
    })
    record.pid = child.pid
    child.stdout?.pipe(log, { end: false }); child.stderr?.pipe(log, { end: false })
    child.on('exit', (code, signal) => { record.exitCode = code; record.signalCode = signal })
    child.on('error', () => { record.spawnFailed = true })
    const closed = new Promise<void>(done => child.once('close', () => done()))
    this.records.push(record); this.children.push({ child, log, record, graceful: !!harnessEnv, closed })
    return child
  }

  async ready(child: ChildProcess, port: number) {
    const entry = this.children.find(item => item.child === child)
    if (!entry) throw new Error('Not an owned startup process')
    const deadline = entry.record.startedAt + 30_000
    while (performance.now() < deadline) {
      if (ownedExited(child) || entry.record.spawnFailed) throw new Error('Owned startup exited before readiness')
      try {
        const response = await fetch(`http://127.0.0.1:${port}/api/v1/bootstrap/status`, { signal: AbortSignal.timeout(Math.min(1000, Math.max(1, Math.floor(deadline - performance.now())))) })
        const body = await response.json()
        if (response.status === 200 && body.code === 0 && typeof body.data?.needed === 'boolean' && performance.now() <= deadline && !ownedExited(child)) {
          entry.record.readyMs = performance.now() - entry.record.startedAt
          return body.data
        }
      } catch { /* bounded readiness observation, never relaunches the process */ }
      await new Promise(done => setTimeout(done, 100))
    }
    throw new Error('Owned startup exceeded original 30000ms readiness gate')
  }

  async exited(child: ChildProcess, timeout = 30_000) {
    if (this.children.find(item => item.child === child)?.record.spawnFailed) throw new Error('Owned startup spawn failed')
    if (!ownedExited(child)) await new Promise<void>((done, reject) => {
      const finish = (error?: Error) => { clearTimeout(timer); child.removeListener('exit', exit); child.removeListener('error', fail); if (error) reject(error); else done() }
      const exit = () => finish()
      const fail = (error: Error) => finish(error)
      const timer = setTimeout(() => finish(new Error('Expected startup failure did not exit')), timeout)
      child.once('exit', exit); child.once('error', fail)
      if (ownedExited(child)) finish()
    })
    await this.flush(child)
  }

  async flush(child: ChildProcess) {
    const entry = this.children.find(item => item.child === child)
    if (!entry || !ownedExited(child)) throw new Error('Only exited owned process logs can be finalized')
    let timer: ReturnType<typeof setTimeout> | undefined
    try {
      await Promise.race([entry.closed, new Promise<never>((_done, reject) => { timer = setTimeout(() => reject(new Error('Owned stdio close exceeded 15000ms')), 15_000) })])
      if (!entry.log.writableFinished) await new Promise<void>((done, reject) => { entry.log.once('error', reject); entry.log.end(done) })
    } finally { if (timer) clearTimeout(timer) }
  }

  stop(child: ChildProcess) { return stopOwned(child, this.children.find(item => item.child === child)?.graceful) }

  async close() {
    await cleanupOwned(this.children.flatMap(({ child, log }) => [
      () => this.stop(child),
      async () => { if (ownedExited(child)) await this.flush(child) },
      () => { detachOwnedOutput(child); if (log.writableFinished) return; return new Promise<void>((done, reject) => { log.once('error', reject); log.end(done) }) },
    ]))
  }
}
