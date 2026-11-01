package parser

import (
	"reflect"
	"testing"
)

func TestParseIntTrims(t *testing.T) {
	got, err := ParseInt(" 42 ")
	if err != nil || got != 42 {
		t.Fatalf("ParseInt(\" 42 \") = %d, %v; want 42, nil", got, err)
	}
}

func TestParseListTrims(t *testing.T) {
	got := ParseList("a, b ,,c")
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseList() = %#v, want %#v", got, want)
	}
}
