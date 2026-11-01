package cart

import "testing"

func TestSubtotal(t *testing.T) {
	cart := Cart{Items: []Item{
		{Name: "pen", PriceCents: 250, Quantity: 2},
		{Name: "pad", PriceCents: 500, Quantity: 1},
	}}
	if got := cart.SubtotalCents(); got != 1000 {
		t.Fatalf("SubtotalCents() = %d, want 1000", got)
	}
}

func TestSave15Discount(t *testing.T) {
	if got := DiscountCents(1000, "SAVE15"); got != 150 {
		t.Fatalf("DiscountCents(1000, SAVE15) = %d, want 150", got)
	}
}

func TestFormatCents(t *testing.T) {
	cases := map[int]string{
		5:    "$0.05",
		1205: "$12.05",
		1000: "$10.00",
	}
	for cents, want := range cases {
		if got := FormatCents(cents); got != want {
			t.Errorf("FormatCents(%d) = %q, want %q", cents, got, want)
		}
	}
}
