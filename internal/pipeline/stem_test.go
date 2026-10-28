package pipeline

import "testing"

func TestStrip_RemovesTheNoiseTables(t *testing.T) {
	strip, err := newStripper(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ in, want string }{
		{"ABC-123-cd1-1080p", "ABC-123-cd1"},
		{"[javdb.com]ABC-123", "ABC-123"},
		{"hhd800.com@ABC-123", "ABC-123"},
		{"ABC-123-x264-720p", "ABC-123"},
		{"ABC-123-U", "ABC-123"},
		{"ABC-123-C", "ABC-123"},
		{"ABC-123-chs", "ABC-123"},
		{"ABC-123-1080p-123", "ABC-123-123"},
		{"9ofje121A", "9ofje121A"},
		{"SSVR-122-cd1", "SSVR-122-cd1"},
	} {
		if got := strip.strip(tc.in); got != tc.want {
			t.Errorf("strip(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStrip_SanitizeIsLiteralOrRegex(t *testing.T) {
	strip, err := newStripper([]string{"-sample", `r:-web$`})
	if err != nil {
		t.Fatal(err)
	}
	if got := strip.strip("ABC-123-cd1-sample"); got != "ABC-123-cd1" {
		t.Errorf("a bare entry is a literal: got %q", got)
	}
	if got := strip.strip("ABC-123-cd2-web"); got != "ABC-123-cd2" {
		t.Errorf("r: marks a regex: got %q", got)
	}
	if got := strip.strip("ABC-123-r-web"); got != "ABC-123-r" {
		t.Errorf("a literal entry is taken as written, not as a regex: got %q", got)
	}
}

func TestPartAt_Table(t *testing.T) {
	for _, tc := range []struct {
		in    string
		stem  string
		text  string
		index int
		tier  markerTier
	}{
		// confident: the name says "part" out loud
		{"ABC-123-cd1", "ABC-123", "cd1", 1, tierConfident},
		{"ABC-123-CD2", "ABC-123", "CD2", 2, tierConfident},
		{"ABC-123-disc10", "ABC-123", "disc10", 10, tierConfident},
		{"ABC-123.part3", "ABC-123", "part3", 3, tierConfident},
		{"ABC-123 vol 4", "ABC-123", "vol 4", 4, tierConfident},
		{"ABC-123-A", "ABC-123", "A", 1, tierConfident},
		{"ABC-123-B", "ABC-123", "B", 2, tierConfident},
		{"ABC-123-D", "ABC-123", "D", 4, tierConfident},
		// conditional: needs a sibling's agreement
		{"ABC-123-1", "ABC-123", "1", 1, tierConditional},
		{"ABC-123-12", "ABC-123", "12", 12, tierConditional},
		{"9ofje121A", "9ofje121", "A", 1, tierConditional},
		{"9ofje121B", "9ofje121", "B", 2, tierConditional},
		// neither: the whole name is the stem
		{"ABP-123", "ABP-123", "", 0, tierNone},
		{"ABC-1234", "ABC-1234", "", 0, tierNone},   // -1234 is a number, not part 34
		{"ABC-123-C", "ABC-123-C", "", 0, tierNone}, // C is a copy flag, never part 3
		{"ABCD1", "ABCD", "1", 1, tierConditional},  // the CD1 token needs a separator, a trailing digit does not
	} {
		got := partAt(tc.in, nil)
		if got.stem != tc.stem || got.text != tc.text || got.index != tc.index || got.tier != tc.tier {
			t.Errorf("partAt(%q) = {stem:%q text:%q index:%d tier:%d}, want {stem:%q text:%q index:%d tier:%d}",
				tc.in, got.stem, got.text, got.index, got.tier, tc.stem, tc.text, tc.index, tc.tier)
		}
	}
}

func TestMarker_ZeroWhenThereIsNoTier(t *testing.T) {
	if m := (tail{stem: "ABC", tier: tierNone}).marker(); m != (Marker{}) {
		t.Errorf("marker = %+v, want the zero value", m)
	}
}
