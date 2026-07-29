// TypeScript port of internal/shell/shellwire/shellwire.go — the end-to-end
// framing for the driver reverse shell. Frames travel as binary WebSocket
// messages between the driver (which owns the PTY) and the operator's client.
// The server-side relay passes them through opaquely and never parses them, so
// this codec is a contract between the two endpoints only; it must stay byte-for-
// byte compatible with the Go definition.
//
// A frame is [1-byte type][payload]:
//
//   0x00 data   — raw PTY bytes (both directions; a PTY master is one stream)
//   0x01 resize — terminal size, two big-endian uint16s: rows then cols
//   0x02 exit   — shell exit code, one big-endian int32
//   0x03 ping   — keepalive, no payload

// Subprotocol is the WebSocket subprotocol version token negotiated on the
// attach leg (Sec-WebSocket-Protocol: gritz-shell.v1). It carries no
// credential — the browser authenticates via its cookie session.
export const SUBPROTOCOL = 'gritz-shell.v1'

// READ_LIMIT mirrors shellwire.ReadLimit (1 MiB). The browser has no equivalent
// of coder/websocket's SetReadLimit, but the constant is exported so callers can
// reason about the maximum frame the driver may send.
export const READ_LIMIT = 1 << 20

// FrameType is the one-byte frame discriminator, matching shellwire.Type.
export enum FrameType {
  Data = 0x00,
  Resize = 0x01,
  Exit = 0x02,
  Ping = 0x03,
}

// A decoded wire frame. Only the field relevant to `type` is populated: `data`
// for Data, `rows`/`cols` for Resize, `code` for Exit, nothing for Ping.
export interface Frame {
  type: number
  data?: Uint8Array
  rows?: number
  cols?: number
  code?: number
}

// encodeData encodes a data frame carrying raw PTY bytes: [0x00, ...bytes]. The
// result is backed by a plain ArrayBuffer (never a SharedArrayBuffer) so it
// satisfies WebSocket.send's BufferSource parameter.
export function encodeData(bytes: Uint8Array): Uint8Array<ArrayBuffer> {
  const msg = new Uint8Array(1 + bytes.length)
  msg[0] = FrameType.Data
  msg.set(bytes, 1)
  return msg
}

// encodeResize encodes a resize frame from rows and cols as two big-endian
// uint16s: [0x01, rows>>8, rows, cols>>8, cols].
export function encodeResize(rows: number, cols: number): Uint8Array<ArrayBuffer> {
  const msg = new Uint8Array(5)
  const view = new DataView(msg.buffer)
  msg[0] = FrameType.Resize
  view.setUint16(1, rows & 0xffff, false)
  view.setUint16(3, cols & 0xffff, false)
  return msg
}

// parse decodes a wire message into a Frame. It throws on an empty message,
// mirroring shellwire.Parse. Resize dimensions and exit codes are decoded eagerly
// so callers switch on `type` and read the matching field.
export function parse(msg: Uint8Array): Frame {
  if (msg.length === 0) {
    throw new Error('shellwire: empty frame')
  }
  const type = msg[0]
  const payload = msg.subarray(1)
  switch (type) {
    case FrameType.Resize: {
      if (payload.length !== 4) {
        throw new Error(`shellwire: resize payload must be 4 bytes, got ${payload.length}`)
      }
      const view = new DataView(payload.buffer, payload.byteOffset, payload.byteLength)
      return { type, rows: view.getUint16(0, false), cols: view.getUint16(2, false) }
    }
    case FrameType.Exit: {
      if (payload.length !== 4) {
        throw new Error(`shellwire: exit payload must be 4 bytes, got ${payload.length}`)
      }
      const view = new DataView(payload.buffer, payload.byteOffset, payload.byteLength)
      return { type, code: view.getInt32(0, false) }
    }
    case FrameType.Ping:
      return { type }
    default:
      return { type, data: payload }
  }
}
