package redact

import (
	"testing"

	"gotest.tools/v3/assert"
)

func TestMarker(t *testing.T) {
	assert.Equal(t, Marker("GH_TOKEN"), "[gritz:masked GH_TOKEN]")
}

// TestString exercises the Transformer the shipper streams through, since
// String is that transformer applied to a whole value.
func TestString(t *testing.T) {
	// Act
	got := String("cloning with ghp_abc123 now\n", map[string]string{"GH_TOKEN": "ghp_abc123"})

	// Assert
	assert.Equal(t, got, "cloning with [gritz:masked GH_TOKEN] now\n")
}

// TestString_OverlappingValues asserts that when one declared secret's value is
// a prefix of another's, the longer value still masks whole -- the chain is
// ordered by value length, not name, so the shorter rule cannot bite into the
// longer value and leave its tail beside a marker.
func TestString_OverlappingValues(t *testing.T) {
	// Arrange -- A sorts first by name but must not fire first.
	short := "gho_abcdefghijklmnop"
	long := short + "QRSTUVWXYZ99"

	// Act
	got := String("using "+long+" and "+short+"\n", map[string]string{
		"A_TOKEN": short,
		"B_TOKEN": long,
	})

	// Assert
	assert.Equal(t, got,
		"using [gritz:masked B_TOKEN] and [gritz:masked A_TOKEN]\n")
}

// TestString_EmptyValue asserts an empty secret is skipped rather than matching
// at every position: the workspace config does no validation, so an empty value
// can reach the transformer.
func TestString_EmptyValue(t *testing.T) {
	// Act
	got := String("hello ghp_abc123\n", map[string]string{"EMPTY": "", "GH_TOKEN": "ghp_abc123"})

	// Assert
	assert.Equal(t, got, "hello [gritz:masked GH_TOKEN]\n")
}

// TestString_NoSecrets asserts an empty map leaves the stream untouched, which
// is what an undeclared workspace ships.
func TestString_NoSecrets(t *testing.T) {
	// Act
	got := String("nothing to hide\n", nil)

	// Assert
	assert.Equal(t, got, "nothing to hide\n")
}
