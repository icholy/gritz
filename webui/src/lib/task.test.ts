import { describe, it, expect } from 'vitest'
import { taskLabel, taskSearchValue, toTaskTab } from './task'

describe('taskLabel', () => {
  it('uses the name when the agent has set one', () => {
    expect(taskLabel({ id: 42n, name: 'fix the flaky test' })).toBe('fix the flaky test')
  })

  it('falls back to the id while the task is still unnamed', () => {
    expect(taskLabel({ id: 42n, name: '' })).toBe('Unnamed - 42')
  })
})

describe('taskSearchValue', () => {
  it('appends the id so same-named tasks stay distinct', () => {
    const a = taskSearchValue({ id: 1n, name: 'deploy' })
    const b = taskSearchValue({ id: 2n, name: 'deploy' })
    expect(a).not.toBe(b)
    expect(a).toBe('deploy 1')
  })

  it('keeps the id searchable for unnamed tasks', () => {
    expect(taskSearchValue({ id: 7n, name: '' })).toBe('Unnamed - 7 7')
  })
})

describe('toTaskTab', () => {
  it('passes through the non-default views', () => {
    expect(toTaskTab('shell')).toBe('shell')
  })

  it('falls back to timeline for the default and any unknown value', () => {
    expect(toTaskTab('timeline')).toBe('timeline')
    // links stopped being a tab when they moved into the sidebar; stale deep
    // links fall back to the timeline.
    expect(toTaskTab('links')).toBe('timeline')
    expect(toTaskTab('bogus')).toBe('timeline')
    expect(toTaskTab(undefined)).toBe('timeline')
    expect(toTaskTab(42)).toBe('timeline')
  })
})
