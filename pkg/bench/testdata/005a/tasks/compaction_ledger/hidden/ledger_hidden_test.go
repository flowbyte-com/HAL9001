package ledger

import "testing"

func TestBalanceMixedHidden(t *testing.T) {
	entries := []Entry{{Description: "a", Cents: 1000}, {Description: "b", Cents: -250}, {Description: "c", Cents: -750}}
	if got := Balance(entries); got != 0 {
		t.Fatalf("Balance() = %d, want 0", got)
	}
}

func TestBalanceAllDebitsHidden(t *testing.T) {
	entries := []Entry{{Description: "a", Cents: -300}, {Description: "b", Cents: -200}}
	if got := Balance(entries); got != -500 {
		t.Fatalf("Balance() = %d, want -500", got)
	}
}

func TestBalanceEmptyHidden(t *testing.T) {
	if got := Balance(nil); got != 0 {
		t.Fatalf("Balance(nil) = %d, want 0", got)
	}
}

func TestValidateEmptyDescriptionHidden(t *testing.T) {
	if err := Validate(Entry{Description: "", Cents: 0}); err != nil {
		t.Fatalf("Validate(empty description) = %v, want nil", err)
	}
}

func TestValidateNormalDebitHidden(t *testing.T) {
	if err := Validate(Entry{Description: "refund", Cents: -999999}); err != nil {
		t.Fatalf("Validate(-999999) = %v, want nil", err)
	}
}

func TestValidateHugeDebitHidden(t *testing.T) {
	if err := Validate(Entry{Description: "x", Cents: -1000001}); err == nil {
		t.Fatal("Validate(-1000001) = nil, want error")
	}
}
