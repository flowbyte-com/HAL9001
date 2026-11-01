package ledger

import "errors"

// Validate checks whether an entry may be posted.
func Validate(entry Entry) error {
	if entry.Description == "" {
		return errors.New("description required")
	}
	if entry.Cents < -1000000 {
		return errors.New("amount too large")
	}
	return nil
}
