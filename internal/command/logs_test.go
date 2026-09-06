package command

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/icholy/gritz/internal/gritzclient"
	"github.com/icholy/gritz/internal/pagination"
	gritzv1 "github.com/icholy/gritz/internal/proto/gritz/v1"
	"gotest.tools/v3/assert"
)

// logChunkSlice is a pagination.Source over an in-memory chunk slice held in
// ascending id order — the store's keyset, minus the database. The forward walk
// runs toward older rows (descending) and the backward walk toward newer ones
// (ascending), both exclusive of the cursor, exactly as the sqlc queries do.
type logChunkSlice struct {
	chunks []*gritzv1.LogChunk
}

func (s logChunkSlice) Cursor(chunk *gritzv1.LogChunk) int64 { return chunk.GetId() }

func (s logChunkSlice) Query(ctx context.Context, token pagination.Token[int64], limit int) ([]*gritzv1.LogChunk, error) {
	var out []*gritzv1.LogChunk
	if token.Backward {
		for _, chunk := range s.chunks {
			if token.Cursor != nil && chunk.GetId() <= *token.Cursor {
				continue
			}
			if out = append(out, chunk); len(out) == limit {
				break
			}
		}
		return out, nil
	}
	for i := len(s.chunks) - 1; i >= 0; i-- {
		chunk := s.chunks[i]
		if token.Cursor != nil && chunk.GetId() >= *token.Cursor {
			continue
		}
		if out = append(out, chunk); len(out) == limit {
			break
		}
	}
	return out, nil
}

// fakeLogs answers ListLogChunksByTask out of an in-memory log. It runs the real
// pagination.List with the apiserver handler's options and token mapping, so the
// CLI is exercised against production cursor semantics rather than a
// hand-rolled imitation of them — the direction of each token is the one thing
// this command has to get right.
type fakeLogs struct {
	chunks []*gritzv1.LogChunk
}

// append adds a chunk to the end of the log, as a shipped AppendLogChunk would.
func (f *fakeLogs) append(data string) {
	f.chunks = append(f.chunks, &gritzv1.LogChunk{Id: int64(len(f.chunks) + 1), TaskId: 1, Data: []byte(data)})
}

func (f *fakeLogs) list(ctx context.Context, req *gritzv1.ListLogChunksByTaskRequest) (*gritzv1.ListLogChunksByTaskResponse, error) {
	page, err := pagination.List(ctx, pagination.Options[*gritzv1.LogChunk, int64]{
		DefaultPageSize: 50,
		MaxPageSize:     200,
		Reverse:         true,
		PageSize:        int(req.GetPageSize()),
		PageToken:       req.GetPageToken(),
		Source:          logChunkSlice{chunks: f.chunks},
	})
	if err != nil {
		return nil, err
	}
	// The store's forward walk goes toward older rows, so its NextToken is the
	// transcript's scroll-back token and its PrevToken the live-follow one.
	return &gritzv1.ListLogChunksByTaskResponse{
		Chunks:        page.Items,
		PrevPageToken: page.NextToken,
		NextPageToken: page.PrevToken,
		More:          page.More,
	}, nil
}

// newFakeLogs returns a client mock serving a log of n chunks, each holding its
// own 1-based index, along with the transcript those chunks concatenate into.
func newFakeLogs(t *testing.T, n int) (*fakeLogs, *gritzclient.ClientMock, string) {
	t.Helper()
	var (
		fake fakeLogs
		want strings.Builder
	)
	for i := 1; i <= n; i++ {
		data := fmt.Sprintf("line %d\n", i)
		fake.append(data)
		want.WriteString(data)
	}
	return &fake, &gritzclient.ClientMock{ListLogChunksByTaskFunc: fake.list}, want.String()
}

func TestPrintTaskLogs(t *testing.T) {
	// Arrange - two and a half pages of history, so the walk has to page back.
	_, client, want := newFakeLogs(t, 2*logPageSize+logPageSize/2)

	// Act
	var buf bytes.Buffer
	err := printTaskLogs(t.Context(), &buf, client, logsOptions{TaskID: 1})

	// Assert - the whole transcript, oldest-first, in one piece.
	assert.NilError(t, err)
	assert.Equal(t, buf.String(), want)
	assert.Equal(t, len(client.ListLogChunksByTaskCalls()), 3)
	// The walk opens at the tail and steps backwards through history.
	assert.Equal(t, client.ListLogChunksByTaskCalls()[0].ListLogChunksByTaskRequest.GetPageToken(), "")
	for _, call := range client.ListLogChunksByTaskCalls()[1:] {
		token, err := pagination.Decode[int64](call.ListLogChunksByTaskRequest.GetPageToken())
		assert.NilError(t, err)
		assert.Equal(t, token.Backward, false, "history must walk toward older rows")
	}
}

func TestPrintTaskLogs_ExactPageBoundary(t *testing.T) {
	// Arrange - history that is an exact multiple of the page size, where a
	// shorter-than-page_size heuristic would stop a page early (or walk one too
	// far). Only `more` gets this right.
	_, client, want := newFakeLogs(t, 2*logPageSize)

	// Act
	var buf bytes.Buffer
	err := printTaskLogs(t.Context(), &buf, client, logsOptions{TaskID: 1})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, buf.String(), want)
	assert.Equal(t, len(client.ListLogChunksByTaskCalls()), 2)
}

func TestPrintTaskLogs_Empty(t *testing.T) {
	// Arrange - a task that never shipped a chunk (or whose logs were deleted).
	_, client, _ := newFakeLogs(t, 0)

	// Act
	var buf bytes.Buffer
	err := printTaskLogs(t.Context(), &buf, client, logsOptions{TaskID: 1})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, buf.String(), "")
	assert.Equal(t, len(client.ListLogChunksByTaskCalls()), 1)
}

func TestPrintTaskLogs_Follow(t *testing.T) {
	// Arrange - history plus a chunk that lands after the first poll. The follow
	// cursor must walk toward newer rows: a token pointed the other way would
	// replay history instead of picking the appended chunk up.
	fake, client, want := newFakeLogs(t, 2)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	list := client.ListLogChunksByTaskFunc
	var calls int
	client.ListLogChunksByTaskFunc = func(ctx context.Context, req *gritzv1.ListLogChunksByTaskRequest) (*gritzv1.ListLogChunksByTaskResponse, error) {
		calls++
		switch calls {
		case 2:
			fake.append("line 3\n")
		case 4:
			// Two empty polls after the append: stop following.
			cancel()
		}
		return list(ctx, req)
	}

	// Act
	var buf bytes.Buffer
	err := printTaskLogs(ctx, &buf, client, logsOptions{TaskID: 1, Follow: true, Interval: time.Millisecond})

	// Assert - the append is printed exactly once, after the history.
	assert.NilError(t, err)
	assert.Equal(t, buf.String(), want+"line 3\n")
	// Every follow poll rides the live-follow (newer) direction.
	for _, call := range client.ListLogChunksByTaskCalls()[1:] {
		token, err := pagination.Decode[int64](call.ListLogChunksByTaskRequest.GetPageToken())
		assert.NilError(t, err)
		assert.Equal(t, token.Backward, true, "follow must poll toward newer rows")
	}
}

// cancelAfter cancels ctx as soon as the output it has accumulated contains
// marker. It stops a follow loop the moment its expected output has arrived,
// which keeps the poll count deterministic — a follow loop stopped on a call
// count instead would hide an extra poll.
type cancelAfter struct {
	buf    bytes.Buffer
	marker string
	cancel context.CancelFunc
}

func (c *cancelAfter) Write(p []byte) (int, error) {
	n, err := c.buf.Write(p)
	if strings.Contains(c.buf.String(), c.marker) {
		c.cancel()
	}
	return n, err
}

func TestPrintTaskLogs_FollowDrainsFullPages(t *testing.T) {
	// Arrange - a poll that finds exactly two full pages of appends. The drain
	// has to walk both and stop on !more: a final page that exactly fills
	// page_size is indistinguishable by length from a page with more behind it.
	fake, client, want := newFakeLogs(t, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	list := client.ListLogChunksByTaskFunc
	var calls int
	client.ListLogChunksByTaskFunc = func(ctx context.Context, req *gritzv1.ListLogChunksByTaskRequest) (*gritzv1.ListLogChunksByTaskResponse, error) {
		calls++
		if calls == 2 {
			for i := 1; i <= 2*logPageSize; i++ {
				data := fmt.Sprintf("append %d\n", i)
				fake.append(data)
				want += data
			}
		}
		return list(ctx, req)
	}
	w := &cancelAfter{marker: fmt.Sprintf("append %d\n", 2*logPageSize), cancel: cancel}

	// Act
	err := printTaskLogs(ctx, w, client, logsOptions{TaskID: 1, Follow: true, Interval: time.Millisecond})

	// Assert - everything appended, in order, in one drain of two pages.
	assert.NilError(t, err)
	assert.Equal(t, w.buf.String(), want)
	assert.Equal(t, len(client.ListLogChunksByTaskCalls()), 3, "the drain must stop on !more, without an extra probe")
}

func TestPrintTaskLogs_FollowFromEmpty(t *testing.T) {
	// Arrange - following a task before it has shipped anything: the tail page
	// yields no follow cursor, so the tail must be re-opened until it does.
	fake, client, _ := newFakeLogs(t, 0)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	list := client.ListLogChunksByTaskFunc
	var calls int
	client.ListLogChunksByTaskFunc = func(ctx context.Context, req *gritzv1.ListLogChunksByTaskRequest) (*gritzv1.ListLogChunksByTaskResponse, error) {
		calls++
		switch calls {
		case 3:
			fake.append("first line\n")
		case 5:
			cancel()
		}
		return list(ctx, req)
	}

	// Act
	var buf bytes.Buffer
	err := printTaskLogs(ctx, &buf, client, logsOptions{TaskID: 1, Follow: true, Interval: time.Millisecond})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, buf.String(), "first line\n")
}

func TestPrintTaskLogs_Error(t *testing.T) {
	// Arrange
	client := &gritzclient.ClientMock{
		ListLogChunksByTaskFunc: func(ctx context.Context, req *gritzv1.ListLogChunksByTaskRequest) (*gritzv1.ListLogChunksByTaskResponse, error) {
			return nil, fmt.Errorf("task 1 not found")
		},
	}

	// Act
	var buf bytes.Buffer
	err := printTaskLogs(t.Context(), &buf, client, logsOptions{TaskID: 1})

	// Assert
	assert.ErrorContains(t, err, "task 1 not found")
}
