package csi

import (
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
