package logship

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/cenkalti/backoff/v5"
	"github.com/icholy/replace"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/icholy/gritz/internal/gritzclient"
	gritzv1 "github.com/icholy/gritz/internal/proto/gritz/v1"
	"github.com/icholy/gritz/internal/x/testx"
)

func TestShipper_Flush(t *testing.T) {
	t.Parallel()
	// Arrange
	client := &gritzclient.ClientMock{
		AppendLogChunkFunc: func(ctx context.Context, req *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	shipper := New(client, 7, nil)

	// Act - a write below the chunk size stays buffered until Flush cuts it
	n, err := shipper.Write([]byte("hello\n"))
	assert.NilError(t, err)
	assert.Equal(t, n, 6)
	assert.Assert(t, cmp.Len(client.AppendedLogChunks(), 0))
	assert.NilError(t, shipper.Flush(t.Context()))

	// Assert
	assert.DeepEqual(t,
		client.AppendedLogChunks(),
		[]*gritzv1.AppendLogChunkRequest{
			{TaskId: 7, Data: []byte("hello\n")},
		},
		protocmp.Transform(),
	)
}

func TestShipper_CutsAtChunkSize(t *testing.T) {
	t.Parallel()
	// Arrange
	client := &gritzclient.ClientMock{
		AppendLogChunkFunc: func(ctx context.Context, req *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	shipper := New(client, 7, nil)
	shipper.chunkSize = 4

	// Act - one oversized write is cut into whole chunks; the 2-byte remainder
	// stays buffered until Flush.
	_, err := shipper.Write([]byte("aaaabbbbcc"))
	assert.NilError(t, err)
	assert.NilError(t, shipper.Flush(t.Context()))

	// Assert
	assert.DeepEqual(t,
		client.AppendedLogChunks(),
		[]*gritzv1.AppendLogChunkRequest{
			{TaskId: 7, Data: []byte("aaaa")},
			{TaskId: 7, Data: []byte("bbbb")},
			{TaskId: 7, Data: []byte("cc")},
		},
		protocmp.Transform(),
	)
}

func TestShipper_SetVersion(t *testing.T) {
	t.Parallel()
	// Arrange
	client := &gritzclient.ClientMock{
		AppendLogChunkFunc: func(ctx context.Context, req *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	shipper := New(client, 7, nil)

	// Act - the pre-run preamble ships as version 0, run bytes as version 3
	_, err := shipper.Write([]byte("preamble\n"))
	assert.NilError(t, err)
	shipper.SetVersion(3)
	_, err = shipper.Write([]byte("run\n"))
	assert.NilError(t, err)
	assert.NilError(t, shipper.Flush(t.Context()))

	// Assert - the boundary cut keeps the two versions in separate chunks
	assert.DeepEqual(t,
		client.AppendedLogChunks(),
		[]*gritzv1.AppendLogChunkRequest{
			{TaskId: 7, Version: 0, Data: []byte("preamble\n")},
			{TaskId: 7, Version: 3, Data: []byte("run\n")},
		},
		protocmp.Transform(),
	)
}

func TestShipper_OrderingAcrossRetries(t *testing.T) {
	t.Parallel()
	// Arrange - the first two attempts fail transiently
	var attempts atomic.Int64
	client := &gritzclient.ClientMock{
		AppendLogChunkFunc: func(ctx context.Context, req *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			if attempts.Add(1) <= 2 {
				return nil, connect.NewError(connect.CodeUnavailable, errors.New("server unreachable"))
			}
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	shipper := New(client, 7, nil)
	shipper.chunkSize = 4
	shipper.backoff = backoff.NewConstantBackOff(time.Millisecond)
	shipper.log = slog.New(slog.DiscardHandler)

	// Act
	for _, w := range []string{"aaaa", "bbbb", "cccc"} {
		_, err := shipper.Write([]byte(w))
		assert.NilError(t, err)
	}
	assert.NilError(t, shipper.Flush(t.Context()))

	// Assert - the failed head is retried before anything newer is sent, so the
	// order the server sees is still write order.
	assert.DeepEqual(t,
		client.AppendedLogChunks(),
		[]*gritzv1.AppendLogChunkRequest{
			{TaskId: 7, Data: []byte("aaaa")},
			{TaskId: 7, Data: []byte("aaaa")},
			{TaskId: 7, Data: []byte("aaaa")},
			{TaskId: 7, Data: []byte("bbbb")},
			{TaskId: 7, Data: []byte("cccc")},
		},
		protocmp.Transform(),
	)
}

func TestShipper_PermanentErrorDropsChunk(t *testing.T) {
	t.Parallel()
	// Arrange - the server rejects one chunk in a way no retry can fix
	client := &gritzclient.ClientMock{
		AppendLogChunkFunc: func(ctx context.Context, req *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			if string(req.Data) == "bad\n" {
				return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("data is required"))
			}
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	shipper := New(client, 7, nil)
	shipper.chunkSize = 4
	shipper.log = slog.New(slog.DiscardHandler)

	// Act
	_, err := shipper.Write([]byte("bad\nok!\n"))
	assert.NilError(t, err)
	assert.NilError(t, shipper.Flush(t.Context()))

	// Assert - the rejected chunk is dropped rather than retried forever, and
	// the queue behind it still drains.
	assert.DeepEqual(t,
		client.AppendedLogChunks(),
		[]*gritzv1.AppendLogChunkRequest{
			{TaskId: 7, Data: []byte("bad\n")},
			{TaskId: 7, Data: []byte("ok!\n")},
		},
		protocmp.Transform(),
	)
}

func TestShipper_OverflowDropsAndAccounts(t *testing.T) {
	t.Parallel()
	// Arrange - nothing drains, so the cap is reached after two chunks
	client := &gritzclient.ClientMock{
		AppendLogChunkFunc: func(ctx context.Context, req *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	shipper := New(client, 7, nil)
	shipper.chunkSize = 8
	shipper.maxPending = 16

	// Act - the last two writes have nowhere to go; they must still report a
	// full, error-free write.
	for _, w := range []string{"aaaaaaaa", "bbbbbbbb", "cccc", "ddddd"} {
		n, err := shipper.Write([]byte(w))
		assert.NilError(t, err)
		assert.Equal(t, n, len(w))
	}
	assert.NilError(t, shipper.Flush(t.Context()))

	// Assert - the 9 dropped bytes are accounted for by the marker
	assert.DeepEqual(t,
		client.AppendedLogChunks(),
		[]*gritzv1.AppendLogChunkRequest{
			{TaskId: 7, Data: []byte("aaaaaaaa")},
			{TaskId: 7, Data: []byte("bbbbbbbb")},
			{TaskId: 7, Data: []byte("[gritz: dropped 9 log bytes]\n")},
		},
		protocmp.Transform(),
	)
}

func TestShipper_DroppedMarkerPrecedesResumedBytes(t *testing.T) {
	t.Parallel()
	// Arrange - an unreachable server, so the queue fills and stays full
	var delivered testx.SafeSlice[string]
	var down atomic.Bool
	down.Store(true)
	client := &gritzclient.ClientMock{
		AppendLogChunkFunc: func(ctx context.Context, req *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			if down.Load() {
				return nil, connect.NewError(connect.CodeUnavailable, errors.New("server unreachable"))
			}
			delivered.Append(string(req.Data))
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	shipper := New(client, 7, nil)
	shipper.chunkSize = 8
	shipper.maxPending = 16
	shipper.backoff = backoff.NewConstantBackOff(time.Millisecond)
	shipper.log = slog.New(slog.DiscardHandler)
	go shipper.Run(t.Context())

	for _, w := range []string{"aaaaaaaa", "bbbbbbbb", "ccccc"} {
		_, err := shipper.Write([]byte(w))
		assert.NilError(t, err)
	}

	// Act - the server comes back, the queue drains, and the next write is
	// accepted again.
	down.Store(false)
	testx.WaitForWithTimeout(t, t.Context(), 5*time.Second, func() bool {
		return len(delivered.Slice()) == 2
	})
	_, err := shipper.Write([]byte("dddddddd"))
	assert.NilError(t, err)
	assert.NilError(t, shipper.Flush(t.Context()))

	// Assert - the marker lands at the gap: after the bytes that made it into
	// the queue, before the first bytes accepted once shipping resumed.
	assert.DeepEqual(t, delivered.Slice(), []string{
		"aaaaaaaa",
		"bbbbbbbb",
		"[gritz: dropped 5 log bytes]\n",
		"dddddddd",
	})
}

func TestShipper_FlushDeadline(t *testing.T) {
	t.Parallel()
	// Arrange - a server that never recovers within the flush deadline
	client := &gritzclient.ClientMock{
		AppendLogChunkFunc: func(ctx context.Context, req *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			return nil, connect.NewError(connect.CodeUnavailable, errors.New("server unreachable"))
		},
	}
	shipper := New(client, 7, nil)
	shipper.backoff = backoff.NewConstantBackOff(time.Millisecond)
	shipper.log = slog.New(slog.DiscardHandler)
	_, err := shipper.Write([]byte("hello\n"))
	assert.NilError(t, err)

	// Act
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	err = shipper.Flush(ctx)

	// Assert - the deadline is reported, not swallowed, so the driver can warn
	assert.ErrorContains(t, err, "flushing log chunks")
	assert.Assert(t, errors.Is(err, context.DeadlineExceeded))
	// Writes keep succeeding after a failed flush: the tee must never learn
	// about network trouble.
	n, err := shipper.Write([]byte("more\n"))
	assert.NilError(t, err)
	assert.Equal(t, n, 5)
}

func TestShipper_Run(t *testing.T) {
	t.Parallel()
	// Arrange
	var delivered testx.SafeSlice[string]
	client := &gritzclient.ClientMock{
		AppendLogChunkFunc: func(ctx context.Context, req *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			delivered.Append(string(req.Data))
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	shipper := New(client, 7, nil)
	shipper.flushInterval = 10 * time.Millisecond
	go shipper.Run(t.Context())

	// Act - a write far below the chunk size, so only the interval can cut it
	_, err := shipper.Write([]byte("trickle\n"))
	assert.NilError(t, err)

	// Assert
	testx.WaitForWithTimeout(t, t.Context(), 5*time.Second, func() bool {
		return len(delivered.Slice()) == 1
	})
	assert.DeepEqual(t, delivered.Slice(), []string{"trickle\n"})
}

func TestShipper_WriteDoesNotBlockOnASlowServer(t *testing.T) {
	t.Parallel()
	// Arrange - every send hangs until the test releases it, so the sender is
	// stuck in flight for the whole run of writes.
	release := make(chan struct{})
	client := &gritzclient.ClientMock{
		AppendLogChunkFunc: func(ctx context.Context, req *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	shipper := New(client, 7, nil)
	shipper.chunkSize = 8
	shipper.maxPending = 16
	go shipper.Run(t.Context())

	// Act - write well past the cap while the sender is blocked
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 {
			_, _ = shipper.Write([]byte("aaaaaaaa"))
		}
	}()

	// Assert - the writes finish without waiting for the server
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Write blocked on the in-flight sender")
	}
	close(release)
	assert.NilError(t, shipper.Flush(t.Context()))

	// Memory stayed bounded at the cap: only the two chunks that fit were kept,
	// and the 7984 bytes that did not are reported rather than silently lost.
	assert.DeepEqual(t,
		client.AppendedLogChunks(),
		[]*gritzv1.AppendLogChunkRequest{
			{TaskId: 7, Data: []byte("aaaaaaaa")},
			{TaskId: 7, Data: []byte("aaaaaaaa")},
			{TaskId: 7, Data: []byte("[gritz: dropped 7984 log bytes]\n")},
		},
		protocmp.Transform(),
	)
}

// maskGHToken is the fixed-string transformer the driver builds for a declared
// secret (redact.Transformer chains one of these per secret).
var maskGHToken = replace.String("ghp_abc123", "[gritz:masked GH_TOKEN]")

// TestShipper_MasksAcrossWrites asserts a secret split across two writes is
// still masked: log bytes arrive in arbitrary runs, so a value can land on any
// write boundary.
func TestShipper_MasksAcrossWrites(t *testing.T) {
	t.Parallel()
	// Arrange
	client := &gritzclient.ClientMock{
		AppendLogChunkFunc: func(ctx context.Context, req *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	shipper := New(client, 7, maskGHToken)

	// Act
	for _, w := range []string{"cloning with ghp_", "abc123 now\n"} {
		_, err := shipper.Write([]byte(w))
		assert.NilError(t, err)
	}
	assert.NilError(t, shipper.Flush(t.Context()))

	// Assert
	assert.Equal(t, client.ShippedLog(), "cloning with [gritz:masked GH_TOKEN] now\n")
}

// TestShipper_MasksAcrossChunks asserts the mask state outlives a chunk cut:
// with one-byte writes and a chunk cut every four bytes, both boundaries fall
// inside the secret, and it must still never reach the server.
func TestShipper_MasksAcrossChunks(t *testing.T) {
	t.Parallel()
	// Arrange
	client := &gritzclient.ClientMock{
		AppendLogChunkFunc: func(ctx context.Context, req *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	shipper := New(client, 7, maskGHToken)
	shipper.chunkSize = 4

	// Act
	for _, b := range []byte("using ghp_abc123 now\n") {
		_, err := shipper.Write([]byte{b})
		assert.NilError(t, err)
	}
	assert.NilError(t, shipper.Flush(t.Context()))

	// Assert - chunk boundaries fall wherever they fall, but the reassembled
	// transcript carries the marker and not the value.
	assert.Assert(t, len(client.AppendedLogChunks()) > 1, "expected more than one chunk")
	assert.Equal(t, client.ShippedLog(), "using [gritz:masked GH_TOKEN] now\n")
}

// TestShipper_FlushDrainsHeldBytes asserts Flush drains the tail the mask holds
// back mid-potential-match, so a run's last bytes ship with that run instead of
// arriving a run late.
func TestShipper_FlushDrainsHeldBytes(t *testing.T) {
	t.Parallel()
	// Arrange
	client := &gritzclient.ClientMock{
		AppendLogChunkFunc: func(ctx context.Context, req *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	shipper := New(client, 7, maskGHToken)

	// Act - the stream ends mid-match, so "ghp_abc" is held back
	_, err := shipper.Write([]byte("prefix ghp_abc"))
	assert.NilError(t, err)
	assert.NilError(t, shipper.Flush(t.Context()))

	// Assert - a partial match is not a secret; it ships whole, in this flush
	assert.DeepEqual(t,
		client.AppendedLogChunks(),
		[]*gritzv1.AppendLogChunkRequest{
			{TaskId: 7, Data: []byte("prefix ghp_abc")},
		},
		protocmp.Transform(),
	)
}

func TestIsPermanentChunkError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unavailable", connect.NewError(connect.CodeUnavailable, errors.New("down")), false},
		{"internal", connect.NewError(connect.CodeInternal, errors.New("boom")), false},
		{"deadline", context.DeadlineExceeded, false},
		{"not found", connect.NewError(connect.CodeNotFound, errors.New("task 1 not found")), true},
		{"invalid argument", connect.NewError(connect.CodeInvalidArgument, errors.New("too big")), true},
		{"permission denied", connect.NewError(connect.CodePermissionDenied, errors.New("nope")), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, isPermanentChunkError(tt.err), tt.want)
		})
	}
}
