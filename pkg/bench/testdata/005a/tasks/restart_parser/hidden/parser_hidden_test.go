package parser

import (
	"reflect"
	"testing"
)

func TestParseIntWhitespaceHidden(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{{"\t-7\n", -7}, {"  0  ", 0}} {
		got, err := ParseInt(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("ParseInt(%q) = %d, %v; want %d, nil", tc.in, got, err, tc.want)
		}
	}
}

func TestParseIntSignHidden(t *testing.T) {
	if got, err := ParseInt("+15"); err != nil || got != 15 {
		t.Fatalf("ParseInt(\"+15\") = %d, %v; want 15, nil", got, err)
	}
}

func TestParseListEmptyHidden(t *testing.T) {
	for _, in := range []string{"", " , , "} {
		if got := ParseList(in); got != nil {
			t.Errorf("ParseList(%q) = %#v, want nil", in, got)
		}
	}
}

func TestParseListSingleHidden(t *testing.T) {
	if got := ParseList(" solo "); !reflect.DeepEqual(got, []string{"solo"}) {
		t.Errorf("ParseList(single) = %#v, want [solo]", got)
	}
}

func TestParseListTrailingHidden(t *testing.T) {
	if got := ParseList("a,b,"); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("ParseList(trailing) = %#v, want [a b]", got)
	}
}
