package attachment

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"k8s.io/apimachinery/pkg/types"
)

const (
	Finalizer             = "storage.distort.io/attachment-fencing"
	ForceDetachAnnotation = "storage.distort.io/force-detach-node"
	AccessReadyCondition  = "AccessReady"
)

func Name(volumeUID types.UID) string {
	return "nvme-attach-" + strings.ToLower(string(volumeUID))
}

func HostNQN(nodeID string) string {
	digest := sha256.Sum256([]byte(nodeID))
	return "nqn.2026-01.io.distort:host-" + hex.EncodeToString(digest[:16])
}
