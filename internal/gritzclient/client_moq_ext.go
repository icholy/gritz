package gritzclient

import (
	gritzv1 "github.com/icholy/gritz/internal/proto/gritz/v1"
)

// SubmittedRunnerEvents returns every runner event submitted across all
// SubmitRunnerEvents calls, flattened in submission order.
func (mock *ClientMock) SubmittedRunnerEvents() []*gritzv1.RunnerEvent {
	var events []*gritzv1.RunnerEvent
	for _, call := range mock.SubmitRunnerEventsCalls() {
		events = append(events, call.SubmitRunnerEventsRequest.GetEvents()...)
	}
	return events
}
