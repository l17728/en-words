import type { ChildProcess } from 'node:child_process'

export function ownedExited(child: ChildProcess): boolean {
  return child.exitCode !== null || child.signalCode !== null
}

export async function stopOwned(owned: ChildProcess | undefined, graceful = false): Promise<void> {
  if (!owned || ownedExited(owned)) return
  await new Promise<void>((done, reject) => {
    let settled = false
    const finish = (error?: Error) => {
      if (settled) return
      settled = true
      clearTimeout(timer)
      owned.removeListener('exit', onExit)
      owned.removeListener('error', onError)
      if (error) reject(error); else done()
    }
    const onExit = () => finish()
    const onError = (error: Error) => finish(error)
    const timer = setTimeout(() => {
      try { owned.kill() } catch { /* Report the bounded stop failure below. */ }
      finish(new Error('Owned backend did not exit'))
    }, 15_000)
    owned.once('exit', onExit)
    owned.once('error', onError)
    try {
      if (ownedExited(owned)) { finish(); return }
      if (graceful) owned.stdin?.end(); else owned.kill()
    } catch (error) { finish(error instanceof Error ? error : new Error('Owned backend stop failed')) }
  })
}

export async function cleanupOwned(actions: (() => void | Promise<void>)[]): Promise<void> {
  const errors: unknown[] = []
  for (const action of actions) {
    try { await action() } catch (error) { errors.push(error) }
  }
  if (errors.length) throw new AggregateError(errors, 'Owned fixture cleanup failed')
}


export function detachOwnedOutput(child: ChildProcess | undefined): void {
  for (const stream of [child?.stdout, child?.stderr]) {
    stream?.unpipe()
    stream?.resume() // Drain without writing to an already closed log, even after a failed kill.
  }
}
