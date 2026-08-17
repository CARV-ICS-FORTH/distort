package volumeidentity

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/types"
)

const (
	handlePrefix = "distort-v1"
	nqnPrefix    = "nqn.2026-02.io.distort:volume-"
)

// Identity contains the stable identifiers derived from one Kubernetes object.
type Identity struct {
	ExternalID   string
	VolumeHandle string
}

// Reference identifies the exact Kubernetes object encoded in a CSI volume handle.
type Reference struct {
	Namespace string
	Name      string
	UID       types.UID
}

// New derives a backend-safe ID and a reversible CSI handle from an object's UID.
func New(namespace, name string, uid types.UID) (Identity, error) {
	if namespace == "" || name == "" || uid == "" {
		return Identity{}, fmt.Errorf("namespace, name, and UID are required for volume identity")
	}
	digest := sha256.Sum256([]byte(uid))
	externalID := "vol-" + hex.EncodeToString(digest[:16])
	encode := base64.RawURLEncoding.EncodeToString
	return Identity{
		ExternalID: externalID,
		VolumeHandle: strings.Join([]string{
			handlePrefix,
			encode([]byte(namespace)),
			encode([]byte(name)),
			encode([]byte(uid)),
		}, "."),
	}, nil
}

// ParseVolumeHandle decodes a CSI handle into an exact namespaced object reference.
func ParseVolumeHandle(handle string) (Reference, error) {
	parts := strings.Split(handle, ".")
	if len(parts) != 4 || parts[0] != handlePrefix {
		return Reference{}, fmt.Errorf("unsupported volume handle format")
	}
	decode := func(field, encoded string) (string, error) {
		value, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil || len(value) == 0 {
			return "", fmt.Errorf("invalid %s in volume handle", field)
		}
		return string(value), nil
	}
	namespace, err := decode("namespace", parts[1])
	if err != nil {
		return Reference{}, err
	}
	name, err := decode("name", parts[2])
	if err != nil {
		return Reference{}, err
	}
	uid, err := decode("UID", parts[3])
	if err != nil {
		return Reference{}, err
	}
	return Reference{Namespace: namespace, Name: name, UID: types.UID(uid)}, nil
}

// NQN returns the deterministic NVMe Qualified Name for a backend identity.
func NQN(externalID string) string {
	return nqnPrefix + externalID
}

// ExternalIDFromNQN recovers the identifier used by legacy persisted exports.
func ExternalIDFromNQN(nqn string) (string, bool) {
	externalID, found := strings.CutPrefix(nqn, nqnPrefix)
	return externalID, found && externalID != ""
}
