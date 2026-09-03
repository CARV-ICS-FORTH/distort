package csi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeviceByNQN(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		nqn     string
		want    string
		wantErr string
	}{
		{
			name: "selects live path from matching subsystem",
			data: `[{"Subsystems":[
                {"NQN":"nqn.other","Paths":[{"Name":"nvme1","State":"live"}]},
                {"NQN":"nqn.test","Paths":[{"Name":"nvme2","State":"connecting"},{"Name":"nvme3","State":"live"}]}
            ]}]`,
			nqn:  "nqn.test",
			want: "/dev/nvme3n1",
		},
		{name: "rejects malformed JSON", data: `{`, nqn: "nqn.test", wantErr: "failed to parse"},
		{name: "rejects missing subsystem", data: `[{"Subsystems":[]}]`, nqn: "nqn.test", wantErr: "not found"},
		{
			name:    "rejects subsystem without live named path",
			data:    `[{"Subsystems":[{"NQN":"nqn.test","Paths":[{"Name":"","State":"live"},{"Name":"nvme2","State":"connecting"}]}]}]`,
			nqn:     "nqn.test",
			wantErr: "not found",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := deviceByNQN([]byte(test.data), test.nqn)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("deviceByNQN() error = %v, want substring %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("deviceByNQN() returned error: %v", err)
			}
			if got != test.want {
				t.Fatalf("deviceByNQN() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestConnectRDMAReusesExistingLiveConnection(t *testing.T) {
	fakeBin := t.TempDir()
	commandLog := filepath.Join(t.TempDir(), "nvme.log")
	script := `#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >> "$NVME_LOG"
if [ "$1" = "list-subsys" ]; then
  printf '%s\n' '[{"Subsystems":[{"NQN":"nqn.test","Paths":[{"Name":"nvme7","Transport":"rdma","State":"live"}]}]}]'
  exit 0
fi
printf '%s\n' 'duplicate connect must not run' >&2
exit 1
`
	if err := os.WriteFile(filepath.Join(fakeBin, "nvme"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("NVME_LOG", commandLog)

	created, err := ConnectRDMA(context.Background(), "nqn.test", "192.0.2.10", "4420", "nqn.test:host")
	if err != nil {
		t.Fatalf("ConnectRDMA returned an error for an existing connection: %v", err)
	}
	if created {
		t.Fatal("ConnectRDMA reported creating an existing connection")
	}
	calls, err := os.ReadFile(commandLog)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(calls)); got != "list-subsys -o json" {
		t.Fatalf("nvme calls = %q, want only the observation command", got)
	}
}

func TestConnectRDMATreatsLivePostFailureStateAsConcurrentSuccess(t *testing.T) {
	fakeBin := t.TempDir()
	stateFile := filepath.Join(t.TempDir(), "connected")
	script := `#!/usr/bin/env bash
set -eu
if [ "$1" = "list-subsys" ]; then
  if [ -f "$NVME_STATE" ]; then
    printf '%s\n' '[{"Subsystems":[{"NQN":"nqn.test","Paths":[{"Name":"nvme8","Transport":"rdma","State":"live"}]}]}]'
  else
    printf '%s\n' '[{"Subsystems":[]}]'
  fi
  exit 0
fi
if [ "$1" = "connect" ]; then
  : > "$NVME_STATE"
  printf '%s\n' 'Failed to write to /dev/nvme-fabrics: Invalid argument' >&2
  printf '%s\n' 'could not add new controller: invalid arguments/configuration' >&2
  exit 1
fi
exit 1
`
	if err := os.WriteFile(filepath.Join(fakeBin, "nvme"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("NVME_STATE", stateFile)

	created, err := ConnectRDMA(context.Background(), "nqn.test", "192.0.2.10", "4420", "nqn.test:host")
	if err != nil {
		t.Fatalf("ConnectRDMA rejected the resulting live connection: %v", err)
	}
	if created {
		t.Fatal("ConnectRDMA claimed ownership of a concurrently established connection")
	}
}
