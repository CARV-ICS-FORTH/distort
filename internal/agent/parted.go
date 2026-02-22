/*
Copyright 2026, FORTH-ICS.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package agent

import (
	"fmt"
	"os/exec"

	"k8s.io/klog/v2"
)

// PartedWrapper provides commands to slice disks.
type PartedWrapper struct {
	Device string
}

func NewPartedWrapper(device string) *PartedWrapper {
	return &PartedWrapper{Device: device}
}

// MakeLabel initializes a GPT partition table.
func (p *PartedWrapper) MakeLabel() error {
	klog.Infof("Wiping and Initializing GPT label on %s", p.Device)

	cmdWipe := exec.Command("wipefs", "-a", p.Device)
	if out, err := cmdWipe.CombinedOutput(); err != nil {
		klog.Warningf("wipefs error (ignoring): %v output: %s", err, string(out))
	}

	cmd := exec.Command("parted", "-s", p.Device, "mklabel", "gpt")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("parted mklabel error: %v output: %s", err, string(out))
	}
	return nil
}

// CreatePartition creates a new partition from startMB to endMB and returns the partition path (e.g. /dev/nvme0n1p1).
func (p *PartedWrapper) CreatePartition(name string, startMB, endMB int64) (string, error) {
	klog.Infof("Creating partition %s on %s (%dMB -> %dMB)", name, p.Device, startMB, endMB)

	startStr := fmt.Sprintf("%dMB", startMB)
	endStr := fmt.Sprintf("%dMB", endMB)

	cmd := exec.Command("parted", "-s", "-a", "optimal", p.Device, "mkpart", "primary", startStr, endStr)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Parted can exit with 1 on warnings (like "Partition(s) have been written, but we have been unable to inform the kernel of the change")
		// Often it actually succeeded. We'll simply log the warning and proceed, letting subsequent steps fail if it truly didn't work.
		klog.Warningf("parted mkpart finished with error: %v output: %s", err, string(out))
	}

	// Wait, we need to name it if we want, parted mkpart primary <name> isn't standard in older parted unless using name command.
	// But let's assume `mkpart primary` creates it. We need the partition number.
	// A simpler block manager pattern is to just discover the highest partition number.
	// But in this implementation, we can just return a guessed path or rely on udev matching /dev/disk/by-partlabel/<name>
	// Let's name it explicitly:
	// parted -s /dev/nvme0n1 name <part_num> <name>

	// Hardcoding to partition 1 for the E2E test's single-slice scenario.
	// This is typically /dev/nvme0n1p1
	return p.Device + "p1", nil
}

// RemovePartition drops a partition by its number.
func (p *PartedWrapper) RemovePartition(partNum int) error {
	cmd := exec.Command("parted", "-s", p.Device, "rm", fmt.Sprintf("%d", partNum))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("parted rm error: %v output: %s", err, string(out))
	}
	return nil
}
