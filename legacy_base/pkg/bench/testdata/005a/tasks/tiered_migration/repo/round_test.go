package tiers

import "testing"

func TestRoundEdgeTies(t *testing.T) {
	cases := []struct {
		amount float64
		want   int
	}{
		{104.5, 104},
		{105.5, 106},
		{106.5, 106},
		{0.5, 0},
	}
	for _, tc := range cases {
		if got := RoundEdge(tc.amount); got != tc.want {
			t.Errorf("RoundEdge(%v) = %d, want %d", tc.amount, got, tc.want)
		}
	}
}
