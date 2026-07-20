package report

import "testing"

func TestFormatUSD(t *testing.T) {
	zero := 0.0
	fraction := 0.005
	whole := 2.0

	tests := []struct {
		name  string
		value *float64
		want  string
	}{
		{name: "unavailable", value: nil, want: "不可用"},
		{name: "zero", value: &zero, want: "$0.00"},
		{name: "fraction", value: &fraction, want: "$0.005"},
		{name: "whole", value: &whole, want: "$2.00"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := FormatUSD(test.value); got != test.want {
				t.Fatalf("FormatUSD()=%q want %q", got, test.want)
			}
		})
	}
}
