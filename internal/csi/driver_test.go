package csi

import (
	"strings"
	"testing"
)

func TestDriverRunRejectsMalformedEndpoint(t *testing.T) {
	driver := NewDriver("test-node", "missing-scheme", nil)
	err := driver.Run()
	if err == nil || !strings.Contains(err.Error(), "invalid endpoint format") {
		t.Fatalf("Run() error = %v, want invalid endpoint format", err)
	}
}
