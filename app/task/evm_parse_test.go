package task

import "testing"

func TestParseEVMQuantity(t *testing.T) {
	if got, err := parseEVMQuantity("0x2a"); err != nil || got != 42 {
		t.Fatalf("parseEVMQuantity() = %d, %v; want 42, nil", got, err)
	}
	if _, err := parseEVMQuantity("42"); err == nil {
		t.Fatal("parseEVMQuantity accepted a non-hex quantity")
	}
}

func TestParseEVMLogFieldsRejectMalformedValues(t *testing.T) {
	if _, err := parseEVMTopicAddress("0x01"); err == nil {
		t.Fatal("parseEVMTopicAddress accepted a short topic")
	}
	if _, err := parseEVMAmount("0x01"); err == nil {
		t.Fatal("parseEVMAmount accepted a short data field")
	}
}
