import { describe, it, expect, vi } from 'vitest'
import { ShellSessions, isShellActive } from './shell-sessions'
import type { Client } from '@connectrpc/connect'
import type { GritzService } from '@/gen/gritz/v1/gritz_pb'

// These tests exercise the entry-lifecycle logic and never call open(), so the
// client is never invoked; a bare cast stands in for it.
const stubClient = {} as Client<typeof GritzService>

const KEY = '7'

function newSessions() {
  return new ShellSessions({ client: stubClient })
}

describe('ShellSessions persistence', () => {
  it('creates an entry on attach', () => {
    const s = newSessions()
    s.attach(KEY, '0')
    expect(s.has(KEY)).toBe(true)
  })

  it('persists a session across navigation: detach does not remove it', () => {
    const s = newSessions()
    s.attach(KEY, '0')
    // Leaving the shell page (and any StrictMode cleanup) must not end the session.
    s.detach(KEY)
    expect(s.has(KEY)).toBe(true)
  })

  it('survives the StrictMode attach → detach → attach cycle', () => {
    const s = newSessions()
    s.attach(KEY, '0')
    s.detach(KEY)
    s.attach(KEY, '0')
    expect(s.has(KEY)).toBe(true)
  })

  it('close removes the entry and notifies subscribers', () => {
    const s = newSessions()
    const listener = vi.fn()
    s.subscribe(KEY, listener)
    s.attach(KEY, '0')

    s.close(KEY)

    expect(s.has(KEY)).toBe(false)
    expect(listener).toHaveBeenCalled()
    expect(s.getSnapshot(KEY).phase).toBe('idle')
  })

  it('detach and close on an unknown task are no-ops', () => {
    const s = newSessions()
    expect(() => s.detach('nope')).not.toThrow()
    expect(() => s.close('nope')).not.toThrow()
    expect(s.has('nope')).toBe(false)
  })

  it('reports the IDLE snapshot for a task with no entry', () => {
    const s = newSessions()
    expect(s.getSnapshot('missing')).toEqual({
      phase: 'idle',
      exitCode: null,
      error: null,
      started: false,
    })
  })

  it('replays scrollback is empty for a fresh entry (subscribeOutput no-ops)', () => {
    const s = newSessions()
    s.attach(KEY, '0')
    const sink = vi.fn()
    const unsubscribe = s.subscribeOutput(KEY, sink)
    // No output has been produced, so nothing is replayed.
    expect(sink).not.toHaveBeenCalled()
    unsubscribe()
  })
})

describe('isShellActive', () => {
  it('is true only while a socket is opening or live', () => {
    expect(isShellActive('opening')).toBe(true)
    expect(isShellActive('starting')).toBe(true)
    expect(isShellActive('connected')).toBe(true)
  })

  it('is false when idle or in a terminal/closed phase', () => {
    expect(isShellActive('idle')).toBe(false)
    expect(isShellActive('exited')).toBe(false)
    expect(isShellActive('detached')).toBe(false)
    expect(isShellActive('error')).toBe(false)
  })
})
