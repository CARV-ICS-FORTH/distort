package csi

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDriverRunRejectsMalformedEndpoint(t *testing.T) {
	driver := NewDriver("test-node", "missing-scheme", nil)
	err := driver.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "invalid endpoint format") {
		t.Fatalf("Run() error = %v, want invalid endpoint format", err)
	}
}

func TestDriverRunStopsWhenContextIsCancelled(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "csi.sock")
	driver := NewDriver("test-node", "unix://"+socketPath, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- driver.Run(ctx)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		info, err := os.Lstat(socketPath)
		if err == nil && info.Mode()&os.ModeSocket != 0 {
			break
		}
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("inspect CSI socket: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("CSI socket was not created before timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() returned an error during graceful shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not stop after context cancellation")
	}
}

func TestDefaultEndpointIsAbsolute(t *testing.T) {
	if DefaultEndpoint != "unix:///tmp/csi.sock" {
		t.Fatalf("DefaultEndpoint = %q, want unix:///tmp/csi.sock", DefaultEndpoint)
	}
}

func TestListenEndpointCreatesUnixParentAndReplacesStaleSocket(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "nested", "csi.sock")
	endpoint := "unix://" + socketPath
	listener, err := listenEndpoint(endpoint)
	if err != nil {
		t.Fatalf("first listenEndpoint: %v", err)
	}
	info, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatalf("socket path was not created: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket path mode = %v, want socket", info.Mode())
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatalf("create stale socket: %v", err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}

	replacement, err := listenEndpoint(endpoint)
	if err != nil {
		t.Fatalf("replace stale socket: %v", err)
	}
	if err := replacement.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestListenEndpointRejectsUnsafeUnixPaths(t *testing.T) {
	if _, err := listenEndpoint("unix://relative/csi.sock"); err == nil || !strings.Contains(err.Error(), "must be absolute") {
		t.Fatalf("relative unix endpoint error = %v, want absolute-path rejection", err)
	}

	regularPath := filepath.Join(t.TempDir(), "csi.sock")
	if err := os.WriteFile(regularPath, []byte("do not remove"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := listenEndpoint("unix://" + regularPath); err == nil || !strings.Contains(err.Error(), "non-socket") {
		t.Fatalf("regular-file endpoint error = %v, want non-socket rejection", err)
	}
	contents, err := os.ReadFile(regularPath)
	if err != nil || string(contents) != "do not remove" {
		t.Fatalf("regular file changed: contents=%q err=%v", contents, err)
	}
}
