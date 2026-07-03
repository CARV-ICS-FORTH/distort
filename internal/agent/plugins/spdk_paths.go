package plugins

import (
	"os"
	"path/filepath"
)

// defaultSPDKDir is where the Dockerfile installs the built SPDK tree
// (the whole /src/spdk checkout is copied to /spdk in the runtime stage).
const defaultSPDKDir = "/spdk"

// spdkDir returns the root of the SPDK installation. It can be overridden with
// the SPDK_DIR environment variable (useful for local runs or a relocated
// tree); otherwise it defaults to /spdk to match the container image layout.
//
// NOTE: scripts/setup.sh and rpc.py resolve their own paths relative to this
// root, so it must point at the full SPDK checkout, not a partial copy.
func spdkDir() string {
	if d := os.Getenv("SPDK_DIR"); d != "" {
		return d
	}
	return defaultSPDKDir
}

// spdkRPCScript is the path to scripts/rpc.py (the SPDK JSON-RPC client).
func spdkRPCScript() string { return filepath.Join(spdkDir(), "scripts", "rpc.py") }

// spdkSetupScript is the path to scripts/setup.sh (binds/unbinds PCI devices).
func spdkSetupScript() string { return filepath.Join(spdkDir(), "scripts", "setup.sh") }

// spdkNvmfTgt is the path to the built nvmf_tgt target daemon binary.
func spdkNvmfTgt() string { return filepath.Join(spdkDir(), "build", "bin", "nvmf_tgt") }
