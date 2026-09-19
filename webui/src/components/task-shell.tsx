import { useEffect, useRef } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { Button } from '@/components/ui/button'
import { Loader2 } from 'lucide-react'
import { useShellSessions } from '@/lib/services'
import { terminalOverlayStyle, terminalSurfaceStyle, terminalTheme } from '@/lib/terminal-theme'
import { useShellState } from '@/hooks/use-shell-state'
import type { ShellPhase } from '@/lib/shell-sessions'

interface TaskShellProps {
  taskId: bigint
  // orgId is the active org, forwarded to the singleton for the cookie-
  // authenticated attach leg (see the SSE stream).
  orgId: string
}

// TaskShell renders an xterm.js terminal bound to the task's shell. The session
// and attach socket are owned by the ShellSessions singleton (outside React);
// this component owns only the terminal instance (cheap to recreate, no shared
// server state) and registers interest so the singleton keeps the socket alive
// across a StrictMode remount.
export function TaskShell({ taskId, orgId }: TaskShellProps) {
  const shell = useShellSessions()
  const key = String(taskId)
  const { phase, exitCode } = useShellState(key)
  const containerRef = useRef<HTMLDivElement>(null)

  // Register interest for the lifetime of this component. detach is grace-delayed
  // in the singleton, so StrictMode's mount→cleanup→mount nets one live socket.
  useEffect(() => {
    shell.attach(key, orgId)
    return () => shell.detach(key)
  }, [shell, key, orgId])

  // The terminal streams output from the singleton and forwards input/resize to
  // it. subscribeOutput replays the retained scrollback, so a remount (StrictMode
  // or navigation) re-renders what the session already produced.
  useEffect(() => {
    const container = containerRef.current
    if (!container) return

    const term = new Terminal({
      cursorBlink: true,
      convertEol: false,
      fontFamily: terminalTheme.fontFamily,
      fontSize: terminalTheme.fontSize,
      lineHeight: terminalTheme.lineHeight,
      theme: { background: terminalTheme.background, foreground: terminalTheme.foreground },
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(container)
    fit.fit()

    const encoder = new TextEncoder()
    const onData = term.onData((d) => shell.sendData(key, encoder.encode(d)))
    const sendResize = () => shell.sendResize(key, term.rows, term.cols)
    sendResize()

    const unsubscribe = shell.subscribeOutput(key, (bytes) => term.write(bytes))
    const observer = new ResizeObserver(() => {
      fit.fit()
      sendResize()
    })
    observer.observe(container)
    term.focus()

    return () => {
      observer.disconnect()
      onData.dispose()
      unsubscribe()
      term.dispose()
    }
  }, [shell, key])

  return (
    <div className="relative h-full w-full" style={terminalSurfaceStyle}>
      <div ref={containerRef} className="h-full w-full p-2" />
      {renderOverlay(phase, exitCode, () => shell.open(key, orgId))}
    </div>
  )
}

function renderOverlay(phase: ShellPhase, exitCode: number | null, onReconnect: () => void) {
  if (phase === 'connected') return null

  if (phase === 'opening' || phase === 'starting') {
    return (
      <Centered>
        <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
        <p className="text-sm text-muted-foreground">Starting sandbox…</p>
      </Centered>
    )
  }

  const message =
    phase === 'exited'
      ? `Shell exited${exitCode !== null ? ` (code ${exitCode})` : ''}.`
      : phase === 'detached'
        ? 'This shell is already attached in another session.'
        : 'The shell connection was lost.'

  return (
    <Centered>
      <p className="text-sm text-muted-foreground">{message}</p>
      <Button size="sm" variant="outline" onClick={onReconnect}>
        Reconnect
      </Button>
    </Centered>
  )
}

function Centered({ children }: { children: React.ReactNode }) {
  return (
    <div
      className="absolute inset-0 flex flex-col items-center justify-center gap-3"
      style={terminalOverlayStyle}
    >
      {children}
    </div>
  )
}
