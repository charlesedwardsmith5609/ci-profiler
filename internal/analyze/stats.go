// Package analyze turns a loaded Dataset into per-workflow timing results. It is pure: it only
// reads its input and returns new values the caller owns.
package analyze

import (
	"math"
	"sort"
	"time"
)

// maxDuration is the longest single job, step or run duration accepted as real. Anything longer
// is a bad timestamp; this also keeps sums far from overflowing in normal use.
const maxDuration = 60 * 24 * time.Hour

// Summary describes a set of durations. Overflow is true when the total could not be
// represented; P50 and P95 are still valid because they are single samples.
type Summary struct {
	N        int
	P50, P95 time.Duration
	Total    time.Duration
	Overflow bool
}

// addDur adds two non-negative durations and reports whether the sum overflowed.
func addDur(a, b time.Duration) (time.Duration, bool) {
	if a > math.MaxInt64-b {
		return math.MaxInt64, true
	}
	return a + b, false
}

// Percentile returns the nearest-rank percentile (rank = ceil(p/100*n)) of sorted values.
// It returns 0 for an empty slice.
func Percentile(sorted []time.Duration, p int) time.Duration {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	rank := (p*n + 99) / 100
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sorted[rank-1]
}

// Summarize computes a Summary; it sorts a copy, never the caller's slice.
func Summarize(ds []time.Duration) Summary {
	s := Summary{N: len(ds)}
	if len(ds) == 0 {
		return s
	}
	c := append([]time.Duration(nil), ds...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	s.P50, s.P95 = Percentile(c, 50), Percentile(c, 95)
	for _, d := range c {
		var o bool
		if s.Total, o = addDur(s.Total, d); o {
			s.Overflow = true
		}
	}
	return s
}

// span returns end-start and whether both are set, ordered and within maxDuration.
func span(start, end time.Time) (time.Duration, bool) {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return 0, false
	}
	if end.Sub(start) > maxDuration || end.Sub(start) < 0 {
		// Sub saturates at the Duration limits, so a negative or huge value means overflow.
		return 0, false
	}
	return end.Sub(start), true
}
