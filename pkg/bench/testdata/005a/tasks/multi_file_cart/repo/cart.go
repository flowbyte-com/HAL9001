package cart

// Item is one line in a shopping cart.
type Item struct {
	Name       string
	PriceCents int
	Quantity   int
}

// Cart holds the items being priced.
type Cart struct {
	Items []Item
}

// SubtotalCents returns the undiscounted total in cents.
func (c Cart) SubtotalCents() int {
	total := 0
	for _, item := range c.Items {
		total += item.PriceCents * item.Quantity
	}
	return total
}
