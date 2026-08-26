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
