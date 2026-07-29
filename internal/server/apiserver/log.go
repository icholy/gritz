package apiserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/icholy/gritz/internal/auth/apiauth"
	"github.com/icholy/gritz/internal/auth/authscope"
	"github.com/icholy/gritz/internal/model"
	gritzv1 "github.com/icholy/gritz/internal/proto/gritz/v1"
)

// reportLogType is the legacy logs.type value the agent's report tool uploads.
// The UploadLogs handler re-points it onto the event stream as a report event;
// the wire (UploadLogs) is unchanged until the agent surface lands.
const reportLogType = "llm"

func (s *Server) UploadLogs(ctx context.Context, req *gritzv1.UploadLogsRequest) (*gritzv1.UploadLogsResponse, error) {
	caller := apiauth.MustCaller(ctx)
	// Coarse, fail-fast capability gate before the DB read (AllowOp ignores
	// predicates); the instance check happens after the row is loaded.
	if !caller.Scopes.AllowOp(authscope.OpTaskWrite) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot write task"))
	}
	task, err := s.store.GetTask(ctx, nil, req.TaskId, caller.OrgID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("task %d not found", req.TaskId))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !caller.Scopes.Allow(authscope.OpTaskWrite, task.ScopeAttr()...) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot write task"))
	}
	for _, entry := range req.Entries {
		// The logs table is gone: the only log channel left on the wire is the
		// agent's report tool (`llm`), which becomes a from-agent `report` event.
		// Reports are from-agent, so they do not wake the task. Other log types no
		// longer have a writer (audit/info/error became lifecycle events, mcp
		// breadcrumbs were dropped), so any non-report entry is ignored.
		if entry.Type != reportLogType {
			continue
		}
		if err := s.store.CreateEvent(ctx, nil, &model.Event{
			TaskID:  task.ID,
			OrgID:   task.OrgID,
			Payload: &model.ReportPayload{Content: entry.Content},
		}); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	s.publish(model.Notification{
		Type:      "change",
		Resources: []model.NotificationResource{{Action: "appended", Type: "task_events", ID: req.TaskId}},
		OrgID:     caller.OrgID,
		UserID:    caller.ID,
		ClientID:  caller.ClientID,
		Time:      time.Now(),
	})
	return &gritzv1.UploadLogsResponse{}, nil
}
