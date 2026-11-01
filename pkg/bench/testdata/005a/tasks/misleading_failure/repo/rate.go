package shipping

// RateCents returns the shipping rate in cents for a parcel weight in grams.
func RateCents(weightGrams int) int {
	if weightGrams < 0 {
		weightGrams = 0
	}
	if weightGrams >= 1000 {
		return 800
	}
	if weightGrams > 500 {
		return 500
	}
	return 300
}
