package capacity

import (
	"math"
	"testing"
)

func TestRoundUp(t *testing.T) {
	tests := []struct {
		name  string
		input int64
		want  int64
		ok    bool
	}{
		{name: "zero", input: 0},
		{name: "negative", input: -1},
		{name: "one byte", input: 1, want: AllocationUnitBytes, ok: true},
		{name: "one MiB", input: AllocationUnitBytes, want: AllocationUnitBytes, ok: true},
		{name: "one MiB plus one", input: AllocationUnitBytes + 1, want: 2 * AllocationUnitBytes, ok: true},
		{name: "maximum", input: MaxAllocatableBytes, want: MaxAllocatableBytes, ok: true},
		{name: "rounding overflow", input: MaxAllocatableBytes + 1},
		{name: "max int64", input: math.MaxInt64},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := RoundUp(test.input)
			if test.ok && (err != nil || got != test.want) {
				t.Fatalf("RoundUp(%d) = %d, %v; want %d", test.input, got, err, test.want)
			}
			if !test.ok && err == nil {
				t.Fatalf("RoundUp(%d) unexpectedly succeeded with %d", test.input, got)
			}
		})
	}
}
