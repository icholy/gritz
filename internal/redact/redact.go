// Package redact masks known secret values in a byte stream or a string.
//
// It performs no detection: callers supply the exact values to mask, and every
// occurrence of one is replaced by a marker naming the secret it stood for.
package redact

import (
	"errors"
	"io"
	"slices"
	"strings"

	"github.com/icholy/replace"
	"golang.org/x/text/transform"
)

// Marker returns the replacement marker for a named secret:
// "[gritz:masked NAME]".
func Marker(name string) string {
	return "[gritz:masked " + name + "]"
}

// names returns the secret names in the order their values must be masked:
// longest value first, so that when one value is a prefix of another the
// shorter one cannot fire first and leave the longer value's tail in the
// output, beside a marker that makes it look masked. Ties break on name to
// keep the order deterministic. Empty values are dropped: an empty needle
// matches at every position and would shred the output instead of redacting
// it.
func names(secrets map[string]string) []string {
	var names []string
	for name, value := range secrets {
		if value != "" {
			names = append(names, name)
		}
	}
	slices.SortFunc(names, func(a, b string) int {
		if n := len(secrets[b]) - len(secrets[a]); n != 0 {
			return n
		}
		return strings.Compare(a, b)
	})
	return names
}

// String returns s with every occurrence of each secret value replaced by
// Marker(name).
func String(s string, secrets map[string]string) string {
	for _, name := range names(secrets) {
		s = strings.ReplaceAll(s, secrets[name], Marker(name))
	}
	return s
}

// NewWriter wraps w so every occurrence of each secret value is replaced by
// Marker(name). Close flushes any held partial match; it does not close w.
func NewWriter(w io.Writer, secrets map[string]string) io.WriteCloser {
	// One transform.Writer per rule, nested innermost-first -- replace
	// documents its transformers as unsafe to use with transform.Chain. The
	// last writer built is the outermost, so it sees the bytes first; build in
	// reverse so the longest value is masked first.
	f := &filter{Writer: w}
	for _, name := range slices.Backward(names(secrets)) {
		tw := transform.NewWriter(f.Writer, replace.String(secrets[name], Marker(name)))
		f.Writer = tw
		f.writers = append(f.writers, tw)
	}
	return f
}

// filter writes through a stack of transform writers. Writer is the outermost
// of them (or the caller's writer when there are no rules); writers holds them
// innermost-first.
type filter struct {
	io.Writer
	writers []*transform.Writer
}

// Close flushes each transform writer's held bytes, outermost first: closing
// one writes its holdback into the next, which then has to flush its own. The
// underlying writer is left open.
func (f *filter) Close() error {
	var err error
	for _, tw := range slices.Backward(f.writers) {
		err = errors.Join(err, tw.Close())
	}
	return err
}
