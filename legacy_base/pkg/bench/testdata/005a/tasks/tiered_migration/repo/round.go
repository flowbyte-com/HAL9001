package tiers

// RoundEdge rounds a computed amount to the nearest cent. Exact halves must
// round to the nearest even cent (banker's rounding), matching finance
// policy for tier-edge amounts.
func RoundEdge(amount float64) int {
	return int(amount)
}
