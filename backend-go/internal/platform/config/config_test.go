package config

import (
	"reflect"
	"testing"
)

// TestParseWeights requires a strictly positive share for every priority
// class: interactive must always lead and scheduled/retry must never starve
// (weight 0 would silently disable a class), so every invalid shape falls
// back to the safe 7/1/2 default.
func TestParseWeights(t *testing.T) {
	cases := []struct {
		in   string
		want []int
	}{
		{"7,1,2", []int{7, 1, 2}},
		{" 7 , 1 , 2 ", []int{7, 1, 2}},
		{"10,3,4", []int{10, 3, 4}},
		{"1,1,1", []int{1, 1, 1}},
		// Zero weights are rejected: a class must keep a non-zero share.
		{"0,1,2", []int{7, 1, 2}},
		{"7,0,2", []int{7, 1, 2}},
		{"7,1,0", []int{7, 1, 2}},
		{"0,0,0", []int{7, 1, 2}},
		// Negative / non-numeric / wrong arity are rejected too.
		{"-1,1,2", []int{7, 1, 2}},
		{"7,x,2", []int{7, 1, 2}},
		{"7,1", []int{7, 1, 2}},
		{"7,1,2,3", []int{7, 1, 2}},
		{"", []int{7, 1, 2}},
	}
	for _, c := range cases {
		if got := parseWeights(c.in); !reflect.DeepEqual(got, c.want) {
			t.Fatalf("parseWeights(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
