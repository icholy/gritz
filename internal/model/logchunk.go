package model

import "time"

// LogChunk is an opaque run of driver log bytes shipped to the server. Chunks
// are not parsed into lines: a task's transcript is the concatenation of its
// chunks' Data in ID order, which — because the driver ships with a single
// in-flight sender — is the order the bytes were written.
//
// See proposals/draft/ship-driver-logs-to-server.md.
type LogChunk struct {
	ID     int64 `json:"id"`
	OrgID  int64 `json:"org_id"`
	TaskID int64 `json:"task_id"`
	// Version is the run (Task.Version) the bytes belong to; 0 is the pre-run
	// preamble the driver buffers before it fetches its task.
	Version   int64     `json:"version"`
	Data      []byte    `json:"data"`
	CreatedAt time.Time `json:"created_at"`
}
