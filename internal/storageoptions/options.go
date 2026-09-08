// Package storageoptions validates backend-specific NVMePartition options.
package storageoptions

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

const (
	// SPDKCoreMaskOption is the StorageClass/NVMePartition option used to select
	// the SPDK reactor cores.
	SPDKCoreMaskOption = "spdk-core-mask"
	BXINIDOption       = "bxi-nid"
	PortOption         = "port"
	RDMAPortOption     = "rdma-port"
	PortalsPIDOption   = "portals-pid"

	// MaxSPDKCoreMaskLength allows a mask for up to 1024 logical CPUs while
	// placing a firm bound on API input passed to the SPDK process.
	MaxSPDKCoreMaskLength = 258
)

var spdkCoreMaskPattern = regexp.MustCompile(`^0x[0-9A-Fa-f]+$`)
var bxiNIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:%-]{0,254}$`)

var bxiPortOptions = []string{PortOption, RDMAPortOption, PortalsPIDOption}

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
		case "bxi":
			switch name {
			case SPDKCoreMaskOption:
				if err := ValidateSPDKCoreMask(value); err != nil {
					return err
				}
			case BXINIDOption:
				if !bxiNIDPattern.MatchString(value) || net.ParseIP(value) == nil {
					return fmt.Errorf("%s must be a valid IP address, got %q", BXINIDOption, value)
				}
			case PortOption, RDMAPortOption, PortalsPIDOption:
				if _, err := validateBXIPort(name, value); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unsupported BXI backend option %q", name)
			}
		default:
			return fmt.Errorf("unsupported target backend %q", targetBackend)
		}
	}
	if targetBackend == "bxi" {
		seenPortOption := ""
		for _, name := range bxiPortOptions {
			if _, exists := options[name]; !exists {
				continue
			}
			if seenPortOption != "" {
				return fmt.Errorf("BXI port options %q and %q are aliases; set only one", seenPortOption, name)
			}
			seenPortOption = name
		}
	}

	return nil
}

func validateBXIPort(name, value string) (int, error) {
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("%s must be an integer from 1 through 65535, got %q", name, value)
	}
	return port, nil
}

// BXIPort returns the configured Portals PID/transport service ID. The aliases
// are retained for compatibility with existing BXI StorageClasses.
func BXIPort(options map[string]string) (int, error) {
	if err := Validate("bxi", options); err != nil {
		return 0, err
	}
	for _, name := range bxiPortOptions {
		if value, exists := options[name]; exists {
			return validateBXIPort(name, value)
		}
	}
	return 11, nil
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
