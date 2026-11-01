package tiers

import "testing"

// TestDryRunMapping prints the full legacy-to-current mapping table. It
// always passes; its output is the diagnostic evidence for the migration
// fix. Each row shows the legacy code, the edge amount, the migrated rate,
// and the rounded edge amount the billing step consumes.
func TestDryRunMapping(t *testing.T) {
	for _, tier := range legacyTable {
		for _, delta := range []int{-1, 0, 1} {
			amount := float64(tier.EdgeCents + delta)
			t.Logf("tier %s: legacy %d delta %+d -> amount %.2f -> migrated %d -> rounded %d",
				tier.Code, tier.EdgeCents, delta, amount, Migrate(tier.Code), RoundEdge(amount))
		}
	}
}

func TestMigrateBoundary(t *testing.T) {
	cases := []struct {
		code string
		want int
	}{
		{"G1", 300},
		{"G3", 500},
		{"G4", 650},
		{"G5", 800},
		{"GX", -1},
	}
	for _, tc := range cases {
		if got := Migrate(tc.code); got != tc.want {
			t.Errorf("Migrate(%q) = %d, want %d", tc.code, got, tc.want)
		}
	}
}
