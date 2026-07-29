package apiserver

import (
	"testing"

	gritzv1 "github.com/icholy/gritz/internal/proto/gritz/v1"
	"github.com/icholy/gritz/internal/store/teststore"
	"gotest.tools/v3/assert"
)

// TestUploadLogs_NonReportIgnored verifies that the only log channel left on the
// wire is the agent's report tool (`llm`). The logs table is gone, so non-report
// entries (formerly audit/info/error rows) no longer have a home and are
// silently dropped — they must not become events.
func TestUploadLogs_NonReportIgnored(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)
	taskResp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:      "Task with Logs",
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})
	assert.NilError(t, err)

	// Act
	_, err = srv.UploadLogs(ctx, &gritzv1.UploadLogsRequest{
		TaskId: taskResp.Task.Id,
		Entries: []*gritzv1.LogEntry{
			{Type: "info", Content: "First log entry"},
			{Type: "error", Content: "Second log entry"},
		},
	})
	assert.NilError(t, err)

	// Assert - no report (or other) events were created from the dropped entries.
	events, err := srv.ListEventsByTask(ctx, &gritzv1.ListEventsByTaskRequest{TaskId: taskResp.Task.Id})
	assert.NilError(t, err)
	for _, e := range events.Events {
		assert.Assert(t, e.GetReport() == nil, "non-report entries must not become report events")
	}
}

func TestUploadLogs_ReportBecomesEvent(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)
	taskResp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:      "Task with report",
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})
	assert.NilError(t, err)

	// Act - the agent's report tool uploads an `llm` entry.
	_, err = srv.UploadLogs(ctx, &gritzv1.UploadLogsRequest{
		TaskId: taskResp.Task.Id,
		Entries: []*gritzv1.LogEntry{
			{Type: "llm", Content: "Opened PR #952"},
		},
	})
	assert.NilError(t, err)

	// Assert - the report is a from-agent report event.
	events, err := srv.ListEventsByTask(ctx, &gritzv1.ListEventsByTaskRequest{TaskId: taskResp.Task.Id})
	assert.NilError(t, err)
	var reports []*gritzv1.ReportPayload
	for _, e := range events.Events {
		if r := e.GetReport(); r != nil {
			assert.Equal(t, e.Wake, false)
			reports = append(reports, r)
		}
	}
	assert.Equal(t, len(reports), 1)
	assert.Equal(t, reports[0].Content, "Opened PR #952")
}

func TestUploadLogs_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	orgA := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctxA := createCtx(t, orgA)
	orgB := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctxB := createCtx(t, orgB)
	taskResp, err := srv.CreateTask(ctxA, &gritzv1.CreateTaskRequest{
		Name:      "User A's Task",
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})
	assert.NilError(t, err)

	// Act
	_, err = srv.UploadLogs(ctxB, &gritzv1.UploadLogsRequest{
		TaskId: taskResp.Task.Id,
		Entries: []*gritzv1.LogEntry{
			{Type: "llm", Content: "Sneaky report"},
		},
	})

	// Assert
	assert.ErrorContains(t, err, "not found")
}
