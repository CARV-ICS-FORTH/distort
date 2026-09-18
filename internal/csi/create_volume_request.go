package csi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

type canonicalCreateVolumeRequest struct {
	RequiredBytes int64                     `json:"requiredBytes"`
	LimitBytes    int64                     `json:"limitBytes"`
	TargetBackend string                    `json:"targetBackend"`
	VolumeManager string                    `json:"volumeManager"`
	TargetOptions map[string]string         `json:"targetOptions"`
	Capability    canonicalVolumeCapability `json:"capability"`
}

func fingerprintCreateVolumeRequest(request canonicalCreateVolumeRequest) (string, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("encode canonical CreateVolume request: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
