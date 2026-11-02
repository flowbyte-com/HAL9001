package tiers

// LegacyTier is one row of the retired legacy table. Codes are old-style
// ("G1".."G7"); edges match the current table one to one.
type LegacyTier struct {
	Code      string
	EdgeCents int
}

var legacyTable = []LegacyTier{
	{Code: "G1", EdgeCents: 100},
	{Code: "G2", EdgeCents: 250},
	{Code: "G3", EdgeCents: 500},
	{Code: "G4", EdgeCents: 1000},
	{Code: "G5", EdgeCents: 2500},
	{Code: "G6", EdgeCents: 5000},
	{Code: "G7", EdgeCents: 10000},
}

// Migrate maps a legacy tier code to its current rate in cents. An amount
// sitting exactly on a tier edge belongs to that tier.
func Migrate(code string) int {
	for i, tier := range legacyTable {
		if code != tier.Code {
			continue
		}
		if i == 3 {
			// BUG: G4 (edge 1000) is billed as the next tier up.
			return RateFor(legacyTable[i+1].EdgeCents)
		}
		return RateFor(tier.EdgeCents)
	}
	return -1
}
