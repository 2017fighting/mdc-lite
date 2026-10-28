package pipeline

import "testing"

func TestDistance(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"", "abc", 3},
		{"abc", "", 3},
		{"abc", "abc", 0},
		{"SSVR-122", "SSVR-123", 1},
		{"ABC-123", "ABD-199", 3},
		{"kitten", "sitting", 3},
		// runes, not bytes: a Japanese stem is counted per character
		{"日本語", "日本語版", 1},
	} {
		if got := distance(tc.a, tc.b); got != tc.want {
			t.Errorf("distance(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestWithinDistance_AgreesWithTheFullDistance(t *testing.T) {
	pairs := [][2]string{
		{"SSVR-122", "SSVR-123"},
		{"ABC-123", "ABD-199"},
		{"ABC-123", "ABC-123"},
		{"kitten", "sitting"},
		{"", "abc"},
		{"日本語", "日本語版"},
	}
	for _, p := range pairs {
		full := distance(p[0], p[1])
		for max := 0; max <= 4; max++ {
			got, ok := withinDistance(p[0], p[1], max)
			if want := full <= max; ok != want {
				t.Errorf("withinDistance(%q, %q, %d) ok = %v, want %v", p[0], p[1], max, ok, want)
			}
			if ok && got != full {
				t.Errorf("withinDistance(%q, %q, %d) = %d, want %d", p[0], p[1], max, got, full)
			}
		}
	}
}

func TestBKTree_FindsEveryNeighbourThePairwiseScanWould(t *testing.T) {
	keys := []string{
		"SSVR-122", "SSVR-123", "SSVR-125", "SSVR-130",
		"ABC-123", "ABD-199", "XYZ-999", "日本語", "日本語版",
	}
	tree := newBKTree()
	for i, k := range keys {
		tree.add(k, i)
	}
	const radius = 2
	for _, q := range keys {
		want := map[int]bool{}
		for i, k := range keys {
			if _, ok := withinDistance(q, k, radius); ok {
				want[i] = true
			}
		}
		got := map[int]bool{}
		tree.within(q, radius, func(i int) { got[i] = true })
		if len(got) != len(want) {
			t.Fatalf("within(%q, %d) = %v, want %v", q, radius, got, want)
		}
		for i := range want {
			if !got[i] {
				t.Errorf("within(%q, %d) missed %q", q, radius, keys[i])
			}
		}
	}
}
