package csi

import (
	"context"
	"maps"
	"strings"
	"testing"
	"time"

	csipb "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	storagev1alpha1 "distort/api/v1alpha1"
	attachmentidentity "distort/internal/attachment"
	"distort/internal/capacity"
	"distort/internal/volumeidentity"
)

const testSPDKCoreMask = "0x3"

type immediatelyExportingClient struct {
	client.Client
}

func (c *immediatelyExportingClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if obj.GetUID() == "" {
		obj.SetUID(uuid.NewUUID())
	}
	if err := c.Client.Create(ctx, obj, opts...); err != nil {
		return err
	}
	partition, ok := obj.(*storagev1alpha1.NVMePartition)
	if !ok {
		attachment, attachmentOK := obj.(*storagev1alpha1.NVMeVolumeAttachment)
		if !attachmentOK {
			return nil
		}
		var stored storagev1alpha1.NVMeVolumeAttachment
		key := types.NamespacedName{Name: attachment.Name, Namespace: attachment.Namespace}
		if err := c.Get(ctx, key, &stored); err != nil {
			return err
		}
		stored.Status.ObservedAttachmentID = stored.Spec.AttachmentID
		meta.SetStatusCondition(&stored.Status.Conditions, metav1.Condition{
			Type: attachmentidentity.AccessReadyCondition, Status: metav1.ConditionTrue,
			Reason: "TestTargetReady", Message: "Test target authorized host access",
		})
		return c.Client.Status().Update(ctx, &stored)
	}
	var stored storagev1alpha1.NVMePartition
	key := types.NamespacedName{Name: partition.Name, Namespace: partition.Namespace}
	if err := c.Get(ctx, key, &stored); err != nil {
		return err
	}
	identity, err := volumeidentity.New(stored.Namespace, stored.Name, stored.UID)
	if err != nil {
		return err
	}
	stored.Status.State = storagev1alpha1.NVMePartitionStateExported
	stored.Status.ExternalID = identity.ExternalID
	stored.Status.VolumeID = identity.VolumeHandle
	stored.Status.BackendVolumeID = "lvs_test/" + identity.ExternalID
	allocatedBytes, err := capacity.RoundUp(stored.Spec.Size.Value())
	if err != nil {
		return err
	}
	stored.Status.AllocatedCapacity = *resource.NewQuantity(allocatedBytes, resource.BinarySI)
	stored.Status.NQN = volumeidentity.NQN(identity.ExternalID)
	stored.Status.PortalIP = "192.0.2.10"
	stored.Status.PortalPort = 4420
	return c.Client.Status().Update(ctx, &stored)
}

func (c *immediatelyExportingClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if _, ok := obj.(*storagev1alpha1.NVMeVolumeAttachment); ok && len(obj.GetFinalizers()) != 0 {
		obj.SetFinalizers(nil)
		if err := c.Update(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return c.Client.Delete(ctx, obj, opts...)
}

func newControllerTestServer(t *testing.T) (*ControllerServer, client.Client) {
	t.Helper()
	testScheme := runtime.NewScheme()
	if err := storagev1alpha1.AddToScheme(testScheme); err != nil {
		t.Fatalf("registering DISTORT scheme: %v", err)
	}
	baseClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithStatusSubresource(&storagev1alpha1.NVMePartition{}, &storagev1alpha1.NVMeVolumeAttachment{}).
		Build()
	readyClient := &immediatelyExportingClient{Client: baseClient}
	return &ControllerServer{
		k8sClient:                  readyClient,
		partitionReadyPollInterval: time.Millisecond,
		partitionReadyTimeout:      time.Second,
		attachmentPollInterval:     time.Millisecond,
		attachmentReadyTimeout:     time.Second,
	}, readyClient
}

func mountCapability(mode csipb.VolumeCapability_AccessMode_Mode) *csipb.VolumeCapability {
	return &csipb.VolumeCapability{
		AccessType: &csipb.VolumeCapability_Mount{Mount: &csipb.VolumeCapability_MountVolume{FsType: "ext4"}},
		AccessMode: &csipb.VolumeCapability_AccessMode{Mode: mode},
	}
}

func blockCapability(mode csipb.VolumeCapability_AccessMode_Mode) *csipb.VolumeCapability {
	return &csipb.VolumeCapability{
		AccessType: &csipb.VolumeCapability_Block{Block: &csipb.VolumeCapability_BlockVolume{}},
		AccessMode: &csipb.VolumeCapability_AccessMode{Mode: mode},
	}
}

func validCreateRequest(name, namespace string) *csipb.CreateVolumeRequest {
	return &csipb.CreateVolumeRequest{
		Name: name,
		CapacityRange: &csipb.CapacityRange{
			RequiredBytes: 512 * 1024 * 1024,
		},
		VolumeCapabilities: []*csipb.VolumeCapability{
			mountCapability(csipb.VolumeCapability_AccessMode_SINGLE_NODE_WRITER),
		},
		Parameters: map[string]string{
			"csi.storage.k8s.io/pvc/namespace": namespace,
			"target-backend":                   "spdk",
			"volume-manager":                   "partition",
		},
	}
}

func requireCode(t *testing.T, err error, code codes.Code) {
	t.Helper()
	if status.Code(err) != code {
		t.Fatalf("status code = %s, want %s (error: %v)", status.Code(err), code, err)
	}
}

func TestCreateVolumeValidatesRequiredFields(t *testing.T) {
	server, _ := newControllerTestServer(t)
	tests := []struct {
		name string
		req  *csipb.CreateVolumeRequest
	}{
		{name: "empty name", req: &csipb.CreateVolumeRequest{VolumeCapabilities: []*csipb.VolumeCapability{mountCapability(csipb.VolumeCapability_AccessMode_SINGLE_NODE_WRITER)}}},
		{name: "empty capabilities", req: &csipb.CreateVolumeRequest{Name: "missing-caps"}},
		{name: "unsupported filesystem", req: func() *csipb.CreateVolumeRequest {
			req := validCreateRequest("bad-filesystem", "default")
			req.Parameters[filesystemParameter] = "btrfs"
			return req
		}()},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := server.CreateVolume(context.Background(), test.req)
			requireCode(t, err, codes.InvalidArgument)
		})
	}
}

func TestCreateVolumeCreatesExpectedPartitionAndResponse(t *testing.T) {
	server, k8sClient := newControllerTestServer(t)
	req := validCreateRequest("volume-valid", "team-a")
	req.Parameters["spdk-core-mask"] = testSPDKCoreMask
	req.Parameters["csi.storage.k8s.io/pvc/name"] = "ignored-metadata"

	response, err := server.CreateVolume(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateVolume returned error: %v", err)
	}
	reference, err := volumeidentity.ParseVolumeHandle(response.Volume.VolumeId)
	if err != nil || reference.Namespace != "team-a" || reference.Name != "volume-valid" {
		t.Fatalf("VolumeId = %q, decoded reference = %#v, error = %v", response.Volume.VolumeId, reference, err)
	}
	if response.Volume.CapacityBytes != req.CapacityRange.RequiredBytes {
		t.Fatalf("CapacityBytes = %d, want %d", response.Volume.CapacityBytes, req.CapacityRange.RequiredBytes)
	}
	if response.Volume.VolumeContext[filesystemParameter] != defaultFilesystem || response.Volume.VolumeContext["nqn"] == "" {
		t.Fatalf("unexpected volume context: %#v", response.Volume.VolumeContext)
	}

	var partition storagev1alpha1.NVMePartition
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "volume-valid", Namespace: "team-a"}, &partition); err != nil {
		t.Fatalf("getting created partition: %v", err)
	}
	if partition.Spec.TargetBackend != "spdk" || partition.Spec.VolumeManager != "partition" {
		t.Fatalf("unexpected backend selection: %#v", partition.Spec)
	}
	if partition.Spec.TargetOptions["spdk-core-mask"] != testSPDKCoreMask {
		t.Fatalf("backend option was not forwarded: %#v", partition.Spec.TargetOptions)
	}
	if partition.Spec.AccessMode != csipb.VolumeCapability_AccessMode_SINGLE_NODE_WRITER.String() || partition.Spec.Filesystem != "ext4" || len(partition.Spec.RequestFingerprint) != 64 {
		t.Fatalf("canonical CreateVolume properties were not persisted: %#v", partition.Spec)
	}
	if _, exists := partition.Spec.TargetOptions["csi.storage.k8s.io/pvc/name"]; exists {
		t.Fatalf("CSI metadata leaked into backend options: %#v", partition.Spec.TargetOptions)
	}
}

func TestCreateVolumeRejectsUnsafeSPDKOptionsBeforeCreatingPartition(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "semicolon", key: "spdk-core-mask", value: "0x1; touch /tmp/proof"},
		{name: "command substitution", key: "spdk-core-mask", value: "$(id)"},
		{name: "whitespace", key: "spdk-core-mask", value: "0x1 0x2"},
		{name: "newline", key: "spdk-core-mask", value: "0x1\nid"},
		{name: "flag", key: "spdk-core-mask", value: "--wait-for-rpc"},
		{name: "oversized", key: "spdk-core-mask", value: "0x" + strings.Repeat("f", 257)},
		{name: "unknown option", key: "arbitrary-backend-argument", value: "value"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, k8sClient := newControllerTestServer(t)
			name := "unsafe-" + strings.ReplaceAll(test.name, " ", "-")
			req := validCreateRequest(name, "default")
			req.Parameters[test.key] = test.value

			_, err := server.CreateVolume(context.Background(), req)
			requireCode(t, err, codes.InvalidArgument)

			getErr := k8sClient.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "default"}, &storagev1alpha1.NVMePartition{})
			if !apierrors.IsNotFound(getErr) {
				t.Fatalf("partition was created for rejected options: %v", getErr)
			}
		})
	}
}

func TestCreateVolumeCompatibleRetryIsIdempotent(t *testing.T) {
	server, _ := newControllerTestServer(t)
	req := validCreateRequest("volume-retry", "team-a")
	first, err := server.CreateVolume(context.Background(), req)
	if err != nil {
		t.Fatalf("first CreateVolume: %v", err)
	}
	second, err := server.CreateVolume(context.Background(), req)
	if err != nil {
		t.Fatalf("retry CreateVolume: %v", err)
	}
	if first.Volume.VolumeId != second.Volume.VolumeId || first.Volume.VolumeContext["nqn"] != second.Volume.VolumeContext["nqn"] {
		t.Fatalf("retry identity changed: first=%#v second=%#v", first.Volume, second.Volume)
	}
}

func TestCreateVolumeRejectsInvalidCapacityRanges(t *testing.T) {
	tests := []struct {
		name     string
		required int64
		limit    int64
	}{
		{name: "zero", required: 0},
		{name: "negative", required: -1},
		{name: "negative limit", required: 1, limit: -1},
		{name: "limit below allocation unit", limit: capacity.AllocationUnitBytes - 1},
		{name: "required exceeds limit", required: 2 * 1024 * 1024, limit: 1024 * 1024},
		{name: "rounded allocation exceeds limit", required: 1024*1024 + 1, limit: 1024*1024 + 1},
		{name: "rounding overflows", required: capacity.MaxAllocatableBytes + 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, _ := newControllerTestServer(t)
			req := validCreateRequest("invalid-capacity-"+test.name, "default")
			req.CapacityRange = &csipb.CapacityRange{RequiredBytes: test.required, LimitBytes: test.limit}
			_, err := server.CreateVolume(context.Background(), req)
			requireCode(t, err, codes.InvalidArgument)
		})
	}
}

func TestCreateVolumeDefaultsMissingRangeAndReturnsRoundedCapacity(t *testing.T) {
	tests := []struct {
		name     string
		range_   *csipb.CapacityRange
		wantSize int64
	}{
		{name: "missing range", wantSize: defaultVolumeCapacityBytes},
		{name: "sub MiB", range_: &csipb.CapacityRange{RequiredBytes: 1, LimitBytes: capacity.AllocationUnitBytes}, wantSize: capacity.AllocationUnitBytes},
		{name: "one MiB plus one", range_: &csipb.CapacityRange{RequiredBytes: capacity.AllocationUnitBytes + 1}, wantSize: 2 * capacity.AllocationUnitBytes},
		{name: "limit only below default", range_: &csipb.CapacityRange{LimitBytes: 64 * capacity.AllocationUnitBytes}, wantSize: 64 * capacity.AllocationUnitBytes},
		{name: "limit only rounds down", range_: &csipb.CapacityRange{LimitBytes: capacity.AllocationUnitBytes + 1}, wantSize: capacity.AllocationUnitBytes},
		{name: "limit only above default", range_: &csipb.CapacityRange{LimitBytes: 2 * defaultVolumeCapacityBytes}, wantSize: defaultVolumeCapacityBytes},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, _ := newControllerTestServer(t)
			req := validCreateRequest("rounded-"+strings.ReplaceAll(test.name, " ", "-"), "default")
			req.CapacityRange = test.range_
			response, err := server.CreateVolume(context.Background(), req)
			if err != nil {
				t.Fatalf("CreateVolume returned error: %v", err)
			}
			if response.Volume.CapacityBytes != test.wantSize {
				t.Fatalf("CapacityBytes = %d, want %d", response.Volume.CapacityBytes, test.wantSize)
			}
		})
	}
}

func TestCreateVolumeRejectsIncompatibleRetries(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*csipb.CreateVolumeRequest)
	}{
		{name: "size", mutate: func(req *csipb.CreateVolumeRequest) { req.CapacityRange.RequiredBytes++ }},
		{name: "limit", mutate: func(req *csipb.CreateVolumeRequest) {
			req.CapacityRange.LimitBytes = req.CapacityRange.RequiredBytes + 1024*1024
		}},
		{name: "backend", mutate: func(req *csipb.CreateVolumeRequest) { req.Parameters["target-backend"] = kernelTargetBackend }},
		{name: "filesystem", mutate: func(req *csipb.CreateVolumeRequest) { req.Parameters[filesystemParameter] = xfsFilesystem }},
		{name: "backend option", mutate: func(req *csipb.CreateVolumeRequest) { req.Parameters["spdk-core-mask"] = "0x7" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, _ := newControllerTestServer(t)
			original := validCreateRequest("incompatible-retry", "team-a")
			if _, err := server.CreateVolume(context.Background(), original); err != nil {
				t.Fatalf("initial CreateVolume: %v", err)
			}
			retry := validCreateRequest("incompatible-retry", "team-a")
			test.mutate(retry)
			_, err := server.CreateVolume(context.Background(), retry)
			requireCode(t, err, codes.AlreadyExists)
		})
	}
}

func TestCreateVolumeRejectsUnsupportedCapabilities(t *testing.T) {
	tests := []struct {
		name string
		cap  *csipb.VolumeCapability
	}{
		{name: "raw block", cap: blockCapability(csipb.VolumeCapability_AccessMode_SINGLE_NODE_WRITER)},
		{name: "read only many", cap: mountCapability(csipb.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY)},
		{name: "read write many", cap: mountCapability(csipb.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER)},
		{name: "unsupported mount flags", cap: func() *csipb.VolumeCapability {
			capability := mountCapability(csipb.VolumeCapability_AccessMode_SINGLE_NODE_WRITER)
			capability.GetMount().MountFlags = []string{"noatime"}
			return capability
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, _ := newControllerTestServer(t)
			req := validCreateRequest("unsupported-"+test.name, "default")
			req.VolumeCapabilities = []*csipb.VolumeCapability{test.cap}
			_, err := server.CreateVolume(context.Background(), req)
			requireCode(t, err, codes.InvalidArgument)
		})
	}
	t.Run("conflicting capabilities", func(t *testing.T) {
		server, _ := newControllerTestServer(t)
		req := validCreateRequest("unsupported-conflicting-capabilities", "default")
		xfs := mountCapability(csipb.VolumeCapability_AccessMode_SINGLE_NODE_WRITER)
		xfs.GetMount().FsType = xfsFilesystem
		req.VolumeCapabilities = append(req.VolumeCapabilities, xfs)
		_, err := server.CreateVolume(context.Background(), req)
		requireCode(t, err, codes.InvalidArgument)
	})
}

func TestValidateVolumeCapabilities(t *testing.T) {
	server, k8sClient := newControllerTestServer(t)
	createRequest := validCreateRequest("validated-volume", "team-a")
	createRequest.Parameters["spdk-core-mask"] = testSPDKCoreMask
	created, err := server.CreateVolume(context.Background(), createRequest)
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}
	valid := mountCapability(csipb.VolumeCapability_AccessMode_SINGLE_NODE_WRITER)
	validRequest := &csipb.ValidateVolumeCapabilitiesRequest{
		VolumeId:           created.Volume.VolumeId,
		VolumeCapabilities: []*csipb.VolumeCapability{valid},
		VolumeContext:      created.Volume.VolumeContext,
		Parameters:         createRequest.Parameters,
	}
	response, err := server.ValidateVolumeCapabilities(context.Background(), validRequest)
	if err != nil || response.GetConfirmed() == nil {
		t.Fatalf("ValidateVolumeCapabilities valid response = %#v, error = %v", response, err)
	}
	if len(response.GetConfirmed().GetVolumeCapabilities()) != 1 ||
		!maps.Equal(response.GetConfirmed().GetVolumeContext(), validRequest.VolumeContext) ||
		!maps.Equal(response.GetConfirmed().GetParameters(), validRequest.Parameters) {
		t.Fatalf("confirmed request was not echoed: %#v", response.GetConfirmed())
	}

	response, err = server.ValidateVolumeCapabilities(context.Background(), &csipb.ValidateVolumeCapabilitiesRequest{
		VolumeId: created.Volume.VolumeId,
		VolumeCapabilities: []*csipb.VolumeCapability{
			valid,
			blockCapability(csipb.VolumeCapability_AccessMode_SINGLE_NODE_WRITER),
		},
	})
	if err != nil {
		t.Fatalf("ValidateVolumeCapabilities unsupported request returned RPC error: %v", err)
	}
	if response.GetConfirmed() != nil || response.GetMessage() == "" {
		t.Fatalf("unsupported capabilities were confirmed: %#v", response)
	}

	for name, mutate := range map[string]func(*csipb.ValidateVolumeCapabilitiesRequest){
		"volume context": func(req *csipb.ValidateVolumeCapabilitiesRequest) {
			req.VolumeContext = maps.Clone(req.VolumeContext)
			req.VolumeContext["portalIP"] = "192.0.2.11"
		},
		"filesystem capability": func(req *csipb.ValidateVolumeCapabilitiesRequest) {
			req.VolumeCapabilities = []*csipb.VolumeCapability{mountCapability(csipb.VolumeCapability_AccessMode_SINGLE_NODE_WRITER)}
			req.VolumeCapabilities[0].GetMount().FsType = xfsFilesystem
			req.VolumeContext = nil
			req.Parameters = nil
		},
		"backend parameters": func(req *csipb.ValidateVolumeCapabilitiesRequest) {
			req.Parameters = maps.Clone(req.Parameters)
			req.Parameters["target-backend"] = kernelTargetBackend
		},
		"backend options": func(req *csipb.ValidateVolumeCapabilitiesRequest) {
			req.Parameters = maps.Clone(req.Parameters)
			req.Parameters["spdk-core-mask"] = "0x7"
		},
		"namespace parameter": func(req *csipb.ValidateVolumeCapabilitiesRequest) {
			req.Parameters = maps.Clone(req.Parameters)
			req.Parameters["csi.storage.k8s.io/pvc/namespace"] = "team-b"
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := &csipb.ValidateVolumeCapabilitiesRequest{
				VolumeId:           validRequest.VolumeId,
				VolumeCapabilities: validRequest.VolumeCapabilities,
				VolumeContext:      validRequest.VolumeContext,
				Parameters:         validRequest.Parameters,
			}
			mutate(request)
			response, err := server.ValidateVolumeCapabilities(context.Background(), request)
			if err != nil {
				t.Fatalf("ValidateVolumeCapabilities returned RPC error: %v", err)
			}
			if response.GetConfirmed() != nil || response.GetMessage() == "" {
				t.Fatalf("mismatched request was confirmed: %#v", response)
			}
		})
	}

	_, err = server.ValidateVolumeCapabilities(context.Background(), &csipb.ValidateVolumeCapabilitiesRequest{
		VolumeId:           "not-a-volume-handle",
		VolumeCapabilities: []*csipb.VolumeCapability{valid},
	})
	requireCode(t, err, codes.NotFound)

	reference, err := volumeidentity.ParseVolumeHandle(created.Volume.VolumeId)
	if err != nil {
		t.Fatal(err)
	}
	var partition storagev1alpha1.NVMePartition
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: reference.Namespace, Name: reference.Name}, &partition); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.Delete(context.Background(), &partition); err != nil {
		t.Fatal(err)
	}
	if _, err := server.CreateVolume(context.Background(), validCreateRequest("validated-volume", "team-a")); err != nil {
		t.Fatalf("recreate volume: %v", err)
	}
	_, err = server.ValidateVolumeCapabilities(context.Background(), validRequest)
	requireCode(t, err, codes.NotFound)
}

func TestControllerUnpublishWithEmptyNodeDetachesVolume(t *testing.T) {
	server, k8sClient := newControllerTestServer(t)
	created, err := server.CreateVolume(context.Background(), validCreateRequest("detach-all", "team-a"))
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}
	if _, err := server.ControllerPublishVolume(context.Background(), &csipb.ControllerPublishVolumeRequest{
		VolumeId: created.Volume.VolumeId, NodeId: "consumer-a",
		VolumeCapability: mountCapability(csipb.VolumeCapability_AccessMode_SINGLE_NODE_WRITER),
		VolumeContext:    created.Volume.VolumeContext,
	}); err != nil {
		t.Fatalf("ControllerPublishVolume: %v", err)
	}
	reference, err := volumeidentity.ParseVolumeHandle(created.Volume.VolumeId)
	if err != nil {
		t.Fatal(err)
	}
	key := types.NamespacedName{Namespace: reference.Namespace, Name: attachmentidentity.Name(reference.UID)}
	if _, err := server.ControllerUnpublishVolume(context.Background(), &csipb.ControllerUnpublishVolumeRequest{
		VolumeId: created.Volume.VolumeId,
	}); err != nil {
		t.Fatalf("ControllerUnpublishVolume: %v", err)
	}
	var attachment storagev1alpha1.NVMeVolumeAttachment
	if err := k8sClient.Get(context.Background(), key, &attachment); !apierrors.IsNotFound(err) {
		t.Fatalf("attachment still exists after all-node unpublish: %v", err)
	}
}

func TestControllerAttachmentFencesCompetingNodesAndRequiresExplicitTakeover(t *testing.T) {
	server, k8sClient := newControllerTestServer(t)
	created, err := server.CreateVolume(context.Background(), validCreateRequest("fenced-volume", "team-a"))
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}
	volumeID := created.Volume.VolumeId
	capability := mountCapability(csipb.VolumeCapability_AccessMode_SINGLE_NODE_WRITER)
	publish := func(node string) (*csipb.ControllerPublishVolumeResponse, error) {
		return server.ControllerPublishVolume(context.Background(), &csipb.ControllerPublishVolumeRequest{
			VolumeId: volumeID, NodeId: node, VolumeCapability: capability,
			VolumeContext: created.Volume.VolumeContext,
		})
	}

	first, err := publish("consumer-a")
	if err != nil {
		t.Fatalf("first ControllerPublishVolume: %v", err)
	}
	retry, err := publish("consumer-a")
	if err != nil {
		t.Fatalf("idempotent ControllerPublishVolume: %v", err)
	}
	if retry.PublishContext[publishContextAttachmentID] != first.PublishContext[publishContextAttachmentID] {
		t.Fatalf("idempotent retry changed attachment ID: first=%#v retry=%#v", first.PublishContext, retry.PublishContext)
	}
	_, err = publish("consumer-b")
	requireCode(t, err, codes.FailedPrecondition)

	reference, err := volumeidentity.ParseVolumeHandle(volumeID)
	if err != nil {
		t.Fatal(err)
	}
	key := types.NamespacedName{Namespace: reference.Namespace, Name: attachmentidentity.Name(reference.UID)}
	var attachment storagev1alpha1.NVMeVolumeAttachment
	if err := k8sClient.Get(context.Background(), key, &attachment); err != nil {
		t.Fatal(err)
	}
	base := attachment.DeepCopy()
	if attachment.Annotations == nil {
		attachment.Annotations = map[string]string{}
	}
	attachment.Annotations[attachmentidentity.ForceDetachAnnotation] = "consumer-a"
	if err := k8sClient.Patch(context.Background(), &attachment, client.MergeFrom(base)); err != nil {
		t.Fatal(err)
	}

	second, err := publish("consumer-b")
	if err != nil {
		t.Fatalf("forced ControllerPublishVolume: %v", err)
	}
	if second.PublishContext[publishContextAttachmentID] == first.PublishContext[publishContextAttachmentID] {
		t.Fatal("forced takeover reused the old attachment ID")
	}
	if second.PublishContext[publishContextHostNQN] != hostNQNForNode("consumer-b") {
		t.Fatalf("unexpected replacement host NQN: %#v", second.PublishContext)
	}

	// A delayed unpublish from the stale owner must not release its replacement.
	if _, err := server.ControllerUnpublishVolume(context.Background(), &csipb.ControllerUnpublishVolumeRequest{
		VolumeId: volumeID, NodeId: "consumer-a",
	}); err != nil {
		t.Fatalf("stale ControllerUnpublishVolume: %v", err)
	}
	if err := k8sClient.Get(context.Background(), key, &attachment); err != nil || attachment.Spec.NodeID != "consumer-b" {
		t.Fatalf("stale unpublish removed replacement attachment: attachment=%#v err=%v", attachment.Spec, err)
	}

	if _, err := server.DeleteVolume(context.Background(), &csipb.DeleteVolumeRequest{VolumeId: volumeID}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("DeleteVolume while attached error = %v, want FailedPrecondition", err)
	}
	if _, err := server.ControllerUnpublishVolume(context.Background(), &csipb.ControllerUnpublishVolumeRequest{
		VolumeId: volumeID, NodeId: "consumer-b",
	}); err != nil {
		t.Fatalf("ControllerUnpublishVolume: %v", err)
	}
	if err := k8sClient.Get(context.Background(), key, &attachment); !apierrors.IsNotFound(err) {
		t.Fatalf("attachment still exists after unpublish: %v", err)
	}
}

func TestVolumeIdentityAndDeletionAreNamespaceSafe(t *testing.T) {
	for _, deletionOrder := range [][]string{{"team-a", "team-b"}, {"team-b", "team-a"}} {
		order := strings.Join(deletionOrder, "-then-")
		t.Run(order, func(t *testing.T) {
			server, k8sClient := newControllerTestServer(t)
			volumes := make(map[string]*csipb.CreateVolumeResponse, 2)
			for _, namespace := range []string{"team-a", "team-b"} {
				response, err := server.CreateVolume(context.Background(), validCreateRequest("same-name", namespace))
				if err != nil {
					t.Fatalf("creating %s volume: %v", namespace, err)
				}
				volumes[namespace] = response
			}

			if volumes["team-a"].Volume.VolumeId == volumes["team-b"].Volume.VolumeId {
				t.Fatalf("same-named volumes received identical VolumeId %q", volumes["team-a"].Volume.VolumeId)
			}
			if volumes["team-a"].Volume.VolumeContext["nqn"] == volumes["team-b"].Volume.VolumeContext["nqn"] {
				t.Fatalf("same-named volumes received identical NQN %q", volumes["team-a"].Volume.VolumeContext["nqn"])
			}

			retry, err := server.CreateVolume(context.Background(), validCreateRequest("same-name", "team-a"))
			if err != nil {
				t.Fatalf("retrying team-a volume: %v", err)
			}
			if retry.Volume.VolumeId != volumes["team-a"].Volume.VolumeId {
				t.Fatalf("retry changed VolumeId from %q to %q", volumes["team-a"].Volume.VolumeId, retry.Volume.VolumeId)
			}

			for index, namespace := range deletionOrder {
				volumeID := volumes[namespace].Volume.VolumeId
				if _, err := server.DeleteVolume(context.Background(), &csipb.DeleteVolumeRequest{VolumeId: volumeID}); err != nil {
					t.Fatalf("deleting %s volume: %v", namespace, err)
				}
				if _, err := server.DeleteVolume(context.Background(), &csipb.DeleteVolumeRequest{VolumeId: volumeID}); err != nil {
					t.Fatalf("repeating deletion of %s volume: %v", namespace, err)
				}
				var deleted storagev1alpha1.NVMePartition
				err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "same-name", Namespace: namespace}, &deleted)
				if !apierrors.IsNotFound(err) {
					t.Fatalf("deleted partition %s/same-name still exists: %v", namespace, err)
				}
				if index == 0 {
					otherNamespace := deletionOrder[1]
					var other storagev1alpha1.NVMePartition
					if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "same-name", Namespace: otherNamespace}, &other); err != nil {
						t.Fatalf("deleting %s removed %s/same-name: %v", namespace, otherNamespace, err)
					}
				}
			}
		})
	}
}

func TestDeleteVolumeDoesNotDeleteRecreatedPartition(t *testing.T) {
	server, k8sClient := newControllerTestServer(t)
	original, err := server.CreateVolume(context.Background(), validCreateRequest("recreated", "team-a"))
	if err != nil {
		t.Fatalf("creating original volume: %v", err)
	}
	var partition storagev1alpha1.NVMePartition
	key := types.NamespacedName{Name: "recreated", Namespace: "team-a"}
	if err := k8sClient.Get(context.Background(), key, &partition); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.Delete(context.Background(), &partition); err != nil {
		t.Fatal(err)
	}
	replacement, err := server.CreateVolume(context.Background(), validCreateRequest("recreated", "team-a"))
	if err != nil {
		t.Fatalf("creating replacement volume: %v", err)
	}
	if replacement.Volume.VolumeId == original.Volume.VolumeId {
		t.Fatalf("recreated partition reused old VolumeId %q", original.Volume.VolumeId)
	}
	if _, err := server.DeleteVolume(context.Background(), &csipb.DeleteVolumeRequest{VolumeId: original.Volume.VolumeId}); err != nil {
		t.Fatalf("deleting old volume handle: %v", err)
	}
	if err := k8sClient.Get(context.Background(), key, &partition); err != nil {
		t.Fatalf("old volume handle deleted the replacement: %v", err)
	}
}

func TestLegacyDeleteVolumeRefusesAmbiguity(t *testing.T) {
	server, k8sClient := newControllerTestServer(t)
	for _, namespace := range []string{"team-a", "team-b"} {
		partition := &storagev1alpha1.NVMePartition{
			ObjectMeta: metav1.ObjectMeta{Name: "legacy-name", Namespace: namespace, UID: uuid.NewUUID()},
			Spec:       storagev1alpha1.NVMePartitionSpec{Size: resource.MustParse("64Mi")},
			Status: storagev1alpha1.NVMePartitionStatus{
				State:      storagev1alpha1.NVMePartitionStateExported,
				ExternalID: "legacy-name",
				NQN:        volumeidentity.NQN("legacy-name"),
			},
		}
		if err := k8sClient.Create(context.Background(), partition); err != nil {
			t.Fatal(err)
		}
		var stored storagev1alpha1.NVMePartition
		if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "legacy-name", Namespace: namespace}, &stored); err != nil {
			t.Fatal(err)
		}
		stored.Status.ExternalID = "legacy-name"
		stored.Status.NQN = volumeidentity.NQN("legacy-name")
		if err := k8sClient.Status().Update(context.Background(), &stored); err != nil {
			t.Fatal(err)
		}
	}

	_, err := server.DeleteVolume(context.Background(), &csipb.DeleteVolumeRequest{VolumeId: "legacy-name"})
	requireCode(t, err, codes.FailedPrecondition)
	for _, namespace := range []string{"team-a", "team-b"} {
		var partition storagev1alpha1.NVMePartition
		if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "legacy-name", Namespace: namespace}, &partition); err != nil {
			t.Fatalf("ambiguous legacy delete removed %s/legacy-name: %v", namespace, err)
		}
	}
}

func TestLegacyDeleteVolumeDeletesOneUnambiguousPartition(t *testing.T) {
	server, k8sClient := newControllerTestServer(t)
	partition := &storagev1alpha1.NVMePartition{
		ObjectMeta: metav1.ObjectMeta{Name: "legacy-name", Namespace: "team-a", UID: uuid.NewUUID()},
		Spec:       storagev1alpha1.NVMePartitionSpec{Size: resource.MustParse("64Mi")},
	}
	if err := k8sClient.Create(context.Background(), partition); err != nil {
		t.Fatal(err)
	}
	var stored storagev1alpha1.NVMePartition
	key := types.NamespacedName{Name: "legacy-name", Namespace: "team-a"}
	if err := k8sClient.Get(context.Background(), key, &stored); err != nil {
		t.Fatal(err)
	}
	stored.Status.ExternalID = "legacy-name"
	stored.Status.NQN = volumeidentity.NQN("legacy-name")
	if err := k8sClient.Status().Update(context.Background(), &stored); err != nil {
		t.Fatal(err)
	}

	if _, err := server.DeleteVolume(context.Background(), &csipb.DeleteVolumeRequest{VolumeId: "legacy-name"}); err != nil {
		t.Fatalf("deleting unambiguous legacy volume: %v", err)
	}
	var deleted storagev1alpha1.NVMePartition
	err := k8sClient.Get(context.Background(), key, &deleted)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("legacy partition still exists: %v", err)
	}
}

func TestCreateVolumeRejectsUnimplementedManagers(t *testing.T) {
	tests := []struct {
		name    string
		backend string
		manager string
	}{
		{name: "lvm manager", backend: "spdk", manager: "lvm"},
		{name: "unknown manager", backend: kernelTargetBackend, manager: "zfs"},
		{name: "unknown backend", backend: "userspace", manager: "partition"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, _ := newControllerTestServer(t)
			req := validCreateRequest("unsupported-configuration", "default")
			req.Parameters["target-backend"] = tt.backend
			req.Parameters["volume-manager"] = tt.manager
			_, err := server.CreateVolume(context.Background(), req)
			requireCode(t, err, codes.InvalidArgument)
		})
	}
}
