package billing

import "math"

// RoundCents rounds a currency amount to the nearest cent.
func RoundCents(amount float64) int {
	return int(math.Trunc(amount))
}
