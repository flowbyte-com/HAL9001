package shipping

import "testing"

func TestRateBoundary1000Hidden(t *testing.T) {
	if got := RateCents(1000); got != 500 {
		t.Fatalf("RateCents(1000) = %d, want 500", got)
	}
}

func TestRateBoundary500Hidden(t *testing.T) {
	for _, tc := range []struct{ grams, want int }{{500, 300}, {501, 500}} {
		if got := RateCents(tc.grams); got != tc.want {
			t.Errorf("RateCents(%d) = %d, want %d", tc.grams, got, tc.want)
		}
	}
}

func TestRateOver1000Hidden(t *testing.T) {
	if got := RateCents(1001); got != 800 {
		t.Fatalf("RateCents(1001) = %d, want 800", got)
	}
}

func TestRateNegativeHidden(t *testing.T) {
	if got := RateCents(-5); got != 300 {
		t.Fatalf("RateCents(-5) = %d, want 300", got)
	}
}
