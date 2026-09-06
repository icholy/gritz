// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { useVisibilityInterval } from './use-visibility-interval'

// Drive document.hidden from a variable so we can flip visibility, and fire a
// real visibilitychange event so the hook's listener runs like it would in a
// browser.
let hidden = false
let root: Root | undefined

async function render(useHook: () => void) {
  const container = document.createElement('div')
  await act(async () => {
    root = createRoot(container)
    root.render(
      createElement(function Probe() {
        useHook()
        return null
      }),
    )
  })
}

async function setHidden(value: boolean) {
  hidden = value
  await act(async () => {
    document.dispatchEvent(new Event('visibilitychange'))
  })
}

beforeEach(() => {
  hidden = false
  Object.defineProperty(document, 'hidden', { configurable: true, get: () => hidden })
  vi.useFakeTimers()
})

afterEach(async () => {
  await act(async () => root?.unmount())
  root = undefined
  vi.useRealTimers()
})

describe('useVisibilityInterval', () => {
  it('drives the callback on each interval while the tab is visible', async () => {
    const cb = vi.fn()
    await render(() => useVisibilityInterval(cb, 30_000))

    await act(async () => vi.advanceTimersByTime(90_000))

    expect(cb).toHaveBeenCalledTimes(3)
  })

  it('pauses when given a null delay', async () => {
    const cb = vi.fn()
    await render(() => useVisibilityInterval(cb, null))

    await act(async () => vi.advanceTimersByTime(90_000))

    expect(cb).not.toHaveBeenCalled()
  })

  it('does not fire while the document is hidden', async () => {
    hidden = true
    const cb = vi.fn()
    await render(() => useVisibilityInterval(cb, 30_000))

    await act(async () => vi.advanceTimersByTime(90_000))

    expect(cb).not.toHaveBeenCalled()
  })

  it('stops firing once the tab is backgrounded', async () => {
    const cb = vi.fn()
    await render(() => useVisibilityInterval(cb, 30_000))

    await act(async () => vi.advanceTimersByTime(30_000))
    expect(cb).toHaveBeenCalledTimes(1)

    await setHidden(true)
    await act(async () => vi.advanceTimersByTime(90_000))
    expect(cb).toHaveBeenCalledTimes(1)
  })

  it('resumes firing when the tab is foregrounded again', async () => {
    hidden = true
    const cb = vi.fn()
    await render(() => useVisibilityInterval(cb, 30_000))

    await act(async () => vi.advanceTimersByTime(30_000))
    expect(cb).not.toHaveBeenCalled()

    await setHidden(false)
    await act(async () => vi.advanceTimersByTime(30_000))
    expect(cb).toHaveBeenCalledTimes(1)
  })

  it('stops the interval after unmount', async () => {
    const cb = vi.fn()
    await render(() => useVisibilityInterval(cb, 30_000))

    await act(async () => vi.advanceTimersByTime(30_000))
    expect(cb).toHaveBeenCalledTimes(1)

    await act(async () => root?.unmount())
    root = undefined
    await act(async () => vi.advanceTimersByTime(90_000))
    expect(cb).toHaveBeenCalledTimes(1)
  })
})
