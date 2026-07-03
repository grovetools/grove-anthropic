package anthropic

import (
	"sort"
	"testing"
)

// indices flattens a breakpoint set into a sorted slice for stable comparison.
func indices(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for i := range m {
		out = append(out, i)
	}
	sort.Ints(out)
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestCacheBreakpointIndices(t *testing.T) {
	tests := []struct {
		name        string
		total       int
		stableCount int
		pinnedCount int
		noCache     bool
		want        []int
	}{
		// Zero-pinned invariant: identical to the pre-pinned two-breakpoint set
		// {stableCount-1, total-1}. These cases must match today's behavior byte
		// for byte.
		{"stable+volatile, no pinned", 5, 2, 0, false, []int{1, 4}},
		{"stable only, no pinned", 3, 3, 0, false, []int{2}},
		{"no stable, no pinned (last-doc only)", 4, 0, 0, false, []int{3}},
		{"single doc, no stable/pinned", 1, 0, 0, false, []int{0}},

		// Pinned present.
		{"stable+pinned+volatile", 6, 2, 2, false, []int{1, 3, 5}},
		{"stable+pinned, no volatile (pinned==last coincide)", 4, 2, 2, false, []int{1, 3}},
		{"no stable, pinned+volatile", 5, 0, 2, false, []int{1, 4}},
		{"pinned only (all three coincide on last)", 3, 0, 3, false, []int{2}},
		{"stable+pinned only, pinned is last", 5, 3, 2, false, []int{2, 4}},

		// NoCache disables everything regardless of partition.
		{"noCache with pinned", 6, 2, 2, true, nil},
		{"noCache no pinned", 5, 2, 0, true, nil},

		// Degenerate: no documents at all.
		{"empty", 0, 0, 0, false, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := indices(cacheBreakpointIndices(tt.total, tt.stableCount, tt.pinnedCount, tt.noCache))
			want := tt.want
			if want == nil {
				want = []int{}
			}
			if got == nil {
				got = []int{}
			}
			if !equalInts(got, want) {
				t.Errorf("cacheBreakpointIndices(%d, %d, %d, %v) = %v, want %v",
					tt.total, tt.stableCount, tt.pinnedCount, tt.noCache, got, want)
			}
		})
	}
}

// TestCacheBreakpointIndices_NeverExceedsBudget asserts we never place more than
// the 3 breakpoints this design uses (well within the API's 4-breakpoint limit),
// across a wide range of partitions.
func TestCacheBreakpointIndices_NeverExceedsBudget(t *testing.T) {
	for total := 0; total <= 8; total++ {
		for stable := 0; stable <= total; stable++ {
			for pinned := 0; pinned <= total-stable; pinned++ {
				bps := cacheBreakpointIndices(total, stable, pinned, false)
				if len(bps) > 3 {
					t.Errorf("total=%d stable=%d pinned=%d produced %d breakpoints (>3): %v",
						total, stable, pinned, len(bps), indices(bps))
				}
				// Every index must be in range.
				for i := range bps {
					if i < 0 || i >= total {
						t.Errorf("total=%d stable=%d pinned=%d produced out-of-range index %d",
							total, stable, pinned, i)
					}
				}
			}
		}
	}
}
