package model

import "testing"

func TestNormalizeManualPaymentReference(t *testing.T) {
	tests := []struct {
		name            string
		input           string
		caseInsensitive bool
		want            string
	}{
		{name: "preserve case", input: "  TxAbC  ", want: "TxAbC"},
		{name: "normalize case", input: "  0xAbC  ", caseInsensitive: true, want: "0xabc"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := NormalizeManualPaymentReference(test.input, test.caseInsensitive); got != test.want {
				t.Fatalf("NormalizeManualPaymentReference() = %q, want %q", got, test.want)
			}
		})
	}
}
