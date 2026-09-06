package command

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/icholy/gritz/internal/gritzclient"
	gritzv1 "github.com/icholy/gritz/internal/proto/gritz/v1"
	"gotest.tools/v3/assert"
)

// The cursor walks themselves are covered against production pagination
// semantics in internal/gritzclient/logs_test.go. What is left here is the
// presentation half: chunk data reaches the writer as-is, and an RPC failure
// reaches the exit code.

func TestPrintTaskLogs(t *testing.T) {
	// Arrange - a single-page transcript, which the history walk reads in one
	// request and hands back oldest-first.
	client := &gritzclient.ClientMock{
		ListLogChunksByTaskFunc: func(ctx context.Context, req *gritzv1.ListLogChunksByTaskRequest) (*gritzv1.ListLogChunksByTaskResponse, error) {
			return &gritzv1.ListLogChunksByTaskResponse{
				Chunks: []*gritzv1.LogChunk{
					{Id: 1, TaskId: 1, Data: []byte("line 1\n")},
					{Id: 2, TaskId: 1, Data: []byte("line 2\n")},
				},
				NextPageToken: "tail",
			}, nil
		},
	}

	// Act
	var buf bytes.Buffer
	err := printTaskLogs(t.Context(), &buf, client, logsOptions{TaskID: 1})

	// Assert - the raw chunk bytes, concatenated, with nothing added.
	assert.NilError(t, err)
	assert.Equal(t, buf.String(), "line 1\nline 2\n")
	assert.Equal(t, client.ListLogChunksByTaskCalls()[0].ListLogChunksByTaskRequest.GetPageSize(), int32(logPageSize))
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
