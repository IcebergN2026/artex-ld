package server

import "testing"

func TestParseBoolTriState(t *testing.T) {
	cases := []struct {
		in   string
		want *bool
	}{
		{"true", new(true)},
		{"TRUE", new(true)},
		{" true ", new(true)},
		{"false", new(false)},
		{"False", new(false)},
		{"", nil},
		{"yes", nil},
		{"0", nil},
	}
	for _, c := range cases {
		got := parseBoolTriState(c.in)
		if (got == nil) != (c.want == nil) {
			t.Fatalf("parseBoolTriState(%q) = %v, want %v", c.in, got, c.want)
		}
		if got != nil && *got != *c.want {
			t.Fatalf("parseBoolTriState(%q) = %v, want %v", c.in, *got, *c.want)
		}
	}
}
