package storageoptions

import (
	"strings"
	"testing"
)

func TestValidateSPDKCoreMask(t *testing.T) {
	for _, mask := range []string{"0x1", "0x3", "0xabcdef", "0xABCDEF"} {
		t.Run("accepts "+mask, func(t *testing.T) {
			if err := ValidateSPDKCoreMask(mask); err != nil {
				t.Fatalf("valid mask %q was rejected: %v", mask, err)
			}
		})
	}

	for _, test := range []struct {
		name string
		mask string
	}{
		{name: "empty", mask: ""},
		{name: "zero", mask: "0x0"},
		{name: "zero padded", mask: "0x0000"},
		{name: "semicolon", mask: "0x1;id"},
		{name: "substitution", mask: "$(id)"},
		{name: "space", mask: "0x1 0x2"},
		{name: "newline", mask: "0x1\nid"},
		{name: "flag", mask: "--wait-for-rpc"},
		{name: "missing prefix", mask: "3"},
		{name: "oversized", mask: "0x" + strings.Repeat("f", MaxSPDKCoreMaskLength-1)},
	} {
		t.Run("rejects "+test.name, func(t *testing.T) {
			if err := ValidateSPDKCoreMask(test.mask); err == nil {
				t.Fatalf("invalid mask %q was accepted", test.mask)
			}
		})
	}
}

func TestValidateRejectsUnknownBackendOptions(t *testing.T) {
	for _, test := range []struct {
		backend string
		options map[string]string
	}{
		{backend: "spdk", options: map[string]string{"unknown": "value"}},
		{backend: "kernel", options: map[string]string{SPDKCoreMaskOption: "0x1"}},
	} {
		if err := Validate(test.backend, test.options); err == nil {
			t.Fatalf("backend %q accepted options %#v", test.backend, test.options)
		}
	}
}

func TestValidateBXIOptions(t *testing.T) {
	options := map[string]string{
		SPDKCoreMaskOption: "0x3",
		BXINIDOption:       "192.168.123.7",
		PortalsPIDOption:   "11",
	}
	if err := Validate("bxi", options); err != nil {
		t.Fatalf("valid BXI options were rejected: %v", err)
	}
	port, err := BXIPort(options)
	if err != nil || port != 11 {
		t.Fatalf("BXIPort = %d, %v; want 11, nil", port, err)
	}
	if port, err := BXIPort(nil); err != nil || port != 11 {
		t.Fatalf("default BXIPort = %d, %v; want 11, nil", port, err)
	}
}

func TestValidateBXIRejectsInvalidOptions(t *testing.T) {
	for _, test := range []struct {
		name    string
		options map[string]string
	}{
		{name: "unknown", options: map[string]string{"unknown": "value"}},
		{name: "invalid address", options: map[string]string{BXINIDOption: "$(id)"}},
		{name: "zero port", options: map[string]string{PortOption: "0"}},
		{name: "oversized port", options: map[string]string{RDMAPortOption: "65536"}},
		{name: "non-numeric port", options: map[string]string{PortalsPIDOption: "11;id"}},
		{name: "conflicting aliases", options: map[string]string{PortOption: "11", PortalsPIDOption: "12"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := Validate("bxi", test.options); err == nil {
				t.Fatalf("invalid BXI options were accepted: %#v", test.options)
			}
		})
	}
}
