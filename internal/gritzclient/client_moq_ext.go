package gritzclient

import (
	gritzv1 "github.com/icholy/gritz/internal/proto/gritz/v1"
)

// AppendedLogChunks returns every log chunk append request, in call order —
// which, because the shipper keeps a single request in flight, is the order the
// server would have stored them in.
func (mock *ClientMock) AppendedLogChunks() []*gritzv1.AppendLogChunkRequest {
	var chunks []*gritzv1.AppendLogChunkRequest
	for _, call := range mock.AppendLogChunkCalls() {
		chunks = append(chunks, call.AppendLogChunkRequest)
	}
	return chunks
}

// SubmittedRunnerEvents returns every runner event submitted across all
// SubmitRunnerEvents calls, flattened in submission order.
func (mock *ClientMock) SubmittedRunnerEvents() []*gritzv1.RunnerEvent {
	var events []*gritzv1.RunnerEvent
	for _, call := range mock.SubmitRunnerEventsCalls() {
		events = append(events, call.SubmitRunnerEventsRequest.GetEvents()...)
	}
	return events
}
