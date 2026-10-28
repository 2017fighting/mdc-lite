package pipeline

// Levenshtein distance, and the BK-tree built from it. `group` uses the plain
// distance up to `max_files_for_levenshtein` videos in a directory and the tree
// above it, which is what keeps one huge directory from turning into a
// quadratic amount of distance computations. Both paths answer the same
// question, so they must agree — a test asserts it.

// runes turns a string into runes so that a Japanese or Chinese stem counts one
// per character rather than one per UTF-8 byte.
func runes(s string) []rune { return []rune(s) }

// distance is the Levenshtein distance between a and b, over runes.
func distance(a, b string) int {
	ra, rb := runes(a), runes(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}
	// One row of the full matrix is enough: distance only ever looks one row back.
	prev := make([]int, len(rb)+1)
	curr := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		curr[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			curr[j] = min3(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(rb)]
}

// withinDistance is `distance` with a ceiling: it returns the distance when it is
// at most max, and (max+1, false) otherwise. Every cell in a row is at least its
// row index, so a row whose minimum already exceeds max cannot come back down.
func withinDistance(a, b string, max int) (int, bool) {
	if max < 0 {
		return max + 1, false
	}
	if d := abs(len(runes(a)) - len(runes(b))); d > max {
		return d, false
	}
	ra, rb := runes(a), runes(b)
	prev := make([]int, len(rb)+1)
	curr := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		curr[0] = i
		rowMin := curr[0]
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			curr[j] = min3(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
			if curr[j] < rowMin {
				rowMin = curr[j]
			}
		}
		if rowMin > max {
			return max + 1, false
		}
		prev, curr = curr, prev
	}
	if prev[len(rb)] > max {
		return max + 1, false
	}
	return prev[len(rb)], true
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// bkTree is a Burkhard-Keller tree over stems: it answers "which stems are within
// distance d of this one" while comparing against a handful of them instead of
// all of them. Distances are integers, so each node keeps a child per distance.
type bkTree struct {
	root *bkNode
	// ids maps a stem to every index that has it, because two files in one
	// directory can share a stem (`ABC-123-1.mp4` and `ABC-123-1.mkv`).
	ids map[string][]int
}

type bkNode struct {
	key      string
	children map[int]*bkNode
}

func newBKTree() *bkTree { return &bkTree{ids: map[string][]int{}} }

// add inserts one stem, remembering the index it came from.
func (t *bkTree) add(key string, idx int) {
	t.ids[key] = append(t.ids[key], idx)
	if t.root == nil {
		t.root = &bkNode{key: key}
		return
	}
	node := t.root
	for {
		d := distance(key, node.key)
		if d == 0 {
			return
		}
		if node.children == nil {
			node.children = map[int]*bkNode{}
		}
		next, ok := node.children[d]
		if !ok {
			node.children[d] = &bkNode{key: key}
			return
		}
		node = next
	}
}

// within calls fn with every index whose stem is at most d away from key, the
// key's own indices included.
func (t *bkTree) within(key string, d int, fn func(idx int)) {
	var walk func(*bkNode)
	walk = func(node *bkNode) {
		if node == nil {
			return
		}
		dist := distance(key, node.key)
		if dist <= d {
			for _, idx := range t.ids[node.key] {
				fn(idx)
			}
		}
		for childDist, child := range node.children {
			// A child at distance childDist from this node can only hold keys within
			// d of the query if the two distances are within d of each other.
			if childDist >= dist-d && childDist <= dist+d {
				walk(child)
			}
		}
	}
	walk(t.root)
}
