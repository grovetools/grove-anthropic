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
		noCache     bool
		want        []int
	}{
		// The pinned-free legacy contract: exactly the two-breakpoint set
		// {stableCount-1, total-1}. These cases must match the historical
		// (pre-pinned, pre-ladder) behavior byte for byte.
		{"stable+volatile", 5, 2, false, []int{1, 4}},
		{"stable only", 3, 3, false, []int{2}},
		{"no stable (last-doc only)", 4, 0, false, []int{3}},
		{"single doc, no stable", 1, 0, false, []int{0}},
		{"single stable doc (both coincide)", 1, 1, false, []int{0}},

		// NoCache disables everything regardless of partition.
		{"noCache", 5, 2, true, nil},

		// Degenerate: no documents at all.
		{"empty", 0, 0, false, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := indices(cacheBreakpointIndices(tt.total, tt.stableCount, tt.noCache))
			want := tt.want
			if want == nil {
				want = []int{}
			}
			if got == nil {
				got = []int{}
			}
			if !equalInts(got, want) {
				t.Errorf("cacheBreakpointIndices(%d, %d, %v) = %v, want %v",
					tt.total, tt.stableCount, tt.noCache, got, want)
			}
		})
	}
}

// TestCacheBreakpointIndices_NeverExceedsBudget asserts we never place more than
// the 2 breakpoints this design uses (well within the API's 4-breakpoint limit),
// across a wide range of partitions.
func TestCacheBreakpointIndices_NeverExceedsBudget(t *testing.T) {
	for total := 0; total <= 8; total++ {
		for stable := 0; stable <= total; stable++ {
			bps := cacheBreakpointIndices(total, stable, false)
			if len(bps) > 2 {
				t.Errorf("total=%d stable=%d produced %d breakpoints (>2): %v",
					total, stable, len(bps), indices(bps))
			}
			// Every index must be in range.
			for i := range bps {
				if i < 0 || i >= total {
					t.Errorf("total=%d stable=%d produced out-of-range index %d",
						total, stable, i)
				}
			}
		}
	}
}
