package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/cenkalti/backoff/v5"

	"github.com/icholy/gritz/internal/gritzclient"
	gritzv1 "github.com/icholy/gritz/internal/proto/gritz/v1"
	"github.com/icholy/gritz/internal/x/common"
	"github.com/icholy/gritz/internal/x/wakeup"
)

// Shipper defaults. They are fields on LogShipper rather than constants used
// directly so tests can shrink them; production always takes these.
const (
	// defaultLogChunkSize is the buffered-byte threshold that cuts a chunk. It
	// matches shell.Serve's PTY pump: big enough that per-request overhead is
	// negligible, small enough to keep a chatty run's chunks flowing.
	defaultLogChunkSize = 32 << 10 // 32 KiB
	// defaultLogFlushInterval cuts a partial chunk when it fires with pending
	// bytes, so a quiet run still ships promptly instead of sitting in the
	// buffer until the next 32 KiB.
	defaultLogFlushInterval = 2 * time.Second
	// defaultMaxPendingLogBytes caps the unsent bytes held in memory (buffered
	// plus queued). Past it, writes are dropped rather than allowed to grow
	// without bound while the server is unreachable.
	defaultMaxPendingLogBytes = 1 << 20 // 1 MiB
)

// logChunk is one queued unit of work: an opaque byte run stamped with the run
// version that was current when it was cut.
type logChunk struct {
	version int64
	data    []byte
}

// LogShipper is an io.Writer that mirrors log bytes to the server as
// asynchronous chunks, one per AppendLogChunk request. Write never blocks and
// never returns an error; a full buffer drops bytes rather than stall the run.
//
// It borrows the shape of the runner's outbox.Outbox — FIFO delivery,
// head-of-line retry with backoff, permanent-vs-transient classification —
// without its on-disk durability: log bytes are diagnostics whose durable copy
// is already in /gritz/log, so persisting them a second time to ship them is
// not worth the write amplification. Losing an unshipped tail on a crash is
// accepted; losing a lifecycle event is not, which is why the runner's outbox
// stays durable and this does not.
//
// Shipping is best-effort end to end: a slow or unreachable server never blocks
// a write, never fails a run, and at worst loses log bytes.
type LogShipper struct {
	client gritzclient.Client
	taskID int64

	// Tunables, defaulted by NewLogShipper. Tests shrink them before Run; they
	// are not mutated once the sender is running.
	chunkSize     int
	flushInterval time.Duration
	maxPending    int
	backoff       backoff.BackOff
	log           *slog.Logger

	// notify wakes the sender when a chunk is cut.
	notify wakeup.Chan
	// sendSem admits one sender at a time. Run and Flush both drain, so the
	// semaphore — not the single Run goroutine — is what guarantees never more
	// than one request in flight, and therefore that the server's insertion
	// order is write order. It is a channel rather than a mutex so a caller can
	// give up on ctx instead of blocking on a sender that is mid-backoff.
	sendSem chan struct{}
	// failing suppresses a warning per retry, logging one per failure streak
	// instead. Guarded by sendSem, not mu.
	failing bool

	mu           sync.Mutex
	version      int64
	buf          []byte
	pending      []logChunk
	pendingBytes int
	dropped      int
}

var _ io.Writer = (*LogShipper)(nil)

// NewLogShipper returns a shipper that mirrors everything written to it to the
// server as log chunks for taskID. It buffers from construction with version 0
// — the pre-run preamble — until SetVersion stamps a run. Nothing is sent until
// Run (or Flush) drains it.
func NewLogShipper(client gritzclient.Client, taskID int64) *LogShipper {
	return &LogShipper{
		client:        client,
		taskID:        taskID,
		chunkSize:     defaultLogChunkSize,
		flushInterval: defaultLogFlushInterval,
		maxPending:    defaultMaxPendingLogBytes,
		backoff:       backoff.NewExponentialBackOff(),
		// Deliberately not the driver's logger: the driver's slog handler writes
		// through the same sink this shipper is teed into, so logging a send
		// failure there would buffer a line that fails to ship, logging another
		// line, and so on. os.Stderr is outside the tee (it reaches docker logs)
		// and breaks the loop.
		log:     slog.New(slog.NewTextHandler(os.Stderr, nil)),
		notify:  wakeup.New(),
		sendSem: make(chan struct{}, 1),
	}
}

// SetVersion stamps subsequent bytes with version, cutting a chunk at the
// boundary so pre-run preamble and run bytes never share one. Because a request
// carries exactly one chunk, no request can straddle a version boundary and the
// sender needs no splitting logic.
func (s *LogShipper) SetVersion(version int64) {
	s.mu.Lock()
	cut := s.cutLocked()
	s.version = version
	s.mu.Unlock()
	if cut {
		s.notify.Wake()
	}
}

// Write buffers p for shipping. It always reports the full write and a nil
// error: the caller is a log tee that must not learn about network trouble, and
// its other writers (the /gritz/log file, os.Stderr) still hold the bytes.
// Bytes past the pending cap are dropped and counted, and the gap is later
// reported in the transcript as a synthetic marker chunk.
func (s *LogShipper) Write(p []byte) (int, error) {
	s.mu.Lock()
	cut := s.appendLocked(p)
	s.mu.Unlock()
	if cut {
		s.notify.Wake()
	}
	return len(p), nil
}

// Run drains pending chunks until ctx is cancelled, cutting a partial chunk
// every flushInterval so a trickle of output still ships. It is the shipper's
// background sender and is expected to run for the driver's lifetime.
func (s *LogShipper) Run(ctx context.Context) {
	ticker := time.NewTicker(s.flushInterval)
	defer ticker.Stop()
	for {
		s.drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.mu.Lock()
			s.cutLocked()
			s.mu.Unlock()
		case <-s.notify:
		}
	}
}

// Flush cuts whatever is buffered and sends everything pending, bounded by ctx.
// It returns an error only when ctx expires with chunks still unsent — the
// caller is expected to log it and carry on, since the full log remains in
// /gritz/log. It is safe to call while Run is sending; the two never have two
// requests in flight.
func (s *LogShipper) Flush(ctx context.Context) error {
	s.mu.Lock()
	s.cutLocked()
	// A drop streak that never saw another accepted write has no later chunk to
	// precede, so record it here rather than lose the accounting.
	s.markDroppedLocked()
	s.mu.Unlock()
	if !s.drain(ctx) {
		return fmt.Errorf("flushing log chunks: %w", ctx.Err())
	}
	return nil
}

// appendLocked buffers p, dropping it when the pending cap is reached, and
// reports whether a chunk was queued.
func (s *LogShipper) appendLocked(p []byte) bool {
	if len(p) == 0 {
		return false
	}
	if s.pendingBytes+len(s.buf)+len(p) > s.maxPending {
		s.dropped += len(p)
		return false
	}
	cut := false
	if s.dropped > 0 {
		// Shipping has resumed. The gap sits between the bytes already buffered
		// and these first accepted ones, so cut there and splice the marker in
		// between to put it where the loss actually happened.
		s.cutLocked()
		s.markDroppedLocked()
		cut = true
	}
	s.buf = append(s.buf, p...)
	if s.cutFullLocked() {
		cut = true
	}
	return cut
}

// cutLocked queues everything buffered, including a partial trailing chunk. It
// reports whether anything was queued.
func (s *LogShipper) cutLocked() bool {
	cut := s.cutFullLocked()
	if len(s.buf) > 0 {
		s.queueLocked(s.buf)
		cut = true
	}
	// Drop the window on the queued array so the next write allocates instead
	// of appending next to chunks that are still in flight.
	s.buf = nil
	return cut
}

// cutFullLocked queues whole chunkSize-sized chunks and leaves any remainder
// buffered to coalesce with the next write. Cutting at a fixed size (rather
// than at whatever a single write happened to deliver) is also what keeps a
// huge write from becoming a request over the server's per-chunk cap.
func (s *LogShipper) cutFullLocked() bool {
	cut := false
	for len(s.buf) >= s.chunkSize {
		// Full slice expression: the queued chunk aliases buf's array, and
		// capping cap keeps a later append from writing into it.
		s.queueLocked(s.buf[:s.chunkSize:s.chunkSize])
		s.buf = s.buf[s.chunkSize:]
		cut = true
	}
	return cut
}

// markDroppedLocked queues the synthetic marker for a finished drop streak, so
// the gap is visible in the transcript rather than silently missing.
func (s *LogShipper) markDroppedLocked() {
	if s.dropped == 0 {
		return
	}
	s.queueLocked(fmt.Appendf(nil, "[gritz: dropped %d log bytes]\n", s.dropped))
	s.dropped = 0
}

// queueLocked appends data to the send queue, stamped with the current version.
func (s *LogShipper) queueLocked(data []byte) {
	s.pending = append(s.pending, logChunk{version: s.version, data: data})
	s.pendingBytes += len(data)
}

// head returns the chunk at the front of the send queue.
func (s *LogShipper) head() (logChunk, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) == 0 {
		return logChunk{}, false
	}
	return s.pending[0], true
}

// pop removes the delivered head, freeing its bytes against the pending cap.
func (s *LogShipper) pop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingBytes -= len(s.pending[0].data)
	s.pending = s.pending[1:]
}

// drain sends pending chunks FIFO, one AppendLogChunk per chunk, until the
// queue is empty or ctx is done; it reports whether the queue drained. A
// transient failure retries the same head after a backoff (head-of-line
// blocking, as the outbox does) so a retry can never be overtaken by a newer
// chunk; a permanent one drops the head and advances rather than wedge the
// queue forever.
func (s *LogShipper) drain(ctx context.Context) bool {
	select {
	case s.sendSem <- struct{}{}:
	case <-ctx.Done():
		return false
	}
	defer func() { <-s.sendSem }()
	for {
		if ctx.Err() != nil {
			return false
		}
		chunk, ok := s.head()
		if !ok {
			s.backoff.Reset()
			return true
		}
		_, err := s.client.AppendLogChunk(ctx, &gritzv1.AppendLogChunkRequest{
			TaskId:  s.taskID,
			Version: chunk.version,
			Data:    chunk.data,
		})
		switch {
		case err == nil:
			s.failing = false
			s.pop()
		case isPermanentChunkError(err):
			s.log.Warn("dropping undeliverable log chunk", "task", s.taskID, "bytes", len(chunk.data), "err", err)
			s.pop()
		default:
			if !s.failing {
				s.failing = true
				s.log.Warn("log chunk send failed, will retry", "task", s.taskID, "err", err)
			}
			if !common.SleepContext(ctx, s.backoff.NextBackOff()) {
				return false
			}
		}
	}
}

// isPermanentChunkError reports whether err will never succeed on retry (the
// task is gone, the token cannot write it, the chunk is malformed). It mirrors
// the runner outbox's classification; the shipper drops such chunks instead of
// dead-lettering them, since a diagnostic byte run has nowhere to go.
func isPermanentChunkError(err error) bool {
	switch connect.CodeOf(err) {
	case connect.CodeNotFound, connect.CodeInvalidArgument, connect.CodePermissionDenied:
		return true
	default:
		return false
	}
}
