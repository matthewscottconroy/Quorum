package repo

import (
	"testing"
	"time"
)

func TestPeriodFor(t *testing.T) {
	// A fixed reference date: 2026-08-15 (Q3, August).
	ref := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		cadence   string
		dueDays   int
		wantLabel string
		wantStart string // YYYY-MM-DD
		wantDue   string
	}{
		{"annual", 30, "2026", "2026-01-01", "2026-01-31"},
		{"quarterly", 0, "2026-Q3", "2026-07-01", "2026-07-01"},
		{"monthly", 14, "2026-08", "2026-08-01", "2026-08-15"},
		{"annual", 0, "2026", "2026-01-01", "2026-01-01"},
	}
	for _, c := range cases {
		label, start, due := PeriodFor(c.cadence, c.dueDays, ref)
		if label != c.wantLabel {
			t.Errorf("PeriodFor(%s).label = %q, want %q", c.cadence, label, c.wantLabel)
		}
		if got := start.Format("2006-01-02"); got != c.wantStart {
			t.Errorf("PeriodFor(%s).start = %s, want %s", c.cadence, got, c.wantStart)
		}
		if got := due.Format("2006-01-02"); got != c.wantDue {
			t.Errorf("PeriodFor(%s, due=%d).due = %s, want %s", c.cadence, c.dueDays, got, c.wantDue)
		}
	}
}

func TestPeriodFor_QuarterBoundaries(t *testing.T) {
	q := map[int]string{1: "2026-Q1", 3: "2026-Q1", 4: "2026-Q2", 6: "2026-Q2", 7: "2026-Q3", 9: "2026-Q3", 10: "2026-Q4", 12: "2026-Q4"}
	for month, want := range q {
		ref := time.Date(2026, time.Month(month), 10, 0, 0, 0, 0, time.UTC)
		if label, _, _ := PeriodFor("quarterly", 0, ref); label != want {
			t.Errorf("month %d: got %q, want %q", month, label, want)
		}
	}
}
