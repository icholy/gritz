// Package bytetrie implements a byte-keyed trie holding one value per key.
//
// Its single lookup answers both questions a streaming matcher asks of the
// bytes it has in hand: which is the longest key that is a prefix of them, and
// could a longer key still match if more bytes arrived. There is no resumable
// cursor -- a caller that gains more input walks again from the root.
package bytetrie

// Trie maps byte-string keys to values of type V. The zero Trie is empty and
// ready to use. It must not be modified concurrently with any other use.
type Trie[V any] struct {
	root *node[V]
}

type node[V any] struct {
	children map[byte]*node[V]
	value    V
	terminal bool
}

// Put stores value under key, replacing whatever was stored under it before.
//
// Keys are strings because callers hold their keys as strings, while Lookup
// takes the bytes a stream arrives in; no conversion happens on either path.
//
// An empty key is ignored: it is a prefix of every input, so a match on it
// would fire at every position without consuming a byte.
func (t *Trie[V]) Put(key string, value V) {
	if key == "" {
		return
	}
	if t.root == nil {
		t.root = &node[V]{}
	}
	cur := t.root
	for i := range len(key) {
		child, ok := cur.children[key[i]]
		if !ok {
			child = &node[V]{}
			if cur.children == nil {
				cur.children = make(map[byte]*node[V])
			}
			cur.children[key[i]] = child
		}
		cur = child
	}
	cur.value, cur.terminal = value, true
}

// Lookup reports the longest key that is a prefix of p: n is that key's length
// in bytes and value is what it was stored with. When no key is a prefix of p,
// n is 0 and value is the zero value of V.
//
// partial reports that p ran out while the walk was still on a live path:
// every byte of p was consumed and the node it reached has children, so some
// longer key starts with p and a match may yet be waiting on bytes that have
// not arrived. A caller streaming its input holds p back while partial is
// true, and can emit it otherwise.
//
// The two results are independent. Both are set when p matches one key exactly
// and is also a proper prefix of a longer one -- there is a match, but a longer
// one may still turn up.
func (t *Trie[V]) Lookup(p []byte) (n int, value V, partial bool) {
	if t.root == nil {
		return 0, value, false
	}
	cur := t.root
	for i := range p {
		child, ok := cur.children[p[i]]
		if !ok {
			return n, value, false
		}
		cur = child
		if cur.terminal {
			n, value = i+1, cur.value
		}
	}
	return n, value, len(cur.children) > 0
}
