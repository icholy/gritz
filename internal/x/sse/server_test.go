package sse

import (
	"net/http/httptest"
	"testing"

	"gotest.tools/v3/assert"
)

func TestServerWriter(t *testing.T) {
	// Arrange
	rec := httptest.NewRecorder()
	sw, err := NewServerWriter(rec)
	assert.NilError(t, err)

	// Act
	err = sw.Write(Event{Event: "ready", Data: []byte("hi")})
	assert.NilError(t, err)

	// Assert
	resp := rec.Result()
	assert.Equal(t, resp.Header.Get("Content-Type"), "text/event-stream")
	assert.Equal(t, resp.Header.Get("Cache-Control"), "no-cache")
	assert.Equal(t, resp.Header.Get("Connection"), "keep-alive")
	assert.Equal(t, resp.Header.Get("X-Accel-Buffering"), "no")
	assert.Equal(t, rec.Body.String(), "event: ready\ndata: hi\n\n")
}
