package billing

import "testing"

func TestBankersTiesHidden(t *testing.T) {
	cases := []struct {
		amount float64
		want   int
	}{
		{104.5, 104},
		{105.5, 106},
		{106.5, 106},
		{2.5, 2},
		{3.5, 4},
	}
	for _, tc := range cases {
		if got := RoundCents(tc.amount); got != tc.want {
			t.Errorf("RoundCents(%v) = %d, want %d", tc.amount, got, tc.want)
		}
	}
}

func TestBankersNegativeHidden(t *testing.T) {
	for _, tc := range []struct {
		amount float64
		want   int
	}{{-104.5, -104}, {-105.5, -106}} {
		if got := RoundCents(tc.amount); got != tc.want {
			t.Errorf("RoundCents(%v) = %d, want %d", tc.amount, got, tc.want)
		}
	}
}

func TestNonTieUnaffectedHidden(t *testing.T) {
	for _, tc := range []struct {
		amount float64
		want   int
	}{{104.4, 104}, {104.6, 105}, {0.49, 0}} {
		if got := RoundCents(tc.amount); got != tc.want {
			t.Errorf("RoundCents(%v) = %d, want %d", tc.amount, got, tc.want)
		}
	}
}
