import { describe, it, expect } from 'vitest'
import { create } from '@bufbuild/protobuf'
import type { InfiniteData } from '@tanstack/react-query'
import {
  ListLogChunksByTaskResponseSchema,
  type ListLogChunksByTaskResponse,
} from '@/gen/gritz/v1/gritz_pb'
import { decodeChunks, dropTrailingEmpty } from './use-task-logs'

const encoder = new TextEncoder()

function page(...chunkData: Uint8Array[]): ListLogChunksByTaskResponse {
  return create(ListLogChunksByTaskResponseSchema, {
    chunks: chunkData.map((data) => ({ data })),
  })
}

function infinite(
  ...pages: ListLogChunksByTaskResponse[]
): InfiniteData<ListLogChunksByTaskResponse> {
  return { pages, pageParams: pages.map((_, i) => `token-${i}`) }
}

describe('decodeChunks', () => {
  it('returns an empty string with no data loaded', () => {
    expect(decodeChunks(undefined)).toBe('')
  })

  it('concatenates chunks across pages into transcript order', () => {
    const data = infinite(
      page(encoder.encode('one\n'), encoder.encode('two\n')),
      page(encoder.encode('three\n')),
    )
    expect(decodeChunks(data)).toBe('one\ntwo\nthree\n')
  })

  it('decodes a multi-byte character straddling a chunk boundary', () => {
    // "é" is 0xC3 0xA9; the shipper cuts chunks at byte counts, so the two
    // bytes can land in different chunks. Joining before decoding must not
    // produce replacement characters.
    const bytes = encoder.encode('café')
    const data = infinite(page(bytes.slice(0, 4), bytes.slice(4)))
    expect(decodeChunks(data)).toBe('café')
  })

  it('strips ANSI escape sequences', () => {
    const data = infinite(page(encoder.encode('\x1b[31mred\x1b[0m plain \x1b]0;title\x07done\n')))
    expect(decodeChunks(data)).toBe('red plain done\n')
  })
})

describe('dropTrailingEmpty', () => {
  it('trims trailing empty pages along with their page params', () => {
    const data = infinite(page(encoder.encode('a')), page(), page())
    const trimmed = dropTrailingEmpty(data)
    expect(trimmed.pages).toHaveLength(1)
    expect(trimmed.pageParams).toEqual(['token-0'])
  })

  it('keeps at least one page even when all are empty', () => {
    const data = infinite(page(), page())
    const trimmed = dropTrailingEmpty(data)
    expect(trimmed.pages).toHaveLength(1)
  })

  it('returns the input unchanged when the last page has chunks', () => {
    const data = infinite(page(), page(encoder.encode('a')))
    expect(dropTrailingEmpty(data)).toBe(data)
  })
})
