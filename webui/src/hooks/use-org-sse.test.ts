import { describe, it, expect } from 'vitest'
import { QueryClient } from '@tanstack/react-query'
import { create } from '@bufbuild/protobuf'
import { createConnectQueryKey } from '@connectrpc/connect-query'
import { getSchedule, listSchedules } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { GetScheduleResponseSchema, ListSchedulesResponseSchema } from '@/gen/gritz/v1/gritz_pb'
import { TimelineFollowers } from '@/lib/timeline-follow'
import { handleNotification, handleReconnect } from './use-org-sse'
import type { Notification } from '@/lib/notification-sse'

function scheduleKey(id: bigint) {
  return createConnectQueryKey({ schema: getSchedule, input: { id }, cardinality: 'finite' })
}

function listKey() {
  return createConnectQueryKey({ schema: listSchedules, cardinality: 'finite' })
}

function schedule(id: bigint, name: string) {
  return create(GetScheduleResponseSchema, { schedule: { id, name } })
}

function change(type: string, id: number): Notification {
  return {
    type: 'change',
    resources: [{ action: 'updated', type, id }],
    org_id: 1,
    timestamp: new Date().toISOString(),
  }
}

// A cached query is only refetched on the next mount if it was invalidated, so
// the assertion is on the invalidated flag rather than on a request going out.
function invalidated(qc: QueryClient, key: unknown[]): boolean {
  return qc.getQueryState(key)?.isInvalidated ?? false
}

describe('handleNotification', () => {
  it('invalidates the edited schedule and the schedule list', () => {
    const qc = new QueryClient()
    qc.setQueryData(scheduleKey(7n), schedule(7n, 'old'))
    qc.setQueryData(listKey(), create(ListSchedulesResponseSchema, {}))

    handleNotification(qc, new TimelineFollowers(), change('schedule', 7))

    expect(invalidated(qc, scheduleKey(7n))).toBe(true)
    expect(invalidated(qc, listKey())).toBe(true)
  })

  it('leaves other schedules cached', () => {
    const qc = new QueryClient()
    qc.setQueryData(scheduleKey(7n), schedule(7n, 'old'))
    qc.setQueryData(scheduleKey(8n), schedule(8n, 'other'))

    handleNotification(qc, new TimelineFollowers(), change('schedule', 7))

    expect(invalidated(qc, scheduleKey(8n))).toBe(false)
  })
})

describe('handleReconnect', () => {
  it('resyncs schedules after a dropped SSE connection', () => {
    const qc = new QueryClient()
    qc.setQueryData(scheduleKey(7n), schedule(7n, 'old'))
    qc.setQueryData(listKey(), create(ListSchedulesResponseSchema, {}))

    handleReconnect(qc, new TimelineFollowers())

    expect(invalidated(qc, scheduleKey(7n))).toBe(true)
    expect(invalidated(qc, listKey())).toBe(true)
  })
})
