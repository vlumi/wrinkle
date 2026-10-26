package uploads

import (
	"testing"
	"time"
)

func TestPendingVerdict(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	hot := 30 * 24 * time.Hour
	for _, tc := range []struct {
		name string
		sent bool
		age  time.Duration
		want verdict
	}{
		{"delivered late", true, 2 * 24 * time.Hour, keep},
		{"failed send", false, 2 * 24 * time.Hour, drop},
		// Its message may be in the cold tier by now.
		{"older than half the hot window", false, 20 * 24 * time.Hour, keep},
		{"sent, long ago", true, 40 * 24 * time.Hour, keep},
	} {
		if got := pendingVerdict(tc.sent, now.Add(-tc.age), now, hot); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}
