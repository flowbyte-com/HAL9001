package shipping

import "testing"

func TestRateTiers(t *testing.T) {
	cases := []struct {
		grams int
		want  int
	}{
		{0, 300},
		{500, 300},
		{501, 500},
		{1000, 500},
		{1001, 800},
	}
	for _, tc := range cases {
		if got := RateCents(tc.grams); got != tc.want {
			t.Errorf("RateCents(%d) = %d, want %d", tc.grams, got, tc.want)
		}
	}
}
