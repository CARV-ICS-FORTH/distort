// Package knownfailure provides an explicit quarantine for regression tests
// that describe confirmed defects which have not been fixed yet.
package knownfailure

import (
	"os"
	"strings"
	"testing"
)

const runEnvironment = "DISTORT_RUN_KNOWN_FAILURES"
const findingEnvironment = "DISTORT_FINDING"

// Require skips a known-failure test during the normal green test run. Setting
// DISTORT_RUN_KNOWN_FAILURES=1 enables it. DISTORT_FINDING may contain a
// comma-separated allow-list such as "F7,F9".
func Require(t *testing.T, finding string) {
	t.Helper()
	if os.Getenv(runEnvironment) != "1" {
		t.Skipf("%s is a known defect; run with %s=1", finding, runEnvironment)
	}

	filter := strings.TrimSpace(os.Getenv(findingEnvironment))
	if filter == "" {
		return
	}
	for value := range strings.SplitSeq(filter, ",") {
		if strings.EqualFold(strings.TrimSpace(value), finding) {
			return
		}
	}
	t.Skipf("%s is not selected by %s=%q", finding, findingEnvironment, filter)
}
