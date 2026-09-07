package redact

import (
	"bytes"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

// closeCounter records Close calls so a test can assert NewWriter's Close does
// not propagate to the underlying writer.
type closeCounter struct {
	bytes.Buffer
	closed int
}

func (c *closeCounter) Close() error {
	c.closed++
	return nil
}

func TestMarker(t *testing.T) {
	assert.Equal(t, Marker("GH_TOKEN"), "[gritz:masked GH_TOKEN]")
}

// TestNewWriter_StraddledWrite asserts a secret split across two Write calls is
// still masked -- log output arrives in arbitrary byte runs, so a value can
// land on any boundary.
func TestNewWriter_StraddledWrite(t *testing.T) {
	// Arrange
	var buf bytes.Buffer
	w := NewWriter(&buf, map[string]string{"GH_TOKEN": "ghp_abc123"})

	// Act
	_, err := w.Write([]byte("cloning with ghp_"))
	assert.NilError(t, err)
	_, err = w.Write([]byte("abc123 now\n"))
	assert.NilError(t, err)
	assert.NilError(t, w.Close())

	// Assert
	assert.Equal(t, buf.String(), "cloning with [gritz:masked GH_TOKEN] now\n")
}

// TestNewWriter_TruncatedValue asserts a value cut short downstream (toollog
// caps tool-call fields at 120 runes) still masks via its 16-byte prefix.
func TestNewWriter_TruncatedValue(t *testing.T) {
	// Arrange
	secret := strings.Repeat("s", 16) + "-tail"
	var buf bytes.Buffer
	w := NewWriter(&buf, map[string]string{"GH_TOKEN": secret})

	// Act
	_, err := w.Write([]byte("full " + secret + " cut " + secret[:16] + "\n"))
	assert.NilError(t, err)
	assert.NilError(t, w.Close())

	// Assert -- the full value masks whole, not as a masked prefix plus a
	// leaked "-tail".
	assert.Equal(t, buf.String(),
		"full [gritz:masked GH_TOKEN] cut [gritz:masked GH_TOKEN]\n")
}

// TestNewWriter_Close asserts Close flushes bytes the transformer held back as
// a potential match, and leaves the underlying writer open -- the driver keeps
// writing to the shipper after closing the filter.
func TestNewWriter_Close(t *testing.T) {
	// Arrange
	var out closeCounter
	w := NewWriter(&out, map[string]string{"GH_TOKEN": "ghp_abc123"})

	// Act -- ends mid-potential-match, so the tail is held back.
	_, err := w.Write([]byte("prefix ghp_abc"))
	assert.NilError(t, err)
	assert.Assert(t, out.String() != "prefix ghp_abc")

	assert.NilError(t, w.Close())

	// Assert
	assert.Equal(t, out.String(), "prefix ghp_abc")
	assert.Equal(t, out.closed, 0)
}

// TestNewWriter_EmptyValue asserts an empty secret is skipped rather than
// matching at every position: the workspace config does no validation, so an
// empty value can reach the filter.
func TestNewWriter_EmptyValue(t *testing.T) {
	// Arrange
	var buf bytes.Buffer
	w := NewWriter(&buf, map[string]string{"EMPTY": "", "GH_TOKEN": "ghp_abc123"})

	// Act
	_, err := w.Write([]byte("hello ghp_abc123\n"))
	assert.NilError(t, err)
	assert.NilError(t, w.Close())

	// Assert
	assert.Equal(t, buf.String(), "hello [gritz:masked GH_TOKEN]\n")
}
