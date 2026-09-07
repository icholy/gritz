// Package redact masks known secret values in a byte stream or a string.
//
// It performs no detection: callers supply the exact values to mask, and every
// occurrence of one is replaced by a marker naming the secret it stood for.
package redact

import (
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

// Transformer returns a transform.Transformer that replaces every occurrence of
// each secret value with Marker(name). The rules are chained in names() order,
// so the longest value is masked first.
//
// Chaining is safe here: replace's fixed-string transformer is a stateless
// value (it embeds transform.NopResetter), and the library's caveats about
// combining transformers apply to its Regexp* functions, not to this one. A
// value straddling two writes is still masked, because a chained
// transform.Transformer signals ErrShortSrc for a trailing potential match and
// its caller retains those bytes for the next call.
func Transformer(secrets map[string]string) transform.Transformer {
	var rules []transform.Transformer
	for _, name := range names(secrets) {
		rules = append(rules, replace.String(secrets[name], Marker(name)))
	}
	return transform.Chain(rules...)
}
