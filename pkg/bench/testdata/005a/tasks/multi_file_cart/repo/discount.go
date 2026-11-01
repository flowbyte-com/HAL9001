package cart

import "strings"

// DiscountCents returns the discount in cents for a promotion code.
// Today every non-empty code receives the same ten-percent discount.
func DiscountCents(subtotalCents int, code string) int {
	if strings.TrimSpace(code) == "" {
		return 0
	}
	return subtotalCents / 10
}
