package handlers

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRatToText(t *testing.T) {
	tests := []struct {
		name     string
		value    *big.Rat
		expected string
	}{
		{name: "zero", value: big.NewRat(0, 1), expected: "0"},
		{name: "integer", value: big.NewRat(42, 1), expected: "42"},
		{name: "ordinary decimal", value: big.NewRat(1, 3), expected: "0.3333"},
		{name: "trim trailing zeroes", value: big.NewRat(6, 5), expected: "1.2"},
		{name: "rounding boundary", value: big.NewRat(1, 20000), expected: "0.0001"},
		{name: "just below rounding boundary", value: big.NewRat(9999, 200000000), expected: "5e-5"},
		{name: "small positive", value: big.NewRat(2469, 200000000), expected: "1.235e-5"},
		{name: "small negative", value: big.NewRat(-2469, 200000000), expected: "-1.235e-5"},
		{name: "trim scientific zeroes", value: big.NewRat(1, 100000), expected: "1e-5"},
		{name: "mantissa rounding carry", value: big.NewRat(19999, 2000000000), expected: "1e-5"},
		{name: "very small rational", value: big.NewRat(1, 1_000_000_000_000_000_000), expected: "1e-18"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, ratToText(tt.value))
		})
	}
}
