package csi

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type mountRecord struct {
	majorMinor string
	root       string
	target     string
	filesystem string
	source     string
	options    map[string]struct{}
}

var (
	readMountInfo = func() ([]byte, error) { return os.ReadFile("/proc/self/mountinfo") }
	statMountPath = os.Stat
	sameMountFile = os.SameFile
	deviceNumber  = func(path string) (string, error) {
		var stat unix.Stat_t
		if err := unix.Stat(path, &stat); err != nil {
			return "", err
		}
		return fmt.Sprintf("%d:%d", unix.Major(stat.Rdev), unix.Minor(stat.Rdev)), nil
	}
)

func unescapeMountInfoField(value string) string {
	replacer := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return replacer.Replace(value)
}

func parseMountInfo(data []byte) ([]mountRecord, error) {
	records := make([]mountRecord, 0)
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		separator := -1
		for index, field := range fields {
			if field == "-" {
				separator = index
				break
			}
		}
		if len(fields) < 10 || separator < 6 || separator+3 >= len(fields) {
			return nil, fmt.Errorf("invalid mountinfo row %q", line)
		}
		options := make(map[string]struct{})
		for _, optionList := range []string{fields[5], fields[separator+3]} {
			for option := range strings.SplitSeq(optionList, ",") {
				options[option] = struct{}{}
			}
		}
		records = append(records, mountRecord{
			majorMinor: fields[2],
			root:       filepath.Clean(unescapeMountInfoField(fields[3])),
			target:     filepath.Clean(unescapeMountInfoField(fields[4])),
			filesystem: fields[separator+1],
			source:     unescapeMountInfoField(fields[separator+2]),
			options:    options,
		})
	}
	return records, nil
}

func mountAt(target string) (*mountRecord, error) {
	data, err := readMountInfo()
	if err != nil {
		return nil, fmt.Errorf("read mountinfo: %w", err)
	}
	records, err := parseMountInfo(data)
	if err != nil {
		return nil, err
	}
	target = filepath.Clean(target)
	for index := len(records) - 1; index >= 0; index-- {
		if records[index].target == target {
			return &records[index], nil
		}
	}
	return nil, nil
}

func optionEnabled(record *mountRecord, option string) bool {
	_, enabled := record.options[option]
	return enabled
}

func verifyStagingMount(source, target, filesystem string) (bool, error) {
	record, err := mountAt(target)
	if err != nil || record == nil {
		return record != nil, err
	}
	expectedDevice, err := deviceNumber(source)
	if err != nil {
		return true, fmt.Errorf("inspect expected block device %s: %w", source, err)
	}
	if record.majorMinor != expectedDevice {
		return true, fmt.Errorf("target %s is mounted from device %s (%s), expected %s (%s)",
			target, record.source, record.majorMinor, source, expectedDevice)
	}
	if normalizeFilesystem(record.filesystem) != filesystem {
		return true, fmt.Errorf("target %s uses filesystem %s, expected %s", target, record.filesystem, filesystem)
	}
	if optionEnabled(record, "ro") {
		return true, fmt.Errorf("staging target %s is read-only", target)
	}
	return true, nil
}

func verifyPublishedMount(source, target string, readOnly bool) (bool, error) {
	record, err := mountAt(target)
	if err != nil || record == nil {
		return record != nil, err
	}
	sourceInfo, err := statMountPath(source)
	if err != nil {
		return true, fmt.Errorf("inspect bind source %s: %w", source, err)
	}
	targetInfo, err := statMountPath(target)
	if err != nil {
		return true, fmt.Errorf("inspect bind target %s: %w", target, err)
	}
	if !sameMountFile(sourceInfo, targetInfo) {
		return true, fmt.Errorf("target %s is not a bind mount of %s (mount root %s, source %s)", target, source, record.root, record.source)
	}
	if readOnly != optionEnabled(record, "ro") {
		return true, fmt.Errorf("target %s read-only state does not match requested readOnly=%t", target, readOnly)
	}
	return true, nil
}
