package billing

import "testing"

func TestNonTieRounding(t *testing.T) {
	cases := []struct {
		amount float64
		want   int
	}{
		{104.4, 104},
		{104.6, 105},
		{0, 0},
		{12.01, 12},
	}
	for _, tc := range cases {
		if got := RoundCents(tc.amount); got != tc.want {
			t.Errorf("RoundCents(%v) = %d, want %d", tc.amount, got, tc.want)
		}
	}
}
