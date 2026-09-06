import { useEffect, useState } from 'react'
import { useInterval } from 'usehooks-ts'

// useVisibilityInterval calls `callback` every `delayMs`, but only while the tab
// is foregrounded. It's built on usehooks-ts' useInterval, which pauses when
// given a null delay — so a hidden tab simply passes null, and a caller can pass
// null itself to pause the interval outright. Visibility is tracked via the
// document's visibilitychange event (usehooks-ts has no visibility hook).
// Skipping hidden tabs matters because a backgrounded tab has closed the SSE,
// and foregrounding reconnects and resyncs, so polling while hidden is wasted
// work.
export function useVisibilityInterval(callback: () => void, delayMs: number | null): void {
  const [hidden, setHidden] = useState(() => document.hidden)
  useEffect(() => {
    const onVisibilityChange = () => setHidden(document.hidden)
    document.addEventListener('visibilitychange', onVisibilityChange)
    return () => document.removeEventListener('visibilitychange', onVisibilityChange)
  }, [])
  useInterval(callback, hidden ? null : delayMs)
}
