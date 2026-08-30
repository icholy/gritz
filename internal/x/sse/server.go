package sse

import (
	"errors"
	"net/http"
)

// ServerWriter writes Server-Sent Events to an http.ResponseWriter,
// setting the SSE response headers and flushing after each event.
type ServerWriter struct {
	w *Writer
	f http.Flusher
}

// NewServerWriter sets the SSE headers on w and returns a ServerWriter.
// It errors if w does not support flushing. Call it only after any error
// responses (it commits SSE headers).
func NewServerWriter(w http.ResponseWriter) (*ServerWriter, error) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("sse: streaming unsupported")
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	// Tell nginx (and other proxies that honour it) not to buffer the
	// response, so each event reaches the client as it's written.
	h.Set("X-Accel-Buffering", "no")
	return &ServerWriter{w: NewWriter(w), f: f}, nil
}

// Write encodes ev, writes it, and flushes.
func (s *ServerWriter) Write(ev Event) error {
	if err := s.w.Write(ev); err != nil {
		return err
	}
	s.f.Flush()
	return nil
}

// WriteComment writes an SSE comment and flushes. Use it to keep an idle
// stream alive.
func (s *ServerWriter) WriteComment(text string) error {
	if err := s.w.WriteComment(text); err != nil {
		return err
	}
	s.f.Flush()
	return nil
}
