package tiers

import "testing"

func TestMigrateExactHidden(t *testing.T) {
	cases := []struct {
		code string
		want int
	}{
		{"G1", 300},
		{"G2", 400},
		{"G4", 650},
		{"G6", 950},
		{"G7", 1100},
		{"G0", -1},
		{"", -1},
	}
	for _, tc := range cases {
		if got := Migrate(tc.code); got != tc.want {
			t.Errorf("Migrate(%q) = %d, want %d", tc.code, got, tc.want)
		}
	}
}

func TestMigrateIdempotentHidden(t *testing.T) {
	// Migrating twice through the rate table must agree with a direct
	// lookup: the migrated rate for an edge must equal the current table
	// rate for that edge.
	for _, tier := range legacyTable {
		if got, want := Migrate(tier.Code), RateFor(tier.EdgeCents); got != want {
			t.Errorf("Migrate(%q) = %d, RateFor(%d) = %d", tier.Code, got, tier.EdgeCents, want)
		}
	}
}

func TestRoundEdgeBankersHidden(t *testing.T) {
	cases := []struct {
		amount float64
		want   int
	}{
		{104.5, 104},
		{105.5, 106},
		{1000.5, 1000},
		{1001.5, 1002},
		{2.5, 2},
		{-104.5, -104},
	}
	for _, tc := range cases {
		if got := RoundEdge(tc.amount); got != tc.want {
			t.Errorf("RoundEdge(%v) = %d, want %d", tc.amount, got, tc.want)
		}
	}
}

func TestRoundEdgeNonTieHidden(t *testing.T) {
	for _, tc := range []struct {
		amount float64
		want   int
	}{{104.4, 104}, {104.6, 105}, {999.9, 1000}, {1000.1, 1000}} {
		if got := RoundEdge(tc.amount); got != tc.want {
			t.Errorf("RoundEdge(%v) = %d, want %d", tc.amount, got, tc.want)
		}
	}
}
