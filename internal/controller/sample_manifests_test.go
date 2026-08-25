package controller

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	storagev1alpha1 "distort/api/v1alpha1"
)

var _ = Describe("Sample manifests", func() {
	samples := []struct {
		filename   string
		object     client.Object
		namespaced bool
	}{
		{filename: "storage_v1alpha1_nvmedevice.yaml", object: &storagev1alpha1.NVMeDevice{}},
		{filename: "storage_v1alpha1_nvmedeviceclaim.yaml", object: &storagev1alpha1.NVMeDeviceClaim{}, namespaced: true},
		{filename: "storage_v1alpha1_nvmepartition.yaml", object: &storagev1alpha1.NVMePartition{}, namespaced: true},
		{filename: "storage_v1alpha1_rdmastoragenode.yaml", object: &storagev1alpha1.RDMAStorageNode{}},
	}

	for _, sample := range samples {
		It("passes API validation for "+sample.filename, func() {
			contents, err := os.ReadFile(filepath.Join("..", "..", "config", "samples", sample.filename))
			Expect(err).NotTo(HaveOccurred())
			Expect(yaml.Unmarshal(contents, sample.object)).To(Succeed())
			if sample.namespaced && sample.object.GetNamespace() == "" {
				sample.object.SetNamespace("default")
			}
			Expect(k8sClient.Create(ctx, sample.object, &client.CreateOptions{
				DryRun: []string{metav1.DryRunAll},
			})).To(Succeed())
		})
	}
})
