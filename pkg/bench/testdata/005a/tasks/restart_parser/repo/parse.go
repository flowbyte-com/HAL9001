package parser

import "strings"

// ParseList splits a comma-separated list.
func ParseList(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}
