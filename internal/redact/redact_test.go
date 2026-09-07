package redact

import (
	"testing"

	"golang.org/x/text/transform"
	"gotest.tools/v3/assert"
)

// mask runs s through Transformer as a single complete stream, which is what
// the shipper does across a run's writes.
func mask(t *testing.T, s string, secrets map[string]string) string {
	t.Helper()
	got, _, err := transform.String(Transformer(secrets), s)
	assert.NilError(t, err)
	return got
}

func TestMarker(t *testing.T) {
	assert.Equal(t, Marker("GH_TOKEN"), "[gritz:masked GH_TOKEN]")
}

func TestTransformer(t *testing.T) {
	// Act
	got := mask(t, "cloning with ghp_abc123 now\n", map[string]string{"GH_TOKEN": "ghp_abc123"})

	// Assert
	assert.Equal(t, got, "cloning with [gritz:masked GH_TOKEN] now\n")
}

// TestTransformer_OverlappingValues asserts that when one declared secret's
// value is a prefix of another's, the longer value still masks whole -- the
// chain is ordered by value length, not name, so the shorter rule cannot bite
// into the longer value and leave its tail beside a marker.
func TestTransformer_OverlappingValues(t *testing.T) {
	// Arrange -- A sorts first by name but must not fire first.
	short := "gho_abcdefghijklmnop"
	long := short + "QRSTUVWXYZ99"

	// Act
	got := mask(t, "using "+long+" and "+short+"\n", map[string]string{
		"A_TOKEN": short,
		"B_TOKEN": long,
	})

	// Assert
	assert.Equal(t, got,
		"using [gritz:masked B_TOKEN] and [gritz:masked A_TOKEN]\n")
}

// TestTransformer_EmptyValue asserts an empty secret is skipped rather than
// matching at every position: the workspace config does no validation, so an
// empty value can reach the transformer.
func TestTransformer_EmptyValue(t *testing.T) {
	// Act
	got := mask(t, "hello ghp_abc123\n", map[string]string{"EMPTY": "", "GH_TOKEN": "ghp_abc123"})

	// Assert
	assert.Equal(t, got, "hello [gritz:masked GH_TOKEN]\n")
}

// TestTransformer_NoSecrets asserts an empty map leaves the stream untouched,
// which is what an undeclared workspace ships.
func TestTransformer_NoSecrets(t *testing.T) {
	// Act
	got := mask(t, "nothing to hide\n", nil)

	// Assert
	assert.Equal(t, got, "nothing to hide\n")
}

// TestString asserts the string entry point masks every occurrence and shares
// the transformer's longest-value-first ordering.
func TestString(t *testing.T) {
	// Arrange
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
