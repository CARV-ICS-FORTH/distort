//go:build e2e
// +build e2e

package e2e

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"distort/test/utils"
)

var _ = Describe("DISTORT Unified E2E Test Suite", Ordered, func() {
	SetDefaultEventuallyTimeout(5 * time.Minute)
	SetDefaultEventuallyPollingInterval(5 * time.Second)

	Context("Layer 1: Agent & Hardware Independence", func() {
		It("Discovery Check: Should discover NVMe devices and RDMA storage nodes", func() {
			By("waiting for NVMeDevice CRDs to be populated")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "nvmedevices", "-o", "jsonpath={.items[*].metadata.name}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				// Should find at least 2 NVMe devices (from master and worker)
				devices := strings.Fields(out)
				g.Expect(len(devices)).To(BeNumerically(">=", 2))
			}).Should(Succeed())

			By("waiting for RDMAStorageNode CRDs to be populated")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "rdmastoragenodes", "-o", "jsonpath={.items[*].metadata.name}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				nodes := strings.Fields(out)
				g.Expect(len(nodes)).To(BeNumerically(">=", 1))
			}).Should(Succeed())

			By("verifying RDMAStorageNode CRDs report 0 capacity initially")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "rdmastoragenodes", "-o", "jsonpath={.items[*].status.totalCapacity}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				capacities := strings.Fields(out)
				g.Expect(len(capacities)).To(BeNumerically(">=", 1))
				for _, cap := range capacities {
					g.Expect(cap).To(Equal("0"))
				}
			}).Should(Succeed())
		})

		It("Partitioning Engine & NVMe-oF Exporting: Should slice a drive and export it", func() {
			By("Creating a raw NVMePartition CRD specifically for distort-worker-1")
			partitionYaml := `
apiVersion: storage.distort.io/v1alpha1
kind: NVMePartition
metadata:
  name: e2e-test-partition
spec:
  size: 500Mi
  nodeName: distort-worker-1
  parentDeviceSerialNumber: SN-distort-worker-1
`
			cmd := exec.Command("sh", "-c", fmt.Sprintf("echo '%s' | kubectl apply -f -", partitionYaml))
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for the NVMePartition to reach Exported state")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "nvmepartition", "e2e-test-partition", "-o", "jsonpath={.status.state}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal("Exported"))
			}).Should(Succeed())

			By("Using SSH to verify physical partition creation via lsblk")
			cmd = exec.Command("sh", "-c", "cd vagrant && vagrant ssh distort-worker-1 -c 'lsblk | grep nvme'")
			out, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			// e.g. nvme0n1p1 or nvme1n1p1
			Expect(out).To(ContainSubstring("p1"))

			By("Using SSH to verify NVMe-oF subsystem export")
			cmd = exec.Command("sh", "-c", "cd vagrant && vagrant ssh distort-worker-1 -c 'sudo ls /sys/kernel/config/nvmet/subsystems'")
			out, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(ContainSubstring("nqn."))

			By("Cleaning up the test partition")
			cmd = exec.Command("kubectl", "delete", "nvmepartition", "e2e-test-partition")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("Layer 2: Management Controllers", func() {
		It("Claim Binding: Should bind an NVMeDeviceClaim to a device by serial number", func() {
			// Get a valid serial number from the discovered devices
			cmd := exec.Command("kubectl", "get", "nvmedevices", "-o", "jsonpath={.items[0].spec.serialNumber}")
			out, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(out).NotTo(BeEmpty())
			serialNum := out

			By("Creating an NVMeDeviceClaim for serial: " + serialNum)
			claimYaml := fmt.Sprintf(`
apiVersion: storage.distort.io/v1alpha1
kind: NVMeDeviceClaim
metadata:
  name: e2e-test-claim
spec:
  serialNumber: %s
`, serialNum)
			cmd = exec.Command("sh", "-c", fmt.Sprintf("echo '%s' | kubectl apply -f -", claimYaml))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the claim is set to Active")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "nvmedeviceclaim", "e2e-test-claim", "-o", "jsonpath={.status.active}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal("true"))
			}).Should(Succeed())

			By("Verifying the claimed device capacity is added to the RDMAStorageNode")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "nvmedeviceclaim", "e2e-test-claim", "-o", "jsonpath={.status.nodeName}")
				nodeName, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(nodeName).NotTo(BeEmpty())

				cmd = exec.Command("kubectl", "get", "rdmastoragenode", nodeName, "-o", "jsonpath={.status.totalCapacity}")
				capacity, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(capacity).NotTo(Equal("0"))
				g.Expect(capacity).NotTo(BeEmpty())
			}).Should(Succeed())

			// Keep the claim active for subsequent tests (Scheduling & CSI Provisioning)
		})

		It("Capacity Scheduling: Should selectively schedule generic NVMePartitions based on capacity", func() {
			By("Creating a generic NVMePartition without a nodeName")
			partitionYaml := `
apiVersion: storage.distort.io/v1alpha1
kind: NVMePartition
metadata:
  name: e2e-schedule-partition
spec:
  size: 200Mi
`
			cmd := exec.Command("sh", "-c", fmt.Sprintf("echo '%s' | kubectl apply -f -", partitionYaml))
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for the Manager to assign a valid nodeName")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "nvmepartition", "e2e-schedule-partition", "-o", "jsonpath={.spec.nodeName}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).NotTo(BeEmpty())
				g.Expect(out).To(MatchRegexp(`distort-(worker|master).*`))
			}).Should(Succeed())

			By("Verifying FreeCapacity decreased on the RDMAStorageNode")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "nvmepartition", "e2e-schedule-partition", "-o", "jsonpath={.spec.nodeName}")
				nodeName, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(nodeName).NotTo(BeEmpty())

				cmd = exec.Command("kubectl", "get", "rdmastoragenode", nodeName, "-o", "jsonpath={.status.totalCapacity}")
				totalCap, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())

				cmd = exec.Command("kubectl", "get", "rdmastoragenode", nodeName, "-o", "jsonpath={.status.freeCapacity}")
				freeCap, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())

				g.Expect(freeCap).NotTo(Equal(totalCap))
				g.Expect(freeCap).NotTo(BeEmpty())
			}).Should(Succeed())

			By("Cleaning up the partition")
			cmd = exec.Command("kubectl", "delete", "nvmepartition", "e2e-schedule-partition")
			_, _ = utils.Run(cmd)
		})
	})

	Context("Layer 3: CSI & Full Stack", func() {
		It("CSI Provisioning, Client Mounting & Persistence", func() {
			By("Installing the StorageClass")
			scYaml := `
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: distort-csi-sc
provisioner: storage.distort.io
volumeBindingMode: WaitForFirstConsumer
`
			cmd := exec.Command("sh", "-c", fmt.Sprintf("echo '%s' | kubectl apply -f -", scYaml))
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("Creating a PVC")
			pvcYaml := `
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: e2e-distort-pvc
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 500Mi
  storageClassName: distort-csi-sc
`
			cmd = exec.Command("sh", "-c", fmt.Sprintf("echo '%s' | kubectl apply -f -", pvcYaml))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("Creating a consumer Pod to trigger provisioning")
			podYaml := `
apiVersion: v1
kind: Pod
metadata:
  name: e2e-distort-pod
spec:
  containers:
  - name: e2e-container
    image: busybox
    command: ["sleep", "3600"]
    volumeMounts:
    - name: distort-vol
      mountPath: /data
  volumes:
  - name: distort-vol
    persistentVolumeClaim:
      claimName: e2e-distort-pvc
`
			cmd = exec.Command("sh", "-c", fmt.Sprintf("echo '%s' | kubectl apply -f -", podYaml))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for the PVC to bound, reflecting NVMePartition creation and export")
			Eventually(func(g Gomega) {
				cmd = exec.Command("kubectl", "get", "pvc", "e2e-distort-pvc", "-o", "jsonpath={.status.phase}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal("Bound"))
			}).Should(Succeed())

			By("Waiting for the consumer Pod to reach Running state (proves nvme connect & mount)")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pod", "e2e-distort-pod", "-o", "jsonpath={.status.phase}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal("Running"))
			}).Should(Succeed())

			By("Writing data into the persistent volume")
			cmd = exec.Command("kubectl", "exec", "e2e-distort-pod", "--", "sh", "-c", "echo 'distort-rocks' > /data/test.txt")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("Recreating the consumer Pod")
			cmd = exec.Command("kubectl", "delete", "pod", "e2e-distort-pod", "--force", "--grace-period=0")
			_, _ = utils.Run(cmd)

			cmd = exec.Command("sh", "-c", fmt.Sprintf("echo '%s' | kubectl apply -f -", podYaml))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pod", "e2e-distort-pod", "-o", "jsonpath={.status.phase}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal("Running"))
			}).Should(Succeed())

			By("Reading data from the persistent volume to verify persistence")
			cmd = exec.Command("kubectl", "exec", "e2e-distort-pod", "--", "sh", "-c", "cat /data/test.txt")
			out, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(ContainSubstring("distort-rocks"))

			By("Cleaning up")
			exec.Command("kubectl", "delete", "pod", "e2e-distort-pod", "--force", "--grace-period=0").Run()
			exec.Command("kubectl", "delete", "pvc", "e2e-distort-pvc").Run()
			exec.Command("kubectl", "delete", "sc", "distort-csi-sc").Run()
			exec.Command("kubectl", "delete", "nvmedeviceclaim", "e2e-test-claim").Run()
		})
	})
})
