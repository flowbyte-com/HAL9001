package ledger

import "testing"

func TestBalanceIncludesDebits(t *testing.T) {
	entries := []Entry{
		{Description: "sale", Cents: 5000},
		{Description: "refund", Cents: -1200},
	}
	if got := Balance(entries); got != 3800 {
		t.Fatalf("Balance() = %d, want 3800", got)
	}
}

func TestValidateAllowsEmptyDescription(t *testing.T) {
	if err := Validate(Entry{Description: "", Cents: -250}); err != nil {
		t.Fatalf("Validate() rejected an automatic adjustment: %v", err)
	}
}

func TestValidateRejectsHugeDebit(t *testing.T) {
	if err := Validate(Entry{Description: "x", Cents: -1000001}); err == nil {
		t.Fatal("Validate() accepted an amount below the limit")
	}
}
