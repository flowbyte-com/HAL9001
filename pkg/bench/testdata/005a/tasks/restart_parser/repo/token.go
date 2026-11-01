package parser

import "strconv"

// ParseInt parses a decimal integer.
func ParseInt(s string) (int, error) {
	return strconv.Atoi(s)
}
