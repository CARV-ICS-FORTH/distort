package volumeidentity

import (
	"testing"

	"k8s.io/apimachinery/pkg/types"
)

func TestIdentityIsStableUniqueAndReversible(t *testing.T) {
	first, err := New("team-a", "same-name", types.UID("11111111-1111-1111-1111-111111111111"))
	if err != nil {
		t.Fatal(err)
	}
	retry, err := New("team-a", "same-name", types.UID("11111111-1111-1111-1111-111111111111"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := New("team-b", "same-name", types.UID("22222222-2222-2222-2222-222222222222"))
	if err != nil {
		t.Fatal(err)
	}
	if first != retry {
		t.Fatalf("identity changed on retry: %#v != %#v", first, retry)
	}
	if first.ExternalID == second.ExternalID || first.VolumeHandle == second.VolumeHandle {
		t.Fatalf("different objects received colliding identities: %#v and %#v", first, second)
	}
	reference, err := ParseVolumeHandle(first.VolumeHandle)
	if err != nil {
		t.Fatal(err)
	}
	if reference.Namespace != "team-a" || reference.Name != "same-name" || reference.UID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("decoded reference = %#v", reference)
	}
}

func TestParseVolumeHandleRejectsLegacyAndMalformedIDs(t *testing.T) {
	for _, handle := range []string{"same-name", "distort-v1.bad", "distort-v1..."} {
		if _, err := ParseVolumeHandle(handle); err == nil {
			t.Fatalf("ParseVolumeHandle(%q) unexpectedly succeeded", handle)
		}
	}
}
