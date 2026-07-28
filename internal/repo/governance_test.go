package repo

import "testing"

func TestRequiredQuorum(t *testing.T) {
	cases := []struct {
		mode   string
		value  int
		active int
		want   int
	}{
		{"majority", 0, 10, 6},    // floor(10/2)+1
		{"majority", 0, 11, 6},    // floor(11/2)+1
		{"majority", 0, 1, 1},     // a single member
		{"majority", 0, 0, 1},     // degenerate: 0/2+1
		{"fixed", 5, 100, 5},      // absolute count, ignores roster
		{"fixed", 5, 3, 5},        // can exceed the roster (unreachable quorum)
		{"percent", 50, 10, 5},    // ceil(10*50/100)
		{"percent", 50, 11, 6},    // ceil(11*50/100)=ceil(5.5)=6
		{"percent", 100, 8, 8},    // everyone
		{"percent", 33, 10, 4},    // ceil(3.3)
		{"percent", 1, 200, 2},    // ceil(2.0)
		{"unknownmode", 0, 10, 6}, // falls back to majority
	}
	for _, c := range cases {
		if got := RequiredQuorum(c.mode, c.value, c.active); got != c.want {
			t.Errorf("RequiredQuorum(%q,%d,active=%d)=%d, want %d", c.mode, c.value, c.active, got, c.want)
		}
	}
}

func TestComputeMotionOutcome(t *testing.T) {
	cases := []struct {
		threshold string
		vFor      int
		against   int
		want      bool
	}{
		{"majority", 6, 5, true},
		{"majority", 5, 5, false}, // tie fails a simple majority
		{"majority", 0, 0, false}, // no support
		{"majority", 3, 0, true},
		{"two_thirds", 2, 1, true},  // 2/3 exactly
		{"two_thirds", 2, 2, false}, // only 1/2
		{"two_thirds", 4, 2, true},  // 2/3
		{"two_thirds", 3, 2, false}, // 3/5 < 2/3
		{"unanimous", 5, 0, true},
		{"unanimous", 5, 1, false},
		{"unanimous", 0, 0, false}, // nobody voted for
	}
	for _, c := range cases {
		if got := ComputeMotionOutcome(c.threshold, c.vFor, c.against); got != c.want {
			t.Errorf("ComputeMotionOutcome(%q, for=%d, against=%d)=%v, want %v", c.threshold, c.vFor, c.against, got, c.want)
		}
	}
}
