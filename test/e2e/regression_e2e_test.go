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

var _ = Describe("Global volume identity", Label("green", "F4", "release-gate", "volume-isolation", "backend-cleanup"), func() {
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
		rpc := func(method string) string {
			out, rpcErr := kubectl(
				"exec", "-n", "distort-system", agentPod, "--",
				"/opt/spdk/scripts/rpc.py", method,
			)
			Expect(rpcErr).NotTo(HaveOccurred())
			return out
		}

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
			for _, namespace := range namespaces {
				Expect(rpc("nvmf_get_subsystems")).To(ContainSubstring(nqns[namespace]))
				Expect(rpc("bdev_get_bdevs")).To(ContainSubstring(backendVolumes[namespace]))
			}

			for index, namespace := range deletionOrder {
				_, err := kubectl("delete", "nvmepartition", "-n", namespace, partitionName, "--wait=true", "--timeout=120s")
				Expect(err).NotTo(HaveOccurred())
				Expect(rpc("nvmf_get_subsystems")).NotTo(ContainSubstring(nqns[namespace]))
				Expect(rpc("bdev_get_bdevs")).NotTo(ContainSubstring(backendVolumes[namespace]))
				if index == 0 {
					other := deletionOrder[1]
					out, getErr := kubectl("get", "nvmepartition", "-n", other, partitionName, "-o", "jsonpath={.status.state}")
					Expect(getErr).NotTo(HaveOccurred())
					Expect(out).To(Equal("Exported"))
					Expect(rpc("nvmf_get_subsystems")).To(ContainSubstring(nqns[other]))
					Expect(rpc("bdev_get_bdevs")).To(ContainSubstring(backendVolumes[other]))
				}
			}
		}
	})
})

var _ = Describe("Exact SPDK teardown", Label("green", "F5", "spdk"), func() {
	It("removes the persisted lvol after an already-completed unexport", func() {
		const (
			claimName     = "regression-f5-claim"
			partitionName = "regression-f5-partition"
		)
		serial := serialForNode("distort-master")
		DeferCleanup(func() {
			_, _ = kubectl("delete", "nvmepartition", partitionName, "--ignore-not-found", "--wait=false")
			_, _ = kubectl("delete", "nvmedeviceclaim", claimName, "--ignore-not-found", "--wait=false")
		})

		_, err := applyManifest(fmt.Sprintf(`
apiVersion: storage.distort.io/v1alpha1
kind: NVMeDeviceClaim
metadata:
  name: %s
spec:
  serialNumber: %s
---
apiVersion: storage.distort.io/v1alpha1
kind: NVMePartition
metadata:
  name: %s
spec:
  size: 64Mi
  targetBackend: spdk
  volumeManager: partition
`, claimName, serial, partitionName))
		Expect(err).NotTo(HaveOccurred())
		Eventually(func(g Gomega) {
			out, getErr := kubectl("get", "nvmepartition", partitionName, "-o", "jsonpath={.status.state}")
			g.Expect(getErr).NotTo(HaveOccurred())
			g.Expect(out).To(Equal("Exported"))
		}, 180*time.Second, 5*time.Second).Should(Succeed())

		statusField := func(field string) string {
			out, getErr := kubectl("get", "nvmepartition", partitionName, "-o", "jsonpath={.status."+field+"}")
			Expect(getErr).NotTo(HaveOccurred())
			Expect(out).NotTo(BeEmpty(), "status.%s was not persisted", field)
			return out
		}
		nqn := statusField("nqn")
		backendVolumeID := statusField("backendVolumeID")
		baseBdev := statusField("spdkBaseBdev")
		lvstoreName := statusField("spdkLvstoreName")
		lvstoreUUID := statusField("spdkLvstoreUUID")
		lvolName := statusField("spdkLvolName")
		lvolUUID := statusField("spdkLvolUUID")
		Expect(backendVolumeID).To(Equal(lvstoreName + "/" + lvolName))

		agentPod, err := kubectl(
			"get", "pod", "-n", "distort-system",
			"-l", "app.kubernetes.io/component=agent",
			"--field-selector", "spec.nodeName=distort-master",
			"-o", "jsonpath={.items[0].metadata.name}",
		)
		Expect(err).NotTo(HaveOccurred())
		agentPod = strings.TrimSpace(agentPod)
		rpc := func(method string, args ...string) string {
			command := []string{"exec", "-n", "distort-system", agentPod, "--", "/opt/spdk/scripts/rpc.py", method}
			command = append(command, args...)
			out, rpcErr := kubectl(command...)
			Expect(rpcErr).NotTo(HaveOccurred())
			return out
		}

		Expect(rpc("nvmf_get_subsystems")).To(ContainSubstring(nqn))
		bdevs := rpc("bdev_get_bdevs")
		Expect(bdevs).To(ContainSubstring(backendVolumeID))
		Expect(bdevs).To(ContainSubstring(lvolUUID))
		lvstores := rpc("bdev_lvol_get_lvstores")
		Expect(lvstores).To(ContainSubstring(baseBdev))
		Expect(lvstores).To(ContainSubstring(lvstoreUUID))

		// Model a process crash after unexport and before lvol deletion. The
		// reconciler must accept the already-absent subsystem and resume cleanup.
		rpc("nvmf_delete_subsystem", nqn)
		_, err = kubectl("delete", "nvmepartition", partitionName, "--wait=true", "--timeout=120s")
		Expect(err).NotTo(HaveOccurred())

		Expect(rpc("nvmf_get_subsystems")).NotTo(ContainSubstring(nqn))
		bdevs = rpc("bdev_get_bdevs")
		Expect(bdevs).NotTo(ContainSubstring(backendVolumeID))
		Expect(bdevs).NotTo(ContainSubstring(lvolUUID))
		lvstores = rpc("bdev_lvol_get_lvstores")
		Expect(lvstores).To(ContainSubstring(lvstoreName), "the reusable lvstore should remain")
		Expect(lvstores).To(ContainSubstring(lvstoreUUID))
		Expect(lvstores).To(ContainSubstring(baseBdev))

		_, err = kubectl("delete", "nvmedeviceclaim", claimName, "--wait=true", "--timeout=120s")
		Expect(err).NotTo(HaveOccurred())
	})
})

var _ = Describe("Kernel partition isolation", Label("green", "F3", "kernel", "release-gate", "volume-isolation", "backend-cleanup"), func() {
	It("allocates reusable partition numbers without damaging surviving volumes", func() {
		const (
			namespace = "distort-f3"
			claimName = "regression-f3-claim"
			className = "regression-f3-kernel"
		)
		serial := serialForNode("distort-worker-2")
		agentPod, err := kubectl(
			"get", "pod", "-n", "distort-system",
			"-l", "app.kubernetes.io/component=agent",
			"--field-selector", "spec.nodeName=distort-worker-2",
			"-o", "jsonpath={.items[0].metadata.name}",
		)
		Expect(err).NotTo(HaveOccurred())
		agentPod = strings.TrimSpace(agentPod)
		partitionNodeExists := func(path string) bool {
			_, statErr := kubectl("exec", "-n", "distort-system", agentPod, "--", "test", "-b", path)
			return statErr == nil
		}
		DeferCleanup(func() {
			_, _ = kubectl("delete", "namespace", namespace, "--ignore-not-found", "--wait=false")
			_, _ = kubectl("delete", "storageclass", className, "--ignore-not-found", "--wait=false")
		})

		_, err = applyManifest(fmt.Sprintf(`
apiVersion: v1
kind: Namespace
metadata:
  name: %s
---
apiVersion: storage.distort.io/v1alpha1
kind: NVMeDeviceClaim
metadata:
  namespace: %s
  name: %s
spec:
  serialNumber: %s
---
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: %s
provisioner: storage.distort.io
volumeBindingMode: WaitForFirstConsumer
parameters:
  target-backend: kernel
  volume-manager: partition
`, namespace, namespace, claimName, serial, className))
		Expect(err).NotTo(HaveOccurred())
		Eventually(func(g Gomega) {
			out, getErr := kubectl("get", "nvmedeviceclaim", "-n", namespace, claimName, "-o", "jsonpath={.status.active}")
			g.Expect(getErr).NotTo(HaveOccurred())
			g.Expect(out).To(Equal("true"))
		}, 90*time.Second, 2*time.Second).Should(Succeed())

		createPVC := func(name string) {
			_, createErr := applyManifest(fmt.Sprintf(`
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  namespace: %s
  name: %s
spec:
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: 64Mi
  storageClassName: %s
`, namespace, name, className))
			Expect(createErr).NotTo(HaveOccurred())
		}
		createPod := func(name, claim string) {
			_, createErr := applyManifest(fmt.Sprintf(`
apiVersion: v1
kind: Pod
metadata:
  namespace: %s
  name: %s
spec:
  nodeSelector:
    kubernetes.io/hostname: distort-worker-1
  containers:
  - name: test
    image: busybox:1.36
    command: ["sleep", "3600"]
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: %s
`, namespace, name, claim))
			Expect(createErr).NotTo(HaveOccurred())
		}
		deletePod := func(name string) {
			_, deleteErr := kubectl("delete", "pod", "-n", namespace, name, "--wait=true", "--timeout=120s")
			Expect(deleteErr).NotTo(HaveOccurred())
		}
		partitionForPVC := func(pvc string) (string, string) {
			var pv string
			Eventually(func(g Gomega) {
				var getErr error
				pv, getErr = kubectl("get", "pvc", "-n", namespace, pvc, "-o", "jsonpath={.spec.volumeName}")
				g.Expect(getErr).NotTo(HaveOccurred())
				g.Expect(pv).NotTo(BeEmpty())
			}, 180*time.Second, 5*time.Second).Should(Succeed())
			Eventually(func(g Gomega) {
				out, getErr := kubectl("get", "nvmepartition", "-n", namespace, pv, "-o", "jsonpath={.status.state}")
				g.Expect(getErr).NotTo(HaveOccurred())
				g.Expect(out).To(Equal("Exported"))
			}, 180*time.Second, 5*time.Second).Should(Succeed())
			path, getErr := kubectl("get", "nvmepartition", "-n", namespace, pv, "-o", "jsonpath={.status.backendVolumeID}")
			Expect(getErr).NotTo(HaveOccurred())
			return pv, path
		}
		deletePVCAndWait := func(pvc, partition string) {
			_, deleteErr := kubectl("delete", "pvc", "-n", namespace, pvc, "--wait=true", "--timeout=120s")
			Expect(deleteErr).NotTo(HaveOccurred())
			Eventually(func(g Gomega) {
				out, getErr := kubectl("get", "nvmepartition", "-n", namespace, partition,
					"--ignore-not-found", "-o", "name")
				g.Expect(getErr).NotTo(HaveOccurred())
				g.Expect(out).To(BeEmpty())
			}, 180*time.Second, 5*time.Second).Should(Succeed())
		}

		createPVC("a")
		createPVC("b")
		createPod("consumer-a", "a")
		createPod("consumer-b", "b")
		partitionA, pathA := partitionForPVC("a")
		partitionB, pathB := partitionForPVC("b")
		Expect(pathA).NotTo(Equal(pathB))
		Expect([]string{pathA, pathB}).To(ConsistOf(
			MatchRegexp(`p1$`),
			MatchRegexp(`p2$`),
		))
		Expect(partitionNodeExists(pathA)).To(BeTrue())
		Expect(partitionNodeExists(pathB)).To(BeTrue())
		deletePod("consumer-a")
		deletePod("consumer-b")

		deletePVCAndWait("a", partitionA)
		Eventually(partitionNodeExists, 30*time.Second, time.Second).WithArguments(pathA).Should(BeFalse())
		Expect(partitionNodeExists(pathB)).To(BeTrue())
		Eventually(func(g Gomega) {
			out, getErr := kubectl("get", "nvmepartition", "-n", namespace, partitionB,
				"-o", "jsonpath={.status.state} {.status.backendVolumeID}")
			g.Expect(getErr).NotTo(HaveOccurred())
			g.Expect(out).To(Equal("Exported " + pathB))
		}, 90*time.Second, 2*time.Second).Should(Succeed())

		createPVC("c")
		createPod("consumer-c", "c")
		partitionC, pathC := partitionForPVC("c")
		Expect(pathC).To(Equal(pathA), "the lowest deleted partition number should be reused")
		deletePod("consumer-c")

		deletePVCAndWait("b", partitionB)
		Eventually(partitionNodeExists, 30*time.Second, time.Second).WithArguments(pathB).Should(BeFalse())
		Expect(partitionNodeExists(pathC)).To(BeTrue())
		Eventually(func(g Gomega) {
			out, getErr := kubectl("get", "nvmepartition", "-n", namespace, partitionC,
				"-o", "jsonpath={.status.state} {.status.backendVolumeID}")
			g.Expect(getErr).NotTo(HaveOccurred())
			g.Expect(out).To(Equal("Exported " + pathC))
		}, 90*time.Second, 2*time.Second).Should(Succeed())
		deletePVCAndWait("c", partitionC)
		Eventually(partitionNodeExists, 30*time.Second, time.Second).WithArguments(pathC).Should(BeFalse())
	})
})

var _ = Describe("Capacity validation", Label("green", "F7"), func() {
	It("rejects unsafe sizes and persists upward-rounded capacity", func() {
		for _, size := range []string{"0", "-1Mi", "9223372036853727233"} {
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

		const (
			claimName     = "regression-f7-claim"
			partitionName = "regression-f7-rounded"
		)
		serial := serialForNode("distort-master")
		DeferCleanup(func() {
			_, _ = kubectl("delete", "nvmepartition", partitionName, "--ignore-not-found", "--wait=false")
			_, _ = kubectl("delete", "nvmedeviceclaim", claimName, "--ignore-not-found", "--wait=false")
		})
		_, err := applyManifest(fmt.Sprintf(`
apiVersion: storage.distort.io/v1alpha1
kind: NVMeDeviceClaim
metadata:
  name: %s
spec:
  serialNumber: %s
---
apiVersion: storage.distort.io/v1alpha1
kind: NVMePartition
metadata:
  name: %s
spec:
  size: 1048577
  targetBackend: spdk
  volumeManager: partition
`, claimName, serial, partitionName))
		Expect(err).NotTo(HaveOccurred())
		Eventually(func(g Gomega) {
			out, getErr := kubectl("get", "nvmepartition", partitionName,
				"-o", "jsonpath={.status.state} {.status.allocatedCapacity}")
			g.Expect(getErr).NotTo(HaveOccurred())
			g.Expect(out).To(Equal("Exported 2Mi"))
		}, 180*time.Second, 5*time.Second).Should(Succeed())
		_, err = kubectl("delete", "nvmepartition", partitionName, "--wait=true", "--timeout=120s")
		Expect(err).NotTo(HaveOccurred())
		_, err = kubectl("delete", "nvmedeviceclaim", claimName, "--wait=true", "--timeout=120s")
		Expect(err).NotTo(HaveOccurred())
	})
})

var _ = Describe("Single-writer attachment fencing", Label("green", "F25", "csi", "spdk", "release-gate"), func() {
	It("rejects a competing node and performs only an explicitly approved takeover", func() {
		const (
			claimName       = "regression-f25-claim"
			storageClass    = "regression-f25-sc"
			pvcName         = "regression-f25-pvc"
			firstPod        = "regression-f25-a"
			secondPod       = "regression-f25-b"
			manualAttach    = "regression-f25-competing"
			providerNode    = "distort-worker-1"
			firstConsumer   = "distort-master"
			secondConsumer  = "distort-worker-2"
			forceAnnotation = "storage.distort.io/force-detach-node"
		)

		DeferCleanup(func() {
			_, _ = kubectl("delete", "pod", firstPod, secondPod, "--ignore-not-found", "--wait=false")
			_, _ = kubectl("delete", "volumeattachment", manualAttach, "--ignore-not-found", "--wait=false")
			_, _ = kubectl("delete", "pvc", pvcName, "--ignore-not-found", "--wait=false")
			_, _ = kubectl("delete", "storageclass", storageClass, "--ignore-not-found", "--wait=false")
			_, _ = kubectl("delete", "nvmedeviceclaim", claimName, "--ignore-not-found", "--wait=false")
		})

		serial := serialForNode(providerNode)
		_, err := applyManifest(fmt.Sprintf(`
apiVersion: storage.distort.io/v1alpha1
kind: NVMeDeviceClaim
metadata:
  name: %s
spec:
  serialNumber: %s
---
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: %s
provisioner: storage.distort.io
volumeBindingMode: WaitForFirstConsumer
parameters:
  target-backend: spdk
  volume-manager: partition
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: %s
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: %s
  resources:
    requests:
      storage: 64Mi
---
apiVersion: v1
kind: Pod
metadata:
  name: %s
spec:
  nodeSelector:
    kubernetes.io/hostname: %s
  containers:
  - name: consumer
    image: busybox:1.36
    command: ["sleep", "3600"]
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: %s
`, claimName, serial, storageClass, pvcName, storageClass, firstPod, firstConsumer, pvcName))
		Expect(err).NotTo(HaveOccurred())

		Eventually(func(g Gomega) {
			out, getErr := kubectl("get", "pod", firstPod, "-o", "jsonpath={.status.phase}")
			g.Expect(getErr).NotTo(HaveOccurred())
			g.Expect(out).To(Equal("Running"))
		}, 240*time.Second, 5*time.Second).Should(Succeed())
		_, err = kubectl("exec", firstPod, "--", "sh", "-c", "echo fenced-data > /data/f25.txt && sync")
		Expect(err).NotTo(HaveOccurred())

		pvName, err := kubectl("get", "pvc", pvcName, "-o", "jsonpath={.spec.volumeName}")
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(pvName)).NotTo(BeEmpty())
		attachmentFields, err := kubectl("get", "nvmevolumeattachments", "-o",
			`jsonpath={range .items[*]}{.metadata.namespace}{" "}{.metadata.name}{" "}{.spec.nodeID}{" "}{.spec.hostNQN}{" "}{.spec.attachmentID}{"\n"}{end}`)
		Expect(err).NotTo(HaveOccurred())
		fields := strings.Fields(attachmentFields)
		Expect(fields).To(HaveLen(5), "expected exactly one active DISTORT attachment, got %q", attachmentFields)
		attachmentNamespace, attachmentName := fields[0], fields[1]
		oldHostNQN, oldAttachmentID := fields[3], fields[4]
		Expect(fields[2]).To(Equal(firstConsumer))

		_, err = applyManifest(fmt.Sprintf(`
apiVersion: storage.k8s.io/v1
kind: VolumeAttachment
metadata:
  name: %s
spec:
  attacher: storage.distort.io
  nodeName: %s
  source:
    persistentVolumeName: %s
`, manualAttach, secondConsumer, strings.TrimSpace(pvName)))
		Expect(err).NotTo(HaveOccurred())

		By("Proving a competing node cannot acquire the single-writer attachment")
		Eventually(func(g Gomega) {
			out, getErr := kubectl("get", "volumeattachment", manualAttach,
				"-o", "jsonpath={.status.attachError.message}")
			g.Expect(getErr).NotTo(HaveOccurred())
			g.Expect(out).To(ContainSubstring("attached to node"))
		}, 120*time.Second, 3*time.Second).Should(Succeed())
		Consistently(func(g Gomega) {
			out, getErr := kubectl("get", "nvmevolumeattachment", "-n", attachmentNamespace, attachmentName,
				"-o", "jsonpath={.spec.nodeID}")
			g.Expect(getErr).NotTo(HaveOccurred())
			g.Expect(out).To(Equal(firstConsumer))
		}, 15*time.Second, 2*time.Second).Should(Succeed())

		By("Explicitly approving takeover only after the administrator has fenced the old node")
		_, err = kubectl("annotate", "nvmevolumeattachment", "-n", attachmentNamespace, attachmentName,
			forceAnnotation+"="+firstConsumer, "--overwrite")
		Expect(err).NotTo(HaveOccurred())
		Eventually(func(g Gomega) {
			out, getErr := kubectl("get", "volumeattachment", manualAttach, "-o", "jsonpath={.status.attached}")
			g.Expect(getErr).NotTo(HaveOccurred())
			g.Expect(out).To(Equal("true"))
		}, 180*time.Second, 3*time.Second).Should(Succeed())

		var newHostNQN string
		Eventually(func(g Gomega) {
			out, getErr := kubectl("get", "nvmevolumeattachment", "-n", attachmentNamespace, attachmentName,
				"-o", `jsonpath={.spec.nodeID}{" "}{.spec.hostNQN}{" "}{.spec.attachmentID}{" "}{.status.conditions[?(@.type=="AccessReady")].status}`)
			g.Expect(getErr).NotTo(HaveOccurred())
			current := strings.Fields(out)
			g.Expect(current).To(HaveLen(4))
			g.Expect(current[0]).To(Equal(secondConsumer))
			g.Expect(current[2]).NotTo(Equal(oldAttachmentID))
			g.Expect(current[3]).To(Equal("True"))
			newHostNQN = current[1]
		}, 180*time.Second, 3*time.Second).Should(Succeed())
		Expect(newHostNQN).NotTo(Equal(oldHostNQN))

		partitionName, err := kubectl("get", "nvmevolumeattachment", "-n", attachmentNamespace, attachmentName,
			"-o", "jsonpath={.spec.volumeRef.name}")
		Expect(err).NotTo(HaveOccurred())
		provider, err := kubectl("get", "nvmepartition", "-n", attachmentNamespace, partitionName,
			"-o", "jsonpath={.spec.nodeName}")
		Expect(err).NotTo(HaveOccurred())
		agentPod, err := kubectl("get", "pod", "-n", "distort-system",
			"-l", "app.kubernetes.io/component=agent", "--field-selector", "spec.nodeName="+provider,
			"-o", "jsonpath={.items[0].metadata.name}")
		Expect(err).NotTo(HaveOccurred())
		subsystems, err := kubectl("exec", "-n", "distort-system", strings.TrimSpace(agentPod), "--",
			"/opt/spdk/scripts/rpc.py", "nvmf_get_subsystems")
		Expect(err).NotTo(HaveOccurred())
		Expect(subsystems).To(ContainSubstring(newHostNQN))
		Expect(subsystems).NotTo(ContainSubstring(oldHostNQN))

		By("Removing the stale consumer and proving its delayed unpublish cannot revoke the new owner")
		_, err = kubectl("delete", "pod", firstPod, "--wait=true", "--timeout=120s")
		Expect(err).NotTo(HaveOccurred())
		Consistently(func(g Gomega) {
			out, getErr := kubectl("get", "nvmevolumeattachment", "-n", attachmentNamespace, attachmentName,
				"-o", "jsonpath={.spec.nodeID}")
			g.Expect(getErr).NotTo(HaveOccurred())
			g.Expect(out).To(Equal(secondConsumer))
		}, 15*time.Second, 2*time.Second).Should(Succeed())

		_, err = applyManifest(fmt.Sprintf(`
apiVersion: v1
kind: Pod
metadata:
  name: %s
spec:
  nodeSelector:
    kubernetes.io/hostname: %s
  containers:
  - name: consumer
    image: busybox:1.36
    command: ["sleep", "3600"]
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: %s
`, secondPod, secondConsumer, pvcName))
		Expect(err).NotTo(HaveOccurred())
		Eventually(func(g Gomega) {
			out, getErr := kubectl("get", "pod", secondPod, "-o", "jsonpath={.status.phase}")
			g.Expect(getErr).NotTo(HaveOccurred())
			g.Expect(out).To(Equal("Running"))
		}, 180*time.Second, 5*time.Second).Should(Succeed())
		out, err := kubectl("exec", secondPod, "--", "cat", "/data/f25.txt")
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(ContainSubstring("fenced-data"))
	})
})

var _ = Describe("Review finding API and authorization regressions", func() {
	It("gives each workload only its required Kubernetes API access", Label("F18", "rbac"), func() {
		checks := []struct {
			account  string
			verb     string
			resource string
			want     string
		}{
			{account: "distort-manager", verb: "update", resource: "nvmepartitions.storage.distort.io", want: "yes"},
			{account: "distort-agent", verb: "update", resource: "nvmepartitions.storage.distort.io/status", want: "yes"},
			{account: "distort-csi-controller", verb: "create", resource: "nvmepartitions.storage.distort.io", want: "yes"},
			{account: "distort-manager", verb: "create", resource: "persistentvolumes", want: "no"},
			{account: "distort-agent", verb: "create", resource: "persistentvolumeclaims", want: "no"},
			{account: "distort-csi-controller", verb: "update", resource: "nvmedevices.storage.distort.io", want: "no"},
			{account: "distort-csi-node", verb: "get", resource: "nvmepartitions.storage.distort.io", want: "no"},
		}
		for _, account := range []string{"distort-manager", "distort-agent", "distort-csi-controller", "distort-csi-node"} {
			checks = append(checks, struct {
				account  string
				verb     string
				resource string
				want     string
			}{account: account, verb: "update", resource: "nodes", want: "no"})
		}
		for _, check := range checks {
			By(fmt.Sprintf("checking %s can %s %s = %s", check.account, check.verb, check.resource, check.want))
			out, err := kubectl(
				"auth", "can-i", check.verb, check.resource,
				"--as=system:serviceaccount:distort-system:"+check.account,
			)
			if check.want == "yes" {
				Expect(err).NotTo(HaveOccurred())
			} else {
				Expect(err).To(HaveOccurred(), "kubectl auth can-i returns status 1 when access is denied")
			}
			Expect(strings.Fields(out)).NotTo(BeEmpty())
			Expect(strings.Fields(out)[len(strings.Fields(out))-1]).To(Equal(check.want))
		}
	})

	It("rejects the unimplemented lvm manager at admission", Label("F20", "admission"), func() {
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
			_, _ = kubectl("delete", "nvmepartition", "regression-f17-volume", "--ignore-not-found", "--wait=true", "--timeout=120s")
			_, _ = kubectl("delete", "nvmedeviceclaim", "regression-f17-claim", "--ignore-not-found", "--wait=true", "--timeout=120s")
			_, _ = kubectl("delete", "pod", "-n", "distort-system", strings.TrimSpace(agentPod), "--ignore-not-found", "--wait=true")
			_, _ = kubectl("rollout", "status", "-n", "distort-system", "daemonset/distort-agent", "--timeout=180s")
		})

		Eventually(func(g Gomega) {
			out, getErr := kubectl("get", "nvmepartition", "regression-f17-volume", "-o", "jsonpath={.status.state}")
			g.Expect(getErr).NotTo(HaveOccurred())
			g.Expect(out).To(Equal("Exported"))
		}, 180*time.Second, 5*time.Second).Should(Succeed())
		expectedNQN, err := kubectl("get", "nvmepartition", "regression-f17-volume", "-o", "jsonpath={.status.nqn}")
		Expect(err).NotTo(HaveOccurred())
		Expect(expectedNQN).NotTo(BeEmpty())

		targetPIDs, err := kubectl(
			"exec", "-n", "distort-system", strings.TrimSpace(agentPod), "-c", "agent", "--", "pidof", "nvmf_tgt",
		)
		Expect(err).NotTo(HaveOccurred())
		pids := strings.Fields(targetPIDs)
		Expect(pids).NotTo(BeEmpty(), "the exported partition must have a running nvmf_tgt process")
		_, err = kubectl(
			"exec", "-n", "distort-system", strings.TrimSpace(agentPod), "-c", "agent", "--", "kill", "-9", pids[0],
		)
		Expect(err).NotTo(HaveOccurred())

		Eventually(func(g Gomega) {
			out, rpcErr := kubectl(
				"exec", "-n", "distort-system", strings.TrimSpace(agentPod), "-c", "agent", "--",
				"/opt/spdk/scripts/rpc.py", "nvmf_get_subsystems",
			)
			g.Expect(rpcErr).NotTo(HaveOccurred())
			g.Expect(out).To(ContainSubstring(expectedNQN))
		}, 90*time.Second, 5*time.Second).Should(Succeed())
	})
})
