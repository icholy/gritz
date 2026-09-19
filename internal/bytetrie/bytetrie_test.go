package bytetrie

import (
	"testing"

	"gotest.tools/v3/assert"
)

func TestLookup(t *testing.T) {
	t.Parallel()
	// Arrange
	var tr Trie[string]
	tr.Put("ab", "two")
	tr.Put("abcd", "four")
	tr.Put("b", "bee")
	tr.Put("\x00\xff", "binary")

	tests := []struct {
		name        string
		input       string
		wantN       int
		wantValue   string
		wantPartial bool
	}{
		{"no match", "zz", 0, "", false},
		{"exact key with nothing past it", "b", 1, "bee", false},
		{"longest key wins", "abcdef", 4, "four", false},
		{"shorter terminal on a longer path", "abzz", 2, "two", false},
		{"input exhausted past a terminal", "abc", 2, "two", true},
		{"input exhausted on a terminal with children", "ab", 2, "two", true},
		{"input exhausted before any terminal", "a", 0, "", true},
		{"empty input", "", 0, "", true},
		{"keys are bytes, not runes", "\x00\xff\x00", 2, "binary", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Act
			n, value, partial := tr.Lookup([]byte(tt.input))

			// Assert
			assert.Equal(t, n, tt.wantN)
			assert.Equal(t, value, tt.wantValue)
			assert.Equal(t, partial, tt.wantPartial)
		})
	}
}

func TestLookup_EmptyTrie(t *testing.T) {
	t.Parallel()
	// Arrange
	var tr Trie[string]

	// Act
	n, value, partial := tr.Lookup([]byte("anything"))

	// Assert
	assert.Equal(t, n, 0)
	assert.Equal(t, value, "")
	assert.Equal(t, partial, false)
}

func TestPut_EmptyKey(t *testing.T) {
	t.Parallel()
	// Arrange - the empty key is the only thing in the trie
	var tr Trie[string]
	tr.Put("", "ignored")

	// Act
	n, value, partial := tr.Lookup([]byte("abc"))

	// Assert - it contributes nothing: no match, and no live path to hold for
	assert.Equal(t, n, 0)
	assert.Equal(t, value, "")
	assert.Equal(t, partial, false)
}

func TestPut_Replace(t *testing.T) {
	t.Parallel()
	// Arrange
	var tr Trie[string]
	tr.Put("ab", "first")
	tr.Put("ab", "second")

	// Act
	n, value, partial := tr.Lookup([]byte("ab"))

	// Assert
	assert.Equal(t, n, 2)
	assert.Equal(t, value, "second")
	assert.Equal(t, partial, false)
}
