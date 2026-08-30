package sse

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func TestReader(t *testing.T) {
	tests := []struct {
		name string
		r    io.Reader
		want []Event
	}{
		{
			name: "data only",
			r:    strings.NewReader("data:hello\n\n"),
			want: []Event{{Data: []byte("hello")}},
		},
		{
			name: "all fields",
			r:    strings.NewReader("id:1\nevent:msg\nretry:5000\ndata:hello\n\n"),
			want: []Event{{ID: "1", Event: "msg", Retry: "5000", Data: []byte("hello")}},
		},
		{
			name: "multiple events",
			r:    strings.NewReader("data:first\n\ndata:second\n\n"),
			want: []Event{{Data: []byte("first")}, {Data: []byte("second")}},
		},
		{
			name: "multi-line data",
			r:    strings.NewReader("data:hello\ndata:world\n\n"),
			want: []Event{{Data: []byte("helloworld")}},
		},
		{
			name: "space after colon",
			r:    strings.NewReader("id: 1\nevent: msg\ndata: hello\n\n"),
			want: []Event{{ID: "1", Event: "msg", Data: []byte("hello")}},
		},
		{
			name: "comments are skipped",
			r:    strings.NewReader(": keepalive\n\n: keepalive\n\nevent: msg\ndata: hello\n\n"),
			want: []Event{{Event: "msg", Data: []byte("hello")}},
		},
		{
			name: "comment inside an event",
			r:    strings.NewReader(": keepalive\ndata: hello\n\n"),
			want: []Event{{Data: []byte("hello")}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewReader(tt.r)
			var got []Event
			for range len(tt.want) {
				ev, err := r.Read()
				assert.NilError(t, err)
				got = append(got, ev.Clone())
			}
			assert.DeepEqual(t, got, tt.want)
		})
	}
}

func TestRoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		events []Event
	}{
		{
			name:   "data only",
			events: []Event{{Data: []byte("hello")}},
		},
		{
			name:   "all fields",
			events: []Event{{ID: "1", Event: "msg", Retry: "5000", Data: []byte("hello")}},
		},
		{
			name: "multiple events",
			events: []Event{
				{Data: []byte("first")},
				{ID: "2", Data: []byte("second")},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			w := NewWriter(&buf)
			for _, ev := range tt.events {
				assert.NilError(t, w.Write(ev))
			}
			r := NewReader(&buf)
			var got []Event
			for range len(tt.events) {
				ev, err := r.Read()
				assert.NilError(t, err)
				got = append(got, ev.Clone())
			}
			assert.DeepEqual(t, got, tt.events)
		})
	}
}

func TestWriterComment(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	assert.NilError(t, w.WriteComment("keepalive"))
	assert.Equal(t, buf.String(), ": keepalive\n\n")

	// A comment must not be surfaced as an event by the reader.
	assert.NilError(t, w.Write(Event{Event: "msg", Data: []byte("hello")}))
	ev, err := NewReader(&buf).Read()
	assert.NilError(t, err)
	assert.DeepEqual(t, ev.Clone(), Event{Event: "msg", Data: []byte("hello")})
}

func TestWriter(t *testing.T) {
	tests := []struct {
		name  string
		event Event
		want  string
	}{
		{
			name:  "data only",
			event: Event{Data: []byte("hello")},
			want:  "data: hello\n\n",
		},
		{
			name:  "all fields",
			event: Event{ID: "1", Event: "msg", Retry: "5000", Data: []byte("hello")},
			want:  "id: 1\nevent: msg\nretry: 5000\ndata: hello\n\n",
		},
		{
			name:  "multi-line data",
			event: Event{Data: []byte("hello\nworld")},
			want:  "data: hello\ndata: world\n\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			w := NewWriter(&buf)
			err := w.Write(tt.event)
			assert.NilError(t, err)
			assert.Equal(t, buf.String(), tt.want)
		})
	}
}
