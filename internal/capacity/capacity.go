package capacity

import (
	"fmt"
	"math"
)

const AllocationUnitBytes int64 = 1024 * 1024

// MaxAllocatableBytes is the largest positive int64 that can be represented as
// a whole allocation unit without overflowing during upward rounding.
const MaxAllocatableBytes = math.MaxInt64 / AllocationUnitBytes * AllocationUnitBytes

// RoundUp returns the smallest whole allocation unit that satisfies bytes.
func RoundUp(bytes int64) (int64, error) {
	if bytes <= 0 {
		return 0, fmt.Errorf("capacity must be positive, got %d", bytes)
	}
	if bytes > MaxAllocatableBytes {
		return 0, fmt.Errorf("capacity %d exceeds maximum allocatable size %d", bytes, MaxAllocatableBytes)
	}
	return ((bytes + AllocationUnitBytes - 1) / AllocationUnitBytes) * AllocationUnitBytes, nil
}
