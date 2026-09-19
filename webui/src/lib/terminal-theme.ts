import type { CSSProperties } from 'react'

// The appearance of a raw terminal transcript, shared by the two tabs that show
// one: the shell tab's xterm instance (webui/src/components/task-shell.tsx) and
// the logs tab's <pre> (webui/src/components/task-logs.tsx). They only read as
// the same surface if there is a single source of truth for the values, so the
// literals live here and both components import them.
//
// Every value matches xterm's own default except `background`, so handing them
// back to the Terminal constructor leaves the shell rendering exactly as before
// — it just makes the values explicit for the DOM side to reuse.
export const terminalTheme = {
  background: '#0a0a0a',
  foreground: '#ffffff',
  fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
  fontSize: 13,
  lineHeight: 1,
} as const

// terminalSurfaceStyle backs an element with the terminal's background.
export const terminalSurfaceStyle: CSSProperties = {
  backgroundColor: terminalTheme.background,
}

// terminalOverlayStyle backs an overlay that dims the terminal behind it.
export const terminalOverlayStyle: CSSProperties = {
  backgroundColor: `color-mix(in srgb, ${terminalTheme.background} 80%, transparent)`,
}

// terminalTextStyle types DOM text with the terminal's metrics and colour.
export const terminalTextStyle: CSSProperties = {
  color: terminalTheme.foreground,
  fontFamily: terminalTheme.fontFamily,
  fontSize: terminalTheme.fontSize,
  lineHeight: terminalTheme.lineHeight,
}
