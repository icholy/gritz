// Package redact masks known secret values in a byte stream.
//
// It performs no detection: callers supply the exact values to mask, and every
// occurrence of one is replaced by a marker naming the secret it stood for.
package redact

import (
	"errors"
	"io"
	"maps"
	"slices"

	"github.com/icholy/replace"
	"golang.org/x/text/transform"
)

// prefixLen is the length of the prefix registered alongside a long secret, so
// a value truncated downstream still masks. A prefix-only hit destroys the
// credential anyway.
const prefixLen = 16

// Marker returns the replacement marker for a named secret:
// "[gritz:masked NAME]".
func Marker(name string) string {
	return "[gritz:masked " + name + "]"
}

// NewWriter wraps w so every occurrence of each secret value is replaced by
// Marker(name). For values longer than prefixLen, the prefix is registered as
// well, so a value truncated downstream still masks. Empty values are skipped:
// an empty needle matches at every position and would shred the stream.
// Close flushes any held partial match; it does not close w.
func NewWriter(w io.Writer, secrets map[string]string) io.WriteCloser {
	// Full values before prefixes: a prefix rule applied first would mask the
	// head of a full value and leave its tail in the stream.
	var full, prefixes []transform.Transformer
	for _, name := range slices.Sorted(maps.Keys(secrets)) {
		value := secrets[name]
		if value == "" {
			continue
		}
		full = append(full, replace.String(value, Marker(name)))
		if len(value) > prefixLen {
			prefixes = append(prefixes, replace.String(value[:prefixLen], Marker(name)))
		}
	}

	// One transform.Writer per rule, nested innermost-first -- replace
	// documents its transformers as unsafe to use with transform.Chain. The
	// last writer built is the outermost, so it sees the bytes first.
	f := &filter{Writer: w}
	for _, rule := range slices.Backward(slices.Concat(full, prefixes)) {
		tw := transform.NewWriter(f.Writer, rule)
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
