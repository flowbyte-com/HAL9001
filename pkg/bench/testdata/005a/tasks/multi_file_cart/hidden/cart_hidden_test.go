package cart

import "testing"

func TestDiscountSave15Hidden(t *testing.T) {
	if got := DiscountCents(2000, "SAVE15"); got != 300 {
		t.Fatalf("DiscountCents(2000, SAVE15) = %d, want 300", got)
	}
}

func TestDiscountSave10Hidden(t *testing.T) {
	if got := DiscountCents(2000, "SAVE10"); got != 200 {
		t.Fatalf("DiscountCents(2000, SAVE10) = %d, want 200", got)
	}
}

func TestDiscountUnknownHidden(t *testing.T) {
	for _, code := range []string{"", "FREE", "save99"} {
		if got := DiscountCents(2000, code); got != 0 {
			t.Errorf("DiscountCents(2000, %q) = %d, want 0", code, got)
		}
	}
}

func TestDiscountNormalizesCodeHidden(t *testing.T) {
	if got := DiscountCents(2000, " save15 "); got != 300 {
		t.Fatalf("DiscountCents(2000, \" save15 \") = %d, want 300", got)
	}
}

func TestFormatPaddingHidden(t *testing.T) {
	cases := map[int]string{0: "$0.00", 5: "$0.05", 99: "$0.99", 100: "$1.00", 1001: "$10.01"}
	for cents, want := range cases {
		if got := FormatCents(cents); got != want {
			t.Errorf("FormatCents(%d) = %q, want %q", cents, got, want)
		}
	}
}

func TestSubtotalEmptyHidden(t *testing.T) {
	if got := (Cart{}).SubtotalCents(); got != 0 {
		t.Fatalf("empty cart subtotal = %d, want 0", got)
	}
}
