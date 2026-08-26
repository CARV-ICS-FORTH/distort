// Package storageoptions validates backend-specific NVMePartition options.
package storageoptions

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	// SPDKCoreMaskOption is the StorageClass/NVMePartition option used to select
	// the SPDK reactor cores.
	SPDKCoreMaskOption = "spdk-core-mask"

	// MaxSPDKCoreMaskLength allows a mask for up to 1024 logical CPUs while
	// placing a firm bound on API input passed to the SPDK process.
	MaxSPDKCoreMaskLength = 258
)

var spdkCoreMaskPattern = regexp.MustCompile(`^0x[0-9A-Fa-f]+$`)

// Validate rejects malformed or unsupported backend options.
func Validate(targetBackend string, options map[string]string) error {
	if targetBackend == "" {
		targetBackend = "spdk"
	}

	for name, value := range options {
		switch targetBackend {
		case "spdk":
			if name != SPDKCoreMaskOption {
				return fmt.Errorf("unsupported SPDK backend option %q", name)
			}
			if err := ValidateSPDKCoreMask(value); err != nil {
				return err
			}
		case "kernel":
			return fmt.Errorf("unsupported kernel backend option %q", name)
		default:
			return fmt.Errorf("unsupported target backend %q", targetBackend)
		}
	}

	return nil
}

// ValidateSPDKCoreMask accepts only a bounded hexadecimal CPU bit mask.
func ValidateSPDKCoreMask(coreMask string) error {
	if len(coreMask) > MaxSPDKCoreMaskLength {
		return fmt.Errorf("%s exceeds the maximum length of %d characters", SPDKCoreMaskOption, MaxSPDKCoreMaskLength)
	}
	if !spdkCoreMaskPattern.MatchString(coreMask) {
		return fmt.Errorf("%s must match 0x followed by hexadecimal digits, got %q", SPDKCoreMaskOption, coreMask)
	}
	if strings.Trim(coreMask[2:], "0") == "" {
		return fmt.Errorf("%s must select at least one CPU, got %q", SPDKCoreMaskOption, coreMask)
	}
	return nil
}
