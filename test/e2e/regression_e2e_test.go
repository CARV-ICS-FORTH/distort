//go:build e2e

package e2e

import (
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Claim authorization", func() {
	It("rejects client-supplied placement without an owning claim", Label("green", "F1", "admission"), func() {
		serial := serialForNode("distort-worker-2")
		name := "regression-f1-unclaimed"
		DeferCleanup(func() {
			_, _ = kubectl("delete", "nvmepartition", name, "--ignore-not-found", "--wait=false")
		})

		manifest := fmt.Sprintf(`
apiVersion: storage.distort.io/v1alpha1
kind: NVMePartition
metadata:
  name: %s
spec:
  size: 64Mi
  nodeName: distort-worker-2
  parentDeviceSerialNumber: %s
  targetBackend: kernel
  volumeManager: partition
`, name, serial)
		_, err := applyManifest(manifest)
		Expect(err).To(HaveOccurred(), "the API admitted scheduler-owned placement without a claim")
	})
})

var _ = Describe("Global volume identity", Label("green", "F4"), func() {
	It("keeps same-named partitions distinct and deletes only the requested object", func() {
		const claimName = "regression-f4-claim"
		serial := serialForNode("distort-master")
		namespaces := []string{"distort-f4-a", "distort-f4-b"}
		for _, namespace := range namespaces {
			_, err := applyManifest(fmt.Sprintf("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n", namespace))
			Expect(err).NotTo(HaveOccurred())
		}
		DeferCleanup(func() {
			for _, namespace := range namespaces {
				_, _ = kubectl("delete", "namespace", namespace, "--ignore-not-found", "--wait=false")
			}
			_, _ = kubectl("delete", "nvmedeviceclaim", claimName, "--ignore-not-found", "--wait=false")
		})

		_, err := applyManifest(fmt.Sprintf(`
apiVersion: storage.distort.io/v1alpha1
kind: NVMeDeviceClaim
metadata:
  name: %s
spec:
  serialNumber: %s
`, claimName, serial))
		Expect(err).NotTo(HaveOccurred())
		Eventually(func(g Gomega) {
			out, getErr := kubectl("get", "nvmedeviceclaim", claimName, "-o", "jsonpath={.status.active}")
			g.Expect(getErr).NotTo(HaveOccurred())
			g.Expect(out).To(Equal("true"))
		}, 90*time.Second, 2*time.Second).Should(Succeed())
		agentPod, err := kubectl(
			"get", "pod", "-n", "distort-system",
			"-l", "app.kubernetes.io/component=agent",
			"--field-selector", "spec.nodeName=distort-master",
			"-o", "jsonpath={.items[0].metadata.name}",
		)
		Expect(err).NotTo(HaveOccurred())
		agentPod = strings.TrimSpace(agentPod)

		for cycle, deletionOrder := range [][]string{{namespaces[0], namespaces[1]}, {namespaces[1], namespaces[0]}} {
			partitionName := fmt.Sprintf("same-name-%d", cycle)
			for _, namespace := range namespaces {
				_, err := applyManifest(fmt.Sprintf(`
apiVersion: storage.distort.io/v1alpha1
kind: NVMePartition
metadata:
  namespace: %s
  name: %s
spec:
  size: 64Mi
  targetBackend: spdk
  volumeManager: partition
`, namespace, partitionName))
				Expect(err).NotTo(HaveOccurred())
			}

			for _, namespace := range namespaces {
				Eventually(func(g Gomega) {
					out, getErr := kubectl("get", "nvmepartition", "-n", namespace, partitionName, "-o", "jsonpath={.status.state}")
					g.Expect(getErr).NotTo(HaveOccurred())
					g.Expect(out).To(Equal("Exported"))
				}, 180*time.Second, 5*time.Second).Should(Succeed())
			}

			identities := make(map[string]string, len(namespaces))
			nqns := make(map[string]string, len(namespaces))
			backendVolumes := make(map[string]string, len(namespaces))
			for _, namespace := range namespaces {
				identities[namespace], err = kubectl("get", "nvmepartition", "-n", namespace, partitionName, "-o", "jsonpath={.status.volumeID}")
				Expect(err).NotTo(HaveOccurred())
				nqns[namespace], err = kubectl("get", "nvmepartition", "-n", namespace, partitionName, "-o", "jsonpath={.status.nqn}")
				Expect(err).NotTo(HaveOccurred())
				backendVolumes[namespace], err = kubectl("get", "nvmepartition", "-n", namespace, partitionName, "-o", "jsonpath={.status.backendVolumeID}")
				Expect(err).NotTo(HaveOccurred())
			}
			Expect(identities[namespaces[0]]).NotTo(Equal(identities[namespaces[1]]))
			Expect(nqns[namespaces[0]]).NotTo(Equal(nqns[namespaces[1]]))
			Expect(backendVolumes[namespaces[0]]).NotTo(Equal(backendVolumes[namespaces[1]]))

			for index, namespace := range deletionOrder {
				_, err := kubectl("delete", "nvmepartition", "-n", namespace, partitionName, "--wait=true", "--timeout=120s")
				Expect(err).NotTo(HaveOccurred())
				if index == 0 {
					other := deletionOrder[1]
					out, getErr := kubectl("get", "nvmepartition", "-n", other, partitionName, "-o", "jsonpath={.status.state}")
					Expect(getErr).NotTo(HaveOccurred())
					Expect(out).To(Equal("Exported"))
					out, getErr = kubectl(
						"exec", "-n", "distort-system", agentPod, "--",
						"/opt/spdk/scripts/rpc.py", "nvmf_get_subsystems",
					)
					Expect(getErr).NotTo(HaveOccurred())
					Expect(out).To(ContainSubstring(nqns[other]))
				}
			}
		}
	})
})

var _ = Describe("Review finding API and authorization regressions", Label("known-failure"), func() {
	It("rejects zero and negative partition sizes at the API boundary", Label("F7", "admission"), func() {
		requireKnownE2E("F7")
		for _, size := range []string{"0", "-1Mi"} {
			name := "regression-f7-" + strings.NewReplacer("-", "negative-", "Mi", "mi").Replace(size)
			DeferCleanup(func() {
				_, _ = kubectl("delete", "nvmepartition", name, "--ignore-not-found", "--wait=false")
			})
			_, err := applyManifest(fmt.Sprintf(`
apiVersion: storage.distort.io/v1alpha1
kind: NVMePartition
metadata:
  name: %s
spec:
  size: %s
`, name, size))
			Expect(err).To(HaveOccurred(), "the API admitted size %s", size)
		}
	})

	It("prevents the shared workload identity from mutating Nodes", Label("F18", "rbac"), func() {
		requireKnownE2E("F18")
		out, err := kubectl(
			"auth", "can-i", "update", "nodes",
			"--as=system:serviceaccount:distort-system:distort",
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(out)).To(Equal("no"))
	})

	It("rejects the unimplemented lvm manager at admission", Label("F20", "admission"), func() {
		requireKnownE2E("F20")
		name := "regression-f20-lvm"
		DeferCleanup(func() {
			_, _ = kubectl("delete", "nvmepartition", name, "--ignore-not-found", "--wait=false")
		})
		_, err := applyManifest(fmt.Sprintf(`
apiVersion: storage.distort.io/v1alpha1
kind: NVMePartition
metadata:
  name: %s
spec:
  size: 64Mi
  volumeManager: lvm
`, name))
		Expect(err).To(HaveOccurred(), "the API advertised an unregistered volume manager")
	})

	It("automatically restores an exported SPDK target after nvmf_tgt crashes", Label("F17", "recovery"), func() {
		requireKnownE2E("F17")
		serial := serialForNode("distort-worker-1")
		_, err := applyManifest(fmt.Sprintf(`
apiVersion: storage.distort.io/v1alpha1
kind: NVMeDeviceClaim
metadata:
  name: regression-f17-claim
spec:
  serialNumber: %s
---
apiVersion: storage.distort.io/v1alpha1
kind: NVMePartition
metadata:
  name: regression-f17-volume
spec:
  size: 64Mi
  targetBackend: spdk
  volumeManager: partition
`, serial))
		Expect(err).NotTo(HaveOccurred())

		agentPod, err := kubectl(
			"get", "pod", "-n", "distort-system",
			"-l", "app.kubernetes.io/component=agent",
			"--field-selector", "spec.nodeName=distort-worker-1",
			"-o", "jsonpath={.items[0].metadata.name}",
		)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			_, _ = kubectl("delete", "pod", "-n", "distort-system", strings.TrimSpace(agentPod), "--ignore-not-found", "--wait=true")
			_, _ = kubectl("rollout", "status", "-n", "distort-system", "daemonset/distort-agent", "--timeout=180s")
			_, _ = kubectl("delete", "nvmepartition", "regression-f17-volume", "--ignore-not-found", "--wait=false")
			_, _ = kubectl("delete", "nvmedeviceclaim", "regression-f17-claim", "--ignore-not-found", "--wait=false")
		})

		Eventually(func(g Gomega) {
			out, getErr := kubectl("get", "nvmepartition", "regression-f17-volume", "-o", "jsonpath={.status.state}")
			g.Expect(getErr).NotTo(HaveOccurred())
			g.Expect(out).To(Equal("Exported"))
		}, 180*time.Second, 5*time.Second).Should(Succeed())
		expectedNQN, err := kubectl("get", "nvmepartition", "regression-f17-volume", "-o", "jsonpath={.status.nqn}")
		Expect(err).NotTo(HaveOccurred())
		Expect(expectedNQN).NotTo(BeEmpty())

		_, err = kubectl("exec", "-n", "distort-system", strings.TrimSpace(agentPod), "--", "pkill", "-9", "nvmf_tgt")
		Expect(err).NotTo(HaveOccurred())

		Eventually(func(g Gomega) {
			out, rpcErr := kubectl(
				"exec", "-n", "distort-system", strings.TrimSpace(agentPod), "--",
				"/opt/spdk/scripts/rpc.py", "nvmf_get_subsystems",
			)
			g.Expect(rpcErr).NotTo(HaveOccurred())
			g.Expect(out).To(ContainSubstring(expectedNQN))
		}, 90*time.Second, 5*time.Second).Should(Succeed())
	})
})
