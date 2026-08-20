package csi

import "distort/internal/attachment"

func hostNQNForNode(nodeID string) string {
	return attachment.HostNQN(nodeID)
}
