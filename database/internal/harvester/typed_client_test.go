package harvester

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	harvesterhciov1beta1 "github.com/harvester/harvester/pkg/apis/harvesterhci.io/v1beta1"
	harvesterfake "github.com/harvester/harvester/pkg/generated/clientset/versioned/fake"
	harvesterutil "github.com/harvester/harvester/pkg/util"
	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	kubevirtv1 "kubevirt.io/api/core/v1"
	kvfake "kubevirt.io/client-go/kubevirt/fake"
)

// mgmtNetInterface is the legacy dynamic-client's dial-probe NIC name
// (removed in PR9 along with DialVMListener) — kept here only so the tests
// below can assert it never appears on a TypedClient-built VM.
const mgmtNetInterface = "mgmt-net"

func testVMCreateParams() VMCreateParams {
	return VMCreateParams{
		ID:                     "orders",
		Namespace:              "tenant-a",
		CPUCores:               2,
		MemoryMB:               4096,
		OSImage:                "ubuntu-22.04",
		DataVolumeRef:          "pg-orders-data",
		DataVolumeSizeGB:       20,
		DataVolumeStorageClass: "harvester-longhorn",
		NADName:                "tenant-a/vm-network",
		MasterUser:             "dbadmin",
		Port:                   5432,
		CloudInitSecretName:    "pg-orders-cloudinit",
	}
}

func TestTypedCreatePostgresVMFailsOnImageResolutionFailure(t *testing.T) {
	ctx := context.Background()
	client := newTestTypedClient()

	vmName, err := client.CreatePostgresVM(ctx, testVMCreateParams())
	if err == nil {
		t.Fatalf("CreatePostgresVM returned nil error, want image resolution error")
	}
	if _, err := client.Clientset.KubevirtV1().VirtualMachines("tenant-a").Get(ctx, vmName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("VM should not exist after image-resolution failure, got: %v", err)
	}
}

func TestDataVolumeNameMatchesPVCTemplateAndResizeUpdatesVMTemplate(t *testing.T) {
	ctx := context.Background()
	client := newTestTypedClient(testTypedVMImage())

	dvName := DataVolumeName("orders")
	if dvName != "pg-orders-data" {
		t.Fatalf("DataVolume name = %q, want pg-orders-data", dvName)
	}
	params := testVMCreateParams()
	params.DataVolumeRef = dvName
	if _, err := client.CreatePostgresVM(ctx, params); err != nil {
		t.Fatalf("CreatePostgresVM returned error: %v", err)
	}
	vm, err := client.Clientset.KubevirtV1().VirtualMachines("tenant-a").Get(ctx, "pg-orders", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get VM: %v", err)
	}
	templates, err := VolumeClaimTemplates(vm)
	if err != nil {
		t.Fatalf("volume claim templates: %v", err)
	}
	dataTemplate := findPVCTemplate(templates, dvName)
	if dataTemplate == nil {
		t.Fatalf("data PVC template %s not found", dvName)
	}
	storage := dataTemplate.Spec.Resources.Requests[corev1.ResourceStorage]
	if got := storage.String(); got != "20Gi" {
		t.Fatalf("Data PVC template storage = %q, want 20Gi", got)
	}

	if err := client.ResizeDataVolume(ctx, "tenant-a", "pg-orders", dvName, 30); err != nil {
		t.Fatalf("ResizeDataVolume returned error: %v", err)
	}
	vm, err = client.Clientset.KubevirtV1().VirtualMachines("tenant-a").Get(ctx, "pg-orders", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get resized VM: %v", err)
	}
	templates, err = VolumeClaimTemplates(vm)
	if err != nil {
		t.Fatalf("resized volume claim templates: %v", err)
	}
	dataTemplate = findPVCTemplate(templates, dvName)
	if dataTemplate == nil {
		t.Fatalf("data PVC template %s not found after resize", dvName)
	}
	storage = dataTemplate.Spec.Resources.Requests[corev1.ResourceStorage]
	if got := storage.String(); got != "30Gi" {
		t.Fatalf("Data PVC template storage after resize = %q, want 30Gi", got)
	}
}

// CreatePostgresVM no longer generates credentials/cloud-init (PR8 — that
// moved to internal/credentials + internal/resource; the reuse-on-reentry
// invariant is now tested there). It only builds the VM against an
// already-provisioned cloud-init Secret name.
func TestTypedCreatePostgresVMUsesSuppliedCloudInitSecret(t *testing.T) {
	ctx := context.Background()
	client := newTestTypedClient(testTypedVMImage())
	params := testVMCreateParams()

	vmName, err := client.CreatePostgresVM(ctx, params)
	if err != nil {
		t.Fatalf("CreatePostgresVM returned error: %v", err)
	}
	if vmName != "pg-orders" {
		t.Fatalf("VM name = %q, want pg-orders", vmName)
	}

	vm, err := client.Clientset.KubevirtV1().VirtualMachines("tenant-a").Get(ctx, vmName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get created VM: %v", err)
	}
	found := false
	for _, v := range vm.Spec.Template.Spec.Volumes {
		if v.Name != "cloudinit" || v.CloudInitNoCloud == nil {
			continue
		}
		found = true
		if v.CloudInitNoCloud.UserDataSecretRef == nil || v.CloudInitNoCloud.UserDataSecretRef.Name != params.CloudInitSecretName {
			t.Fatalf("UserDataSecretRef = %+v, want %q", v.CloudInitNoCloud.UserDataSecretRef, params.CloudInitSecretName)
		}
		if v.CloudInitNoCloud.NetworkDataSecretRef == nil || v.CloudInitNoCloud.NetworkDataSecretRef.Name != params.CloudInitSecretName {
			t.Fatalf("NetworkDataSecretRef = %+v, want %q", v.CloudInitNoCloud.NetworkDataSecretRef, params.CloudInitSecretName)
		}
	}
	if !found {
		t.Fatal("cloudinit volume not found on VM")
	}
}

func TestTypedCreatePostgresVMPreservesVMShape(t *testing.T) {
	ctx := context.Background()
	client := newTestTypedClient(testTypedVMImage())
	client.MgmtLogicalSwitch = "ovn-default"
	params := testVMCreateParams()
	params.DNSServerIP = "10.96.0.10/32"

	vmName, err := client.CreatePostgresVM(ctx, params)
	if err != nil {
		t.Fatalf("CreatePostgresVM returned error: %v", err)
	}
	cloudInitSecretName := params.CloudInitSecretName
	vm, err := client.Clientset.KubevirtV1().VirtualMachines("tenant-a").Get(ctx, vmName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get created VM: %v", err)
	}

	if vm.Spec.Template.ObjectMeta.Annotations["ovn.kubernetes.io/logical_switch"] != "ovn-default" {
		t.Fatalf("logical switch annotation = %q, want ovn-default", vm.Spec.Template.ObjectMeta.Annotations["ovn.kubernetes.io/logical_switch"])
	}
	if vm.Spec.Template.Spec.DNSPolicy != corev1.DNSNone {
		t.Fatalf("dnsPolicy = %q, want None", vm.Spec.Template.Spec.DNSPolicy)
	}
	if vm.Spec.Template.Spec.Domain.Memory != nil && vm.Spec.Template.Spec.Domain.Memory.Guest != nil {
		t.Fatalf("memory.guest is set before Harvester admission: %s", vm.Spec.Template.Spec.Domain.Memory.Guest.String())
	}
	memoryLimit := vm.Spec.Template.Spec.Domain.Resources.Limits[corev1.ResourceMemory]
	if got := memoryLimit.String(); got != "4Gi" {
		t.Fatalf("memory limit = %q, want 4Gi", got)
	}
	if got := vm.Spec.Template.Spec.DNSConfig.Nameservers; len(got) != 1 || got[0] != "10.96.0.10" {
		t.Fatalf("nameservers = %v, want [10.96.0.10]", got)
	}
	if got := len(vm.Spec.DataVolumeTemplates); got != 0 {
		t.Fatalf("dataVolumeTemplates = %d, want 0 for Harvester-native volumeClaimTemplates path", got)
	}
	templates, err := VolumeClaimTemplates(vm)
	if err != nil {
		t.Fatalf("volume claim templates: %v", err)
	}
	if got := len(templates); got != 2 {
		t.Fatalf("volume claim template count = %d, want 2", got)
	}
	osTemplate := findPVCTemplate(templates, "pg-orders-os")
	if osTemplate == nil {
		t.Fatalf("OS PVC template not found")
	}
	if got := osTemplate.Annotations["harvesterhci.io/imageId"]; got != "default/ubuntu-22.04" {
		t.Fatalf("OS image annotation = %q, want default/ubuntu-22.04", got)
	}
	dataTemplate := findPVCTemplate(templates, "pg-orders-data")
	if dataTemplate == nil {
		t.Fatalf("data PVC template not found")
	}
	if got := dataTemplate.Annotations["harvesterhci.io/imageId"]; got != "" {
		t.Fatalf("data PVC image annotation = %q, want empty", got)
	}
	if dataTemplate.Spec.StorageClassName == nil || *dataTemplate.Spec.StorageClassName != "harvester-longhorn" {
		t.Fatalf("data PVC storageClass = %#v, want harvester-longhorn", dataTemplate.Spec.StorageClassName)
	}
	if !vmVolumeUsesPVC(vm, "os-disk", "pg-orders-os") {
		t.Fatalf("os-disk volume does not use PVC pg-orders-os")
	}
	if !vmVolumeUsesPVC(vm, "pgdata-disk", "pg-orders-data") {
		t.Fatalf("pgdata-disk volume does not use PVC pg-orders-data")
	}
	// The VM must have only the data-net interface — mgmt-net (masquerade)
	// is removed; the readiness probe uses the QGA virtio channel instead.
	if vmHasInterface(vm, mgmtNetInterface) {
		t.Fatalf("mgmt-net interface must not be attached to the VM")
	}

	// Readiness probe must be configured as an exec probe via the guest agent.
	probe := vm.Spec.Template.Spec.ReadinessProbe
	if probe == nil {
		t.Fatalf("ReadinessProbe is not set")
	}
	if probe.Exec == nil {
		t.Fatalf("ReadinessProbe.Exec is not set")
	}
	if !strings.Contains(strings.Join(probe.Exec.Command, " "), "pg_isready") {
		t.Fatalf("ReadinessProbe command does not contain pg_isready: %v", probe.Exec.Command)
	}
	if probe.InitialDelaySeconds != 30 || probe.PeriodSeconds != 10 || probe.FailureThreshold != 12 {
		t.Fatalf("ReadinessProbe timing initial=%d period=%d failure=%d, want 30/10/12",
			probe.InitialDelaySeconds, probe.PeriodSeconds, probe.FailureThreshold)
	}

	raw, err := json.Marshal(vm)
	if err != nil {
		t.Fatalf("marshal VM: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, `"secretRef":{"name":"`+cloudInitSecretName+`"}`) {
		t.Fatalf("VM JSON does not contain cloud-init secretRef: %s", body)
	}
	if !strings.Contains(body, `"networkDataSecretRef":{"name":"`+cloudInitSecretName+`"}`) {
		t.Fatalf("VM JSON does not contain cloud-init networkDataSecretRef: %s", body)
	}

	// RunStrategy must be Always so the VMI restarts after any exit (clean or crash).
	// RerunOnFailure would leave the VM permanently stopped after a clean guest shutdown,
	// which can happen during cloud-init or after a cold resize restart.
	if vm.Spec.RunStrategy == nil || *vm.Spec.RunStrategy != kubevirtv1.RunStrategyAlways {
		t.Fatalf("RunStrategy = %v, want Always", vm.Spec.RunStrategy)
	}
	// AnnotationRunStrategy must match so Harvester's patchRunStrategy webhook confirms
	// Always on every Halted→non-Halted transition instead of overriding to RerunOnFailure.
	if got := vm.Annotations[harvesterutil.AnnotationRunStrategy]; got != string(kubevirtv1.RunStrategyAlways) {
		t.Fatalf("AnnotationRunStrategy = %q, want %q", got, string(kubevirtv1.RunStrategyAlways))
	}
}

func TestTypedGetVMIReadinessUsesOnlyDataNetIP(t *testing.T) {
	ctx := context.Background()
	client := newTestTypedClient(&kubevirtv1.VirtualMachineInstance{
		TypeMeta:   metav1.TypeMeta{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachineInstance"},
		ObjectMeta: metav1.ObjectMeta{Name: "pg-orders", Namespace: "tenant-a"},
		Status: kubevirtv1.VirtualMachineInstanceStatus{
			Phase: kubevirtv1.Running,
			Interfaces: []kubevirtv1.VirtualMachineInstanceNetworkInterface{
				{Name: mgmtNetInterface, IP: "10.244.0.10"},
				{Name: dataNetInterface, IP: "192.168.40.50"},
			},
		},
	})

	readiness, err := client.GetVMIReadiness(ctx, "tenant-a", "pg-orders")
	if err != nil {
		t.Fatalf("GetVMIReadiness returned error: %v", err)
	}
	if !readiness.Running || readiness.IP != "192.168.40.50" {
		t.Fatalf("readiness = %+v, want running with data-net IP", readiness)
	}
}

func TestTypedGetVMIReadinessDoesNotFallbackToMgmtNet(t *testing.T) {
	ctx := context.Background()
	client := newTestTypedClient(&kubevirtv1.VirtualMachineInstance{
		TypeMeta:   metav1.TypeMeta{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachineInstance"},
		ObjectMeta: metav1.ObjectMeta{Name: "pg-orders", Namespace: "tenant-a"},
		Status: kubevirtv1.VirtualMachineInstanceStatus{
			Phase:      kubevirtv1.Running,
			Interfaces: []kubevirtv1.VirtualMachineInstanceNetworkInterface{{Name: mgmtNetInterface, IP: "10.244.0.10"}},
		},
	})

	readiness, err := client.GetVMIReadiness(ctx, "tenant-a", "pg-orders")
	if err != nil {
		t.Fatalf("GetVMIReadiness returned error: %v", err)
	}
	if !readiness.Running || readiness.IP != "" {
		t.Fatalf("readiness = %+v, want running with no IP fallback", readiness)
	}
}

func TestTypedGetVMIReadinessSurfacesConditionsAndUID(t *testing.T) {
	ctx := context.Background()
	client := newTestTypedClient(&kubevirtv1.VirtualMachineInstance{
		TypeMeta:   metav1.TypeMeta{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachineInstance"},
		ObjectMeta: metav1.ObjectMeta{Name: "pg-orders", Namespace: "tenant-a", UID: "abc-123"},
		Status: kubevirtv1.VirtualMachineInstanceStatus{
			Phase: kubevirtv1.Running,
			Interfaces: []kubevirtv1.VirtualMachineInstanceNetworkInterface{
				{Name: dataNetInterface, IP: "192.168.40.50"},
			},
			Conditions: []kubevirtv1.VirtualMachineInstanceCondition{
				{Type: kubevirtv1.VirtualMachineInstanceReady, Status: corev1.ConditionTrue},
				{Type: kubevirtv1.VirtualMachineInstanceAgentConnected, Status: corev1.ConditionTrue},
			},
		},
	})

	r, err := client.GetVMIReadiness(ctx, "tenant-a", "pg-orders")
	if err != nil {
		t.Fatalf("GetVMIReadiness returned error: %v", err)
	}
	if !r.Running {
		t.Fatalf("Running = false, want true")
	}
	if r.IP != "192.168.40.50" {
		t.Fatalf("IP = %q, want 192.168.40.50", r.IP)
	}
	if !r.Ready {
		t.Fatalf("Ready = false, want true")
	}
	if !r.AgentConnected {
		t.Fatalf("AgentConnected = false, want true")
	}
	if r.VMIUID != "abc-123" {
		t.Fatalf("VMIUID = %q, want abc-123", r.VMIUID)
	}
}

func TestTypedGetVMIReadinessConditionsDefaultToFalseWhenAbsent(t *testing.T) {
	ctx := context.Background()
	client := newTestTypedClient(&kubevirtv1.VirtualMachineInstance{
		TypeMeta:   metav1.TypeMeta{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachineInstance"},
		ObjectMeta: metav1.ObjectMeta{Name: "pg-orders", Namespace: "tenant-a", UID: "xyz-456"},
		Status: kubevirtv1.VirtualMachineInstanceStatus{
			Phase: kubevirtv1.Running,
			// No conditions set — VMI booting, probes not yet evaluated.
		},
	})

	r, err := client.GetVMIReadiness(ctx, "tenant-a", "pg-orders")
	if err != nil {
		t.Fatalf("GetVMIReadiness returned error: %v", err)
	}
	if r.Ready || r.AgentConnected {
		t.Fatalf("Ready=%v AgentConnected=%v, want both false when conditions absent", r.Ready, r.AgentConnected)
	}
	if r.VMIUID != "xyz-456" {
		t.Fatalf("VMIUID = %q, want xyz-456", r.VMIUID)
	}
}

func TestTypedStartStopAndResizeVM(t *testing.T) {
	ctx := context.Background()
	runStrategy := kubevirtv1.RunStrategyRerunOnFailure
	client := newTestTypedClient(&kubevirtv1.VirtualMachine{
		TypeMeta:   metav1.TypeMeta{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine"},
		ObjectMeta: metav1.ObjectMeta{Name: "pg-orders", Namespace: "tenant-a"},
		Spec: kubevirtv1.VirtualMachineSpec{
			RunStrategy: &runStrategy,
			Template: &kubevirtv1.VirtualMachineInstanceTemplateSpec{
				Spec: kubevirtv1.VirtualMachineInstanceSpec{
					Domain: kubevirtv1.DomainSpec{
						CPU:    &kubevirtv1.CPU{Cores: 2},
						Memory: &kubevirtv1.Memory{},
					},
				},
			},
		},
	})

	// StopVM: sets RunStrategy = Halted (spec mutation, matches Harvester's stop pattern)
	if err := client.StopVM(ctx, "tenant-a", "pg-orders"); err != nil {
		t.Fatalf("StopVM returned error: %v", err)
	}
	vm, _ := client.Clientset.KubevirtV1().VirtualMachines("tenant-a").Get(ctx, "pg-orders", metav1.GetOptions{})
	if vm.Spec.RunStrategy == nil || *vm.Spec.RunStrategy != kubevirtv1.RunStrategyHalted {
		t.Fatalf("RunStrategy after StopVM = %v, want Halted", vm.Spec.RunStrategy)
	}
	if vm.Spec.Running != nil {
		t.Fatalf("Running after StopVM = %v, want nil", vm.Spec.Running)
	}

	// StartVM: calls the KubeVirt start subresource API (does not mutate spec)
	if err := client.StartVM(ctx, "tenant-a", "pg-orders"); err != nil {
		t.Fatalf("StartVM returned error: %v", err)
	}

	if err := client.ResizeVM(ctx, "tenant-a", "pg-orders", 4, 8192); err != nil {
		t.Fatalf("ResizeVM returned error: %v", err)
	}
	vm, _ = client.Clientset.KubevirtV1().VirtualMachines("tenant-a").Get(ctx, "pg-orders", metav1.GetOptions{})
	cpuLimit := vm.Spec.Template.Spec.Domain.Resources.Limits[corev1.ResourceCPU]
	memLimit := vm.Spec.Template.Spec.Domain.Resources.Limits[corev1.ResourceMemory]
	if vm.Spec.Template.Spec.Domain.CPU.Cores != 4 || cpuLimit.Cmp(*resource.NewQuantity(4, resource.DecimalSI)) != 0 || memLimit.Cmp(resource.MustParse("8192Mi")) != 0 {
		t.Fatalf("resized CPU/memory = cores:%d cpuLimit:%s memLimit:%s, want cores:4 cpuLimit:4 memLimit:8192Mi",
			vm.Spec.Template.Spec.Domain.CPU.Cores, cpuLimit.String(), memLimit.String())
	}
}

func TestTypedTeardownIgnoresNotFound(t *testing.T) {
	ctx := context.Background()
	client := newTestTypedClient()
	err := client.TeardownAll(ctx, "orders", "tenant-a", dbaasv1.ResourceRefs{
		VMName:              "pg-orders",
		DataVolumeName:      "pg-orders-data",
		SecretName:          "pg-orders-credentials",
		CloudInitSecretName: "pg-orders-cloudinit",
		MetricsServiceName:  "pg-orders-metrics",
		ServiceMonitor:      "pg-orders-monitor",
	})
	if err != nil {
		t.Fatalf("TeardownAll returned error for missing resources: %v", err)
	}
}

func TestTypedRemoveCloudInitDiskStripsEntries(t *testing.T) {
	ctx := context.Background()
	vm := &kubevirtv1.VirtualMachine{
		TypeMeta:   metav1.TypeMeta{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine"},
		ObjectMeta: metav1.ObjectMeta{Name: "pg-orders", Namespace: "tenant-a"},
		Spec: kubevirtv1.VirtualMachineSpec{
			Template: &kubevirtv1.VirtualMachineInstanceTemplateSpec{
				Spec: kubevirtv1.VirtualMachineInstanceSpec{
					Domain: kubevirtv1.DomainSpec{
						Devices: kubevirtv1.Devices{
							Disks: []kubevirtv1.Disk{
								{Name: "os-disk"},
								{Name: "cloudinit"},
							},
						},
					},
					Volumes: []kubevirtv1.Volume{
						{Name: "os-disk"},
						{Name: "cloudinit", VolumeSource: kubevirtv1.VolumeSource{
							CloudInitNoCloud: &kubevirtv1.CloudInitNoCloudSource{},
						}},
					},
				},
			},
		},
	}
	client := newTestTypedClient(vm)

	if err := client.RemoveCloudInitDisk(ctx, "tenant-a", "pg-orders"); err != nil {
		t.Fatalf("RemoveCloudInitDisk returned error: %v", err)
	}

	updated, err := client.Clientset.KubevirtV1().VirtualMachines("tenant-a").Get(ctx, "pg-orders", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get updated VM: %v", err)
	}
	for _, d := range updated.Spec.Template.Spec.Domain.Devices.Disks {
		if d.Name == "cloudinit" {
			t.Fatalf("cloudinit disk still present after RemoveCloudInitDisk")
		}
	}
	for _, v := range updated.Spec.Template.Spec.Volumes {
		if v.Name == "cloudinit" {
			t.Fatalf("cloudinit volume still present after RemoveCloudInitDisk")
		}
	}
	if len(updated.Spec.Template.Spec.Domain.Devices.Disks) != 1 || updated.Spec.Template.Spec.Domain.Devices.Disks[0].Name != "os-disk" {
		t.Fatalf("disks after removal = %v, want only [os-disk]", updated.Spec.Template.Spec.Domain.Devices.Disks)
	}
}

func TestTypedRemoveCloudInitDiskIsIdempotent(t *testing.T) {
	ctx := context.Background()
	vm := &kubevirtv1.VirtualMachine{
		TypeMeta:   metav1.TypeMeta{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine"},
		ObjectMeta: metav1.ObjectMeta{Name: "pg-orders", Namespace: "tenant-a"},
		Spec: kubevirtv1.VirtualMachineSpec{
			Template: &kubevirtv1.VirtualMachineInstanceTemplateSpec{
				Spec: kubevirtv1.VirtualMachineInstanceSpec{
					Domain: kubevirtv1.DomainSpec{Devices: kubevirtv1.Devices{
						Disks: []kubevirtv1.Disk{{Name: "os-disk"}},
					}},
					Volumes: []kubevirtv1.Volume{{Name: "os-disk"}},
				},
			},
		},
	}
	client := newTestTypedClient(vm)

	// cloudinit disk is already absent — second call must not error
	if err := client.RemoveCloudInitDisk(ctx, "tenant-a", "pg-orders"); err != nil {
		t.Fatalf("RemoveCloudInitDisk on already-clean VM returned error: %v", err)
	}
}

func TestTypedTeardownAggregatesDeleteErrors(t *testing.T) {
	ctx := context.Background()
	client := newTestTypedClient(&kubevirtv1.VirtualMachine{
		TypeMeta:   metav1.TypeMeta{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine"},
		ObjectMeta: metav1.ObjectMeta{Name: "pg-orders", Namespace: "tenant-a"},
	})
	client.Clientset.(*harvesterfake.Clientset).PrependReactor("delete", "virtualmachines", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, &apierrors.StatusError{ErrStatus: metav1.Status{Reason: metav1.StatusReasonForbidden, Message: "blocked"}}
	})

	err := client.TeardownAll(ctx, "orders", "tenant-a", dbaasv1.ResourceRefs{VMName: "pg-orders"})
	if err == nil {
		t.Fatalf("TeardownAll returned nil error, want aggregate")
	}
	if !strings.Contains(err.Error(), "virtualmachines/pg-orders") || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("aggregate error = %q", err.Error())
	}
}

func newTestTypedClient(objs ...runtime.Object) *TypedClient {
	return NewTypedClientWithClientsets(harvesterfake.NewSimpleClientset(objs...), kubefake.NewSimpleClientset(), kvfake.NewSimpleClientset(), "")
}

func findPVCTemplate(pvcs []*corev1.PersistentVolumeClaim, name string) *corev1.PersistentVolumeClaim {
	for _, pvc := range pvcs {
		if pvc.Name == name {
			return pvc
		}
	}
	return nil
}

func vmVolumeUsesPVC(vm *kubevirtv1.VirtualMachine, volumeName, claimName string) bool {
	for _, volume := range vm.Spec.Template.Spec.Volumes {
		if volume.Name == volumeName && volume.PersistentVolumeClaim != nil && volume.PersistentVolumeClaim.ClaimName == claimName {
			return true
		}
	}
	return false
}

func vmHasInterface(vm *kubevirtv1.VirtualMachine, interfaceName string) bool {
	for _, iface := range vm.Spec.Template.Spec.Domain.Devices.Interfaces {
		if iface.Name == interfaceName {
			return true
		}
	}
	return false
}

func vmInterfaceHasPort(vm *kubevirtv1.VirtualMachine, interfaceName string, port int32) bool {
	for _, iface := range vm.Spec.Template.Spec.Domain.Devices.Interfaces {
		if iface.Name != interfaceName {
			continue
		}
		for _, ifacePort := range iface.Ports {
			if ifacePort.Port == port && ifacePort.Protocol == "TCP" {
				return true
			}
		}
	}
	return false
}

func testTypedVMImage() *harvesterhciov1beta1.VirtualMachineImage {
	return &harvesterhciov1beta1.VirtualMachineImage{
		TypeMeta:   metav1.TypeMeta{APIVersion: "harvesterhci.io/v1beta1", Kind: "VirtualMachineImage"},
		ObjectMeta: metav1.ObjectMeta{Name: "ubuntu-22.04", Namespace: "default"},
		Spec:       harvesterhciov1beta1.VirtualMachineImageSpec{DisplayName: "Ubuntu 22.04"},
		Status: harvesterhciov1beta1.VirtualMachineImageStatus{
			StorageClassName: "longhorn-image-ubuntu",
			Conditions: []harvesterhciov1beta1.Condition{
				{Type: harvesterhciov1beta1.ImageImported, Status: corev1.ConditionTrue},
			},
		},
	}
}
