// Package sse implements reading and writing Server-Sent Events.
package sse

import (
	"bufio"
	"bytes"
	"io"
)

// Event is a single Server-Sent Event.
type Event struct {
	ID    string
	Event string
	Retry string
	Data  []byte
}

// Clone returns a deep copy of e with its own Data slice.
func (e Event) Clone() Event {
	e.Data = bytes.Clone(e.Data)
	return e
}

// Reader parses Server-Sent Events from an underlying io.Reader.
type Reader struct {
	buf     bytes.Buffer
	scanner *bufio.Scanner
}

// NewReader returns a Reader that parses events from r.
func NewReader(r io.Reader) *Reader {
	return &Reader{
		scanner: bufio.NewScanner(r),
	}
}

// Read returns the next event from the stream. The returned Event's Data
// field aliases an internal buffer that is reused on the next call to Read;
// use Event.Clone to retain it across calls.
func (r *Reader) Read() (Event, error) {
	r.buf.Reset()
	var ev Event
	for r.scanner.Scan() {
		line := r.scanner.Bytes()
		if len(line) == 0 {
			ev.Data = r.buf.Bytes()
			return ev, nil
		}
		name, value, ok := bytes.Cut(line, []byte{':'})
		if !ok {
			continue
		}
		value = bytes.TrimPrefix(value, []byte{' '})
		switch {
		case bytes.Equal(name, []byte("id")):
			ev.ID = string(value)
		case bytes.Equal(name, []byte("event")):
			ev.Event = string(value)
		case bytes.Equal(name, []byte("retry")):
			ev.Retry = string(value)
		case bytes.Equal(name, []byte("data")):
			r.buf.Write(value)
		}
	}
	if err := r.scanner.Err(); err != nil {
		return Event{}, err
	}
	return Event{}, nil
}

func (w *Writer) writeField(name, value []byte) {
	w.buf.Write(name)
	w.buf.Write([]byte(": "))
	w.buf.Write(value)
	w.buf.Write([]byte("\n"))
}

// Writer encodes Server-Sent Events to an underlying io.Writer.
type Writer struct {
	buf bytes.Buffer
	w   io.Writer
}

// NewWriter returns a Writer that encodes events to w.
func NewWriter(w io.Writer) *Writer {
	return &Writer{w: w}
}

// Write encodes ev and writes it to the underlying writer, terminated by a
// blank line.
func (w *Writer) Write(ev Event) error {
	w.buf.Reset()
	if ev.ID != "" {
		w.writeField([]byte("id"), []byte(ev.ID))
	}
	if ev.Event != "" {
		w.writeField([]byte("event"), []byte(ev.Event))
	}
	if ev.Retry != "" {
		w.writeField([]byte("retry"), []byte(ev.Retry))
	}
	for line := range bytes.Lines(ev.Data) {
		w.writeField([]byte("data"), bytes.TrimSuffix(line, []byte{'\n'}))
	}
	w.buf.WriteByte('\n')
	_, err := w.buf.WriteTo(w.w)
	return err
}
