package tiers

// Tier is one row of the current rate table. EdgeCents is the inclusive
// upper bound of the tier in cents; RateCents is the price for the tier.
type Tier struct {
	Code      string
	EdgeCents int
	RateCents int
}

// CurrentTable is the finance-approved rate table. Tier edges are exact:
// an amount of exactly EdgeCents belongs to that tier, not the next one.
var CurrentTable = []Tier{
	{Code: "T1", EdgeCents: 100, RateCents: 300},
	{Code: "T2", EdgeCents: 250, RateCents: 400},
	{Code: "T3", EdgeCents: 500, RateCents: 500},
	{Code: "T4", EdgeCents: 1000, RateCents: 650},
	{Code: "T5", EdgeCents: 2500, RateCents: 800},
	{Code: "T6", EdgeCents: 5000, RateCents: 950},
	{Code: "T7", EdgeCents: 10000, RateCents: 1100},
}

// RateFor returns the rate in cents for an amount, using the current table.
func RateFor(amountCents int) int {
	for _, tier := range CurrentTable {
		if amountCents <= tier.EdgeCents {
			return tier.RateCents
		}
	}
	return CurrentTable[len(CurrentTable)-1].RateCents
}
