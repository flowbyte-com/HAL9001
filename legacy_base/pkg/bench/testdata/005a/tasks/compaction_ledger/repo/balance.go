package ledger

// Entry is one ledger line. Positive cents are credits, negative are debits.
type Entry struct {
	Description string
	Cents       int
}

// Balance returns the net balance in cents.
func Balance(entries []Entry) int {
	total := 0
	for _, entry := range entries {
		if entry.Cents > 0 {
			total += entry.Cents
		}
	}
	return total
}
