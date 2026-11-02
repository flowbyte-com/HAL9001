package cart

import "fmt"

// FormatCents renders a cent amount as a dollar string.
func FormatCents(cents int) string {
	return fmt.Sprintf("$%d.%d", cents/100, cents%100)
}
