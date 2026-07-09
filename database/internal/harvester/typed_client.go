/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package harvester

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	harvesterbuilder "github.com/harvester/harvester/pkg/builder"
	"github.com/harvester/harvester/pkg/util"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	kubevirtv1 "kubevirt.io/api/core/v1"

	harvesterhciov1beta1 "github.com/harvester/harvester/pkg/apis/harvesterhci.io/v1beta1"
	harvesterclientset "github.com/harvester/harvester/pkg/generated/clientset/versioned"
	cdiclientset "kubevirt.io/client-go/containerizeddataimporter"
	kvclientset "kubevirt.io/client-go/kubevirt"
)

// TypedClient manages Harvester resources through Harvester's generated
// clientset and standard Kubernetes typed clients.
type TypedClient struct {
	Clientset         harvesterclientset.Interface
	KubeClient        kubernetes.Interface
	KvClientset       kvclientset.Interface
	CdiClientset      cdiclientset.Interface
	GrafanaURL        string
	MgmtLogicalSwitch string
}

// credentialMaterial is the per-instance secret material shared by the
// credentials Secret (user-facing) and the cloud-init bootstrap (what the VM
// is actually provisioned with). Both must come from the same generation, so
// CreatePostgresVM derives them from one source — see ensureCredentialsSecret.
type credentialMaterial struct {
	adminPw    string
	replPw     string
	exporterPw string
	tls        *TLSBundle
}

var _ ClientInterface = (*TypedClient)(nil)

func NewTypedClient(config *rest.Config, grafanaURL string) (*TypedClient, error) {
	clientset, err := harvesterclientset.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	kvClientset, err := kvclientset.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	cdiClient, err := cdiclientset.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return NewTypedClientWithClientsets(clientset, kubeClient, kvClientset, cdiClient, grafanaURL), nil
}

func NewTypedClientWithClientsets(clientset harvesterclientset.Interface, kubeClient kubernetes.Interface, kvClientset kvclientset.Interface, cdiClient cdiclientset.Interface, grafanaURL string) *TypedClient {
	return &TypedClient{Clientset: clientset, KubeClient: kubeClient, KvClientset: kvClientset, CdiClientset: cdiClient, GrafanaURL: grafanaURL}
}

func (c *TypedClient) CreateDataVolume(ctx context.Context, id, ns string, sizeGB int, storageClass string) (string, error) {
	dvName := fmt.Sprintf("pg-%s-data", id)
	// In the Harvester-owned storage path, this phase reserves the deterministic
	// data disk PVC/template name. The actual PVC is created later by Harvester
	// from the VM's harvesterhci.io/volumeClaimTemplates annotation.
	return dvName, nil
}

func (c *TypedClient) ResizeDataVolume(ctx context.Context, ns, vmName, dvName string, newSizeGB int) error {
	newReq := resource.MustParse(fmt.Sprintf("%dGi", newSizeGB))
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		vm, err := c.Clientset.KubevirtV1().VirtualMachines(ns).Get(ctx, vmName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		pvcs, err := typedVolumeClaimTemplates(vm)
		if err != nil {
			return err
		}
		// Index into the slice rather than range-copy: the mutation below must
		// reach the element that gets re-marshalled, independent of whether
		// typedVolumeClaimTemplates returns pointers or values.
		found := false
		for i := range pvcs {
			if pvcs[i].Name != dvName {
				continue
			}
			found = true
			// Grow-only: Harvester expands the live PVC only when the annotation
			// request exceeds the current size and silently ignores anything <=
			// (vm_controller.go createPVCsFromAnnotation). Skip the write unless
			// we're actually growing, so we never rewrite an unchanged annotation
			// (needless VM update + self-triggered reconcile) nor leave the
			// annotation understating the real PVC. The caller rejects true shrinks.
			if cur, ok := pvcs[i].Spec.Resources.Requests[corev1.ResourceStorage]; ok && newReq.Cmp(cur) <= 0 {
				return nil
			}
			if pvcs[i].Spec.Resources.Requests == nil {
				pvcs[i].Spec.Resources.Requests = corev1.ResourceList{}
			}
			pvcs[i].Spec.Resources.Requests[corev1.ResourceStorage] = newReq
			break
		}
		if !found {
			return fmt.Errorf("data volume claim template %s not found on VM %s/%s", dvName, ns, vmName)
		}
		data, err := json.Marshal(pvcs)
		if err != nil {
			return fmt.Errorf("marshal %s: %w", util.AnnotationVolumeClaimTemplates, err)
		}
		if vm.Annotations == nil {
			vm.Annotations = map[string]string{}
		}
		vm.Annotations[util.AnnotationVolumeClaimTemplates] = string(data)
		_, err = c.Clientset.KubevirtV1().VirtualMachines(ns).Update(ctx, vm, metav1.UpdateOptions{})
		return err
	})
}

func (c *TypedClient) CreatePostgresVM(ctx context.Context, p VMCreateParams) (vmName, credSecretName, cloudInitSecretName, caCertPEM string, err error) {
	vmName = fmt.Sprintf("pg-%s", p.ID)
	credSecretName = fmt.Sprintf("pg-%s-credentials", p.ID)
	cloudInitSecretName = fmt.Sprintf("pg-%s-cloudinit", p.ID)

	// Resolve the image first so a bad/missing image fails before we create any
	// Secrets (no orphans on the most common early failure).
	imgNs, imgName, imgSC, err := c.resolveVMImage(ctx, p.ImageName)
	if err != nil {
		return vmName, credSecretName, cloudInitSecretName, caCertPEM, err
	}

	// Obtain credential + TLS material idempotently. The credentials Secret is
	// the single source of truth: on a retry where it already exists we reuse
	// its material instead of regenerating, so the cloud-init Secret and the
	// returned CA always match what the VM was bootstrapped with. Regenerating
	// here would silently publish a CA/passwords that diverge from the running
	// VM (verify-ca failures, wrong credentials) on any partial re-entry.
	creds, err := c.ensureCredentialsSecret(ctx, p, vmName, credSecretName)
	if err != nil {
		return vmName, credSecretName, cloudInitSecretName, caCertPEM, err
	}
	caCertPEM = creds.tls.CACertPEM

	// cloud-init Secret, always derived from the settled material above.
	if err = c.ensureCloudInitSecret(ctx, p, cloudInitSecretName, creds); err != nil {
		return vmName, credSecretName, cloudInitSecretName, caCertPEM, err
	}

	vm, err := c.buildPostgresVM(p, vmName, cloudInitSecretName, fmt.Sprintf("%s/%s", imgNs, imgName), imgSC, true)
	if err != nil {
		return vmName, credSecretName, cloudInitSecretName, caCertPEM, err
	}
	if _, e := c.Clientset.KubevirtV1().VirtualMachines(p.Namespace).Create(ctx, vm, metav1.CreateOptions{}); e != nil {
		if err = ignoreAlreadyExists(e); err != nil {
			return vmName, credSecretName, cloudInitSecretName, caCertPEM, err
		}
	}
	return vmName, credSecretName, cloudInitSecretName, caCertPEM, err
}

// ensureCredentialsSecret returns the credential + TLS material for the
// instance, reusing the existing credentials Secret if present (retry-safe) and
// otherwise generating fresh material and creating it.
func (c *TypedClient) ensureCredentialsSecret(ctx context.Context, p VMCreateParams, vmName, name string) (*credentialMaterial, error) {
	if existing, err := c.KubeClient.CoreV1().Secrets(p.Namespace).Get(ctx, name, metav1.GetOptions{}); err == nil {
		return materialFromSecret(existing, p.Namespace, name)
	} else if !apierrors.IsNotFound(err) {
		return nil, err
	}

	// First creation: generate fresh material and persist it.
	tls, err := generateTLS(vmName)
	if err != nil {
		return nil, fmt.Errorf("TLS generation: %w", err)
	}
	m := &credentialMaterial{
		adminPw:    randomString(32),
		replPw:     randomString(32),
		exporterPw: randomString(24),
		tls:        tls,
	}
	credSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: p.Namespace},
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"admin_user":        p.MasterUser,
			"admin_password":    m.adminPw,
			"repl_password":     m.replPw,
			"exporter_password": m.exporterPw,
			"ca_cert":           tls.CACertPEM,
			"ca_key":            tls.CAKeyPEM,
			"server_cert":       tls.ServerCertPEM,
			"server_key":        tls.ServerKeyPEM,
		},
	}
	if _, err := c.KubeClient.CoreV1().Secrets(p.Namespace).Create(ctx, credSecret, metav1.CreateOptions{}); err != nil {
		// Lost a race: created concurrently between our Get and Create. Reuse the winner.
		// Classic race : TOCTOU (time-of-check-to-time-of-use)
		if apierrors.IsAlreadyExists(err) {
			won, gerr := c.KubeClient.CoreV1().Secrets(p.Namespace).Get(ctx, name, metav1.GetOptions{})
			if gerr != nil {
				return nil, gerr
			}
			return materialFromSecret(won, p.Namespace, name)
		}
		return nil, err
	}
	return m, nil
}

// ensureCloudInitSecret creates the cloud-init Secret from the given material,
// or overwrites an existing one so it always matches that material. Overwriting
// is safe because CreatePostgresVM only runs before the VM exists (phaseVM is
// guarded by VMName), so the Secret has not been consumed by a boot yet.
func (c *TypedClient) ensureCloudInitSecret(ctx context.Context, p VMCreateParams, name string, m *credentialMaterial) error {
	desired := map[string]string{
		"userdata":    buildCloudInit(p, m.adminPw, m.replPw, m.exporterPw, m.tls),
		"networkdata": buildNetworkData(p),
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: p.Namespace},
		Type:       corev1.SecretTypeOpaque,
		StringData: desired,
	}
	if _, err := c.KubeClient.CoreV1().Secrets(p.Namespace).Create(ctx, secret, metav1.CreateOptions{}); err == nil {
		return nil
	} else if !apierrors.IsAlreadyExists(err) {
		return err
	}
	// Already Exist cloud-init Secret: overwrite to match the credential generated from ensureCredentialsSecret().
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		existing, err := c.KubeClient.CoreV1().Secrets(p.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		existing.Data = nil
		existing.StringData = desired
		_, err = c.KubeClient.CoreV1().Secrets(p.Namespace).Update(ctx, existing, metav1.UpdateOptions{})
		return err
	})
}

// materialFromSecret reconstructs credentialMaterial struct from a persisted
// credentials Secret. It reads Data (populated by the apiserver) and falls back
// to StringData (set on a freshly built object, e.g. under the fake client).
func materialFromSecret(s *corev1.Secret, ns, name string) (*credentialMaterial, error) {
	get := func(k string) string {
		if v, ok := s.Data[k]; ok {
			return string(v)
		}
		return s.StringData[k]
	}
	m := &credentialMaterial{
		adminPw:    get("admin_password"),
		replPw:     get("repl_password"),
		exporterPw: get("exporter_password"),
		tls: &TLSBundle{
			CACertPEM:     get("ca_cert"),
			CAKeyPEM:      get("ca_key"),
			ServerCertPEM: get("server_cert"),
			ServerKeyPEM:  get("server_key"),
		},
	}
	if m.adminPw == "" || m.tls.CACertPEM == "" || m.tls.ServerCertPEM == "" || m.tls.ServerKeyPEM == "" {
		return nil, fmt.Errorf("credentials secret %s/%s is missing required keys", ns, name)
	}
	return m, nil
}

func (c *TypedClient) GetVMIReadiness(ctx context.Context, ns, vmName string) (VMIReadiness, error) {
	vmi, err := c.Clientset.KubevirtV1().VirtualMachineInstances(ns).Get(ctx, vmName, metav1.GetOptions{})
	if err != nil {
		return VMIReadiness{}, err
	}

	readiness := VMIReadiness{
		Running: string(vmi.Status.Phase) == vmiPhaseRunning,
		VMIUID:  string(vmi.UID),
	}
	for _, iface := range vmi.Status.Interfaces {
		if iface.Name != dataNetInterface {
			continue
		}
		readiness.IP = iface.IP
		break
	}
	for _, cond := range vmi.Status.Conditions {
		switch cond.Type {
		case kubevirtv1.VirtualMachineInstanceReady:
			readiness.Ready = cond.Status == corev1.ConditionTrue
		case kubevirtv1.VirtualMachineInstanceAgentConnected:
			readiness.AgentConnected = cond.Status == corev1.ConditionTrue
		}
	}
	return readiness, nil
}

// To align behavior with kubevirt v1.1.1, we set runStrategy to Halted when stopping a VM.
// see harvester/pkg/api/vm/handler.go 142 for harvester version 1.7.1
func (c *TypedClient) StopVM(ctx context.Context, ns, vmName string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		vm, err := c.Clientset.KubevirtV1().VirtualMachines(ns).Get(ctx, vmName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		runStrategy := kubevirtv1.RunStrategyHalted
		vm.Spec.RunStrategy = &runStrategy
		vm.Spec.Running = nil
		_, err = c.Clientset.KubevirtV1().VirtualMachines(ns).Update(ctx, vm, metav1.UpdateOptions{})
		return err
	})
}

// see harvester/pkg/api/vm/handler.go 138 : harvester version 1.7.1
func (c *TypedClient) StartVM(ctx context.Context, ns, vmName string) error {
	return c.KvClientset.KubevirtV1().VirtualMachines(ns).Start(ctx, vmName, &kubevirtv1.StartOptions{})
}

// Perform a Cold Resize of a VM - Stopping the exisintg VM and starting back is the responsibility of the caller.
func (c *TypedClient) ResizeVM(ctx context.Context, ns, vmName string, cpuCores, memoryMB int) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		vm, err := c.Clientset.KubevirtV1().VirtualMachines(ns).Get(ctx, vmName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if vm.Spec.Template.Spec.Domain.CPU == nil {
			vm.Spec.Template.Spec.Domain.CPU = &kubevirtv1.CPU{} // lazy init to avoid nil dereference
		}
		vm.Spec.Template.Spec.Domain.CPU.Cores = uint32(cpuCores)
		if vm.Spec.Template.Spec.Domain.Resources.Limits == nil {
			vm.Spec.Template.Spec.Domain.Resources.Limits = corev1.ResourceList{}
		}
		vm.Spec.Template.Spec.Domain.Resources.Limits[corev1.ResourceCPU] = *resource.NewQuantity(int64(cpuCores), resource.DecimalSI)
		// Memory: set limits only — the Harvester mutating webhook derives domain.memory.guest
		// from resources.limits[memory] on every VM update (pkg/webhook/.../mutator.go).
		vm.Spec.Template.Spec.Domain.Resources.Limits[corev1.ResourceMemory] = resource.MustParse(fmt.Sprintf("%dMi", memoryMB))
		_, err = c.Clientset.KubevirtV1().VirtualMachines(ns).Update(ctx, vm, metav1.UpdateOptions{})
		return err
	})
}

func (c *TypedClient) DeleteSecret(ctx context.Context, ns, name string) error {
	err := c.KubeClient.CoreV1().Secrets(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func (c *TypedClient) RemoveCloudInitDisk(ctx context.Context, ns, vmName string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		vm, err := c.Clientset.KubevirtV1().VirtualMachines(ns).Get(ctx, vmName, metav1.GetOptions{})
		if err != nil {
			return err
		}

		var disks []kubevirtv1.Disk
		for _, d := range vm.Spec.Template.Spec.Domain.Devices.Disks {
			if d.Name != "cloudinit" {
				disks = append(disks, d)
			}
		}
		var volumes []kubevirtv1.Volume
		for _, v := range vm.Spec.Template.Spec.Volumes {
			if v.Name != "cloudinit" {
				volumes = append(volumes, v)
			}
		}
		vm.Spec.Template.Spec.Domain.Devices.Disks = disks
		vm.Spec.Template.Spec.Volumes = volumes

		_, err = c.Clientset.KubevirtV1().VirtualMachines(ns).Update(ctx, vm, metav1.UpdateOptions{})
		return err
	})
}

func (c *TypedClient) PrepareCloudInitForRepave(ctx context.Context, p VMCreateParams, vmName, credSecretName, cloudInitSecretName string) error {
	secret, err := c.KubeClient.CoreV1().Secrets(p.Namespace).Get(ctx, credSecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get credentials Secret %s/%s for repave cloud-init: %w", p.Namespace, credSecretName, err)
	}
	m, err := materialFromSecret(secret, p.Namespace, credSecretName)
	if err != nil {
		return err
	}
	if err := c.ensureCloudInitSecret(ctx, p, cloudInitSecretName, m); err != nil {
		return fmt.Errorf("ensure cloud-init Secret %s/%s for repave: %w", p.Namespace, cloudInitSecretName, err)
	}
	return c.attachCloudInitDisk(ctx, p.Namespace, vmName, cloudInitSecretName)
}

func (c *TypedClient) attachCloudInitDisk(ctx context.Context, ns, vmName, cloudInitSecretName string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		vm, err := c.Clientset.KubevirtV1().VirtualMachines(ns).Get(ctx, vmName, metav1.GetOptions{})
		if err != nil {
			return err
		}

		hasDisk := false
		for _, disk := range vm.Spec.Template.Spec.Domain.Devices.Disks {
			if disk.Name == "cloudinit" {
				hasDisk = true
				break
			}
		}
		if !hasDisk {
			vm.Spec.Template.Spec.Domain.Devices.Disks = append(vm.Spec.Template.Spec.Domain.Devices.Disks, kubevirtv1.Disk{
				Name: "cloudinit",
				DiskDevice: kubevirtv1.DiskDevice{
					Disk: &kubevirtv1.DiskTarget{Bus: kubevirtv1.DiskBusVirtio},
				},
			})
		}

		hasVolume := false
		for i := range vm.Spec.Template.Spec.Volumes {
			if vm.Spec.Template.Spec.Volumes[i].Name != "cloudinit" {
				continue
			}
			hasVolume = true
			vm.Spec.Template.Spec.Volumes[i].VolumeSource.CloudInitNoCloud = cloudInitNoCloudSource(cloudInitSecretName)
			break
		}
		if !hasVolume {
			vm.Spec.Template.Spec.Volumes = append(vm.Spec.Template.Spec.Volumes, kubevirtv1.Volume{
				Name: "cloudinit",
				VolumeSource: kubevirtv1.VolumeSource{
					CloudInitNoCloud: cloudInitNoCloudSource(cloudInitSecretName),
				},
			})
		}

		_, err = c.Clientset.KubevirtV1().VirtualMachines(ns).Update(ctx, vm, metav1.UpdateOptions{})
		return err
	})
}

func cloudInitNoCloudSource(secretName string) *kubevirtv1.CloudInitNoCloudSource {
	ref := &corev1.LocalObjectReference{Name: secretName}
	return &kubevirtv1.CloudInitNoCloudSource{
		UserDataSecretRef:    ref,
		NetworkDataSecretRef: ref,
	}
}

// Deploy the prometheus monitoring stack. Discussion : Harvester already have Prometheus operator, what to do ?
func (c *TypedClient) DeployMonitoring(ctx context.Context, id, ns, vmIP string) (svcName, smName, grafanaURL, promTarget string, err error) {
	smName = fmt.Sprintf("pg-%s-monitor", id)
	svcName = fmt.Sprintf("pg-%s-metrics", id)
	grafanaURL = fmt.Sprintf("%s/d/dbaas-%s/postgresql-%s", c.GrafanaURL, id, id)
	promTarget = fmt.Sprintf("%s.%s.svc:9187", svcName, ns)
	if vmIP == "" {
		err = fmt.Errorf("monitoring endpoint IP is required")
		return svcName, smName, grafanaURL, promTarget, err
	}

	if err = c.createOrUpdateService(ctx, id, ns, svcName); err != nil {
		return svcName, smName, grafanaURL, promTarget, err
	}
	if err = c.createOrUpdateEndpoints(ctx, id, ns, svcName, vmIP); err != nil {
		return svcName, smName, grafanaURL, promTarget, err
	}
	err = c.createOrUpdateServiceMonitor(ctx, id, ns, smName)
	return svcName, smName, grafanaURL, promTarget, err
}

func (c *TypedClient) TeardownAll(ctx context.Context, id, ns string, refs dbaasv1.ResourceRefs) error {
	// Disk PVCs are created by Harvester from the VM's volumeClaimTemplates
	// annotation with no ownerReferences, so deleting the VM does NOT delete
	// them — they must be deleted explicitly here or they orphan (and a
	// later same-named DBInstance silently reattaches the old OS disk).
	// Collect their names from the live VM first; if the VM is already gone,
	// fall back to the naming conventions.
	diskPVCs := c.collectDiskPVCNames(ctx, id, ns, refs)

	type deleteTask struct {
		resource string
		name     string
		delete   func() error
	}
	tasks := []deleteTask{
		{"servicemonitors", refs.ServiceMonitor, func() error {
			return c.Clientset.MonitoringV1().ServiceMonitors(ns).Delete(ctx, refs.ServiceMonitor, metav1.DeleteOptions{})
		}},
		{"endpoints", refs.MetricsServiceName, func() error {
			return c.KubeClient.CoreV1().Endpoints(ns).Delete(ctx, refs.MetricsServiceName, metav1.DeleteOptions{})
		}},
		{"services", refs.MetricsServiceName, func() error {
			return c.KubeClient.CoreV1().Services(ns).Delete(ctx, refs.MetricsServiceName, metav1.DeleteOptions{})
		}},
		{"virtualmachines", refs.VMName, func() error {
			return c.Clientset.KubevirtV1().VirtualMachines(ns).Delete(ctx, refs.VMName, metav1.DeleteOptions{})
		}},
		{"secrets", refs.SecretName, func() error {
			return c.KubeClient.CoreV1().Secrets(ns).Delete(ctx, refs.SecretName, metav1.DeleteOptions{})
		}},
		{"secrets", refs.CloudInitSecretName, func() error {
			return c.KubeClient.CoreV1().Secrets(ns).Delete(ctx, refs.CloudInitSecretName, metav1.DeleteOptions{})
		}},
	}
	for _, pvcName := range diskPVCs {
		name := pvcName
		// Stray DataVolume first: the buggy DVT-based repave may have left a DV
		// that adopted the PVC; deleting the DV releases/cascades it, and the
		// direct PVC delete below covers the ownerless case. Both ignore NotFound.
		tasks = append(tasks,
			deleteTask{"datavolumes", name, func() error {
				return c.DeleteDataVolume(ctx, ns, name)
			}},
			deleteTask{"persistentvolumeclaims", name, func() error {
				return c.DeletePVC(ctx, ns, name)
			}},
		)
	}

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []string
	)
	for _, t := range tasks {
		if t.name == "" {
			continue
		}
		wg.Add(1)
		go func(dt deleteTask) {
			defer wg.Done()
			err := dt.delete()
			if err == nil || apierrors.IsNotFound(err) {
				return // successful deletion or already gone
			}
			mu.Lock()
			errs = append(errs, fmt.Sprintf("%s/%s: %v", dt.resource, dt.name, err))
			mu.Unlock()
		}(t)
	}
	wg.Wait()
	if len(errs) > 0 {
		return fmt.Errorf("teardown: %s", strings.Join(errs, "; "))
	}
	return nil
}

// collectDiskPVCNames returns every disk PVC name belonging to this instance,
// deduplicated, in priority order:
//
//  1. The live VM's volumeClaimTemplates annotation — authoritative and
//     exact, covers revision-suffixed repave names. Used whenever the VM
//     still exists.
//  2. refs.OSDiskPVCName / refs.DataVolumeName — the names the controller
//     itself recorded in status.resources at provision/repave time. Exact,
//     no guessing, used when the VM is already gone (a retried teardown
//     after partial failure) but the fields are populated.
//  3. Naming-convention fallback (pg-<id>-data, pg-<id>-os) plus a
//     namespace-wide prefix scan for revision-suffixed pg-<id>-os-<rev>
//     names — ONLY for instances that predate both #1 and #2, i.e. were
//     torn down mid-teardown before this field ever got a chance to be set.
//     This scan is deliberately a last resort: matching by string prefix is
//     ambiguous whenever one instance's name is itself a prefix of
//     another's (e.g. "orders" and "orders-os" both exist — "orders"'s
//     prefix "pg-orders-os" is also a prefix of "orders-os"'s own disk
//     "pg-orders-os-os"). #1 and #2 are exact and carry no such risk; only
//     reach this branch for genuinely pre-fix orphans, which is a shrinking,
//     bounded population, not an ongoing exposure.
func (c *TypedClient) collectDiskPVCNames(ctx context.Context, id, ns string, refs dbaasv1.ResourceRefs) []string {
	seen := map[string]bool{}
	sawVM := false
	if refs.VMName != "" {
		if vm, err := c.Clientset.KubevirtV1().VirtualMachines(ns).Get(ctx, refs.VMName, metav1.GetOptions{}); err == nil {
			if pvcs, err := typedVolumeClaimTemplates(vm); err == nil {
				sawVM = true
				for _, p := range pvcs {
					seen[p.Name] = true
				}
			}
		}
	}
	if refs.DataVolumeName != "" {
		seen[refs.DataVolumeName] = true
	}
	if sawVM {
		return mapKeys(seen)
	}

	// VM already gone: prefer the exact names recorded in status over guessing.
	if refs.OSDiskPVCName != "" {
		seen[refs.OSDiskPVCName] = true
	}
	if refs.DataVolumeName == "" {
		seen[fmt.Sprintf("pg-%s-data", id)] = true
	}
	if refs.OSDiskPVCName != "" {
		return mapKeys(seen)
	}

	// Both status field and live VM are unavailable — pre-fix orphan. Fall
	// back to the naming convention and a bounded prefix scan.
	osPVCPrefix := fmt.Sprintf("pg-%s-os", id)
	seen[osPVCPrefix] = true
	if list, err := c.KubeClient.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{}); err == nil {
		for i := range list.Items {
			if isOSDiskName(list.Items[i].Name, osPVCPrefix) {
				seen[list.Items[i].Name] = true
			}
		}
	}
	return mapKeys(seen)
}

func mapKeys(m map[string]bool) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	return names
}

func (c *TypedClient) ClearDataVolumeOwnerRef(ctx context.Context, ns, dvName string) error {
	patch := []byte(`{"metadata":{"ownerReferences":[]}}`)
	_, err := c.CdiClientset.CdiV1beta1().DataVolumes(ns).Patch(
		ctx, dvName, types.MergePatchType, patch, metav1.PatchOptions{},
	)
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func (c *TypedClient) DeleteDataVolume(ctx context.Context, ns, dvName string) error {
	err := c.CdiClientset.CdiV1beta1().DataVolumes(ns).Delete(ctx, dvName, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// SwapVMOSDisk points the VM's OS disk at a fresh PVC provisioned from the
// image referenced by imgRef. Returns the name of the PVC it replaced (empty
// when the VM is already on the target disk) and the name it's now on
// (always set) — the caller should persist the latter to
// status.resources.osDiskPVCName as the authoritative current disk name,
// rather than ever re-deriving it by pattern-matching PVC names later.
//
// The typed client builds VMs Harvester-style: disks are plain PVCs declared
// in the harvesterhci.io/volumeClaimTemplates VM annotation and created —
// ownerless — by Harvester's VM controller (createPVCsFromAnnotation). There
// are no dataVolumeTemplates to patch. Reusing the old PVC name is impossible
// without a race (Harvester recreates any annotation entry whose PVC goes
// missing), so each repave target gets a revision-suffixed name
// (pg-<id>-os-v20260615): Harvester provisions the new PVC from the new
// image's storageClass, and the old PVC is deleted by the caller afterwards.
// Distinct names mean creation and deletion can never fight over the same
// object — this is what makes the swap race-free.
func (c *TypedClient) SwapVMOSDisk(ctx context.Context, ns, vmName, instID, imgRef string) (string, string, error) {
	imgNs, imgName, imgSC, err := c.resolveVMImage(ctx, imgRef)
	if err != nil {
		return "", "", fmt.Errorf("SwapVMOSDisk: resolve image %q: %w", imgRef, err)
	}
	osPVCPrefix := fmt.Sprintf("pg-%s-os", instID)
	newPVCName := osDiskNameForImage(instID, imgName)

	oldPVCName := ""
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		vm, err := c.Clientset.KubevirtV1().VirtualMachines(ns).Get(ctx, vmName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		pvcs, err := typedVolumeClaimTemplates(vm)
		if err != nil {
			return err
		}

		found := false
		for i := range pvcs {
			if !isOSDiskName(pvcs[i].Name, osPVCPrefix) {
				continue
			}
			found = true
			if pvcs[i].Name == newPVCName {
				// Already swapped (re-entered after a crash or requeue): nothing
				// to replace, nothing for the caller to delete.
				oldPVCName = ""
				return nil
			}
			oldPVCName = pvcs[i].Name
			if pvcs[i].Annotations == nil {
				pvcs[i].Annotations = map[string]string{}
			}
			pvcs[i].Name = newPVCName
			pvcs[i].Annotations[harvesterbuilder.AnnotationKeyImageID] = fmt.Sprintf("%s/%s", imgNs, imgName)
			pvcs[i].Spec.StorageClassName = &imgSC
			break
		}
		if !found {
			return fmt.Errorf("SwapVMOSDisk: no OS disk entry (prefix %s) in %s annotation of VM %s/%s",
				osPVCPrefix, util.AnnotationVolumeClaimTemplates, ns, vmName)
		}

		data, err := json.Marshal(pvcs)
		if err != nil {
			return fmt.Errorf("SwapVMOSDisk: marshal %s: %w", util.AnnotationVolumeClaimTemplates, err)
		}
		if vm.Annotations == nil {
			vm.Annotations = map[string]string{}
		}
		vm.Annotations[util.AnnotationVolumeClaimTemplates] = string(data)

		// Repoint the os-disk volume at the new PVC.
		for i := range vm.Spec.Template.Spec.Volumes {
			v := &vm.Spec.Template.Spec.Volumes[i]
			if v.PersistentVolumeClaim != nil && isOSDiskName(v.PersistentVolumeClaim.ClaimName, osPVCPrefix) {
				v.PersistentVolumeClaim.ClaimName = newPVCName
			}
		}

		// Drop dataVolumeTemplates left behind by the earlier (DVT-based) repave
		// implementation — nothing references them, and the stray DataVolumes
		// they spawn adopt whatever PVC holds the OS disk name.
		if len(vm.Spec.DataVolumeTemplates) > 0 {
			kept := vm.Spec.DataVolumeTemplates[:0]
			for i := range vm.Spec.DataVolumeTemplates {
				if !isOSDiskName(vm.Spec.DataVolumeTemplates[i].Name, osPVCPrefix) {
					kept = append(kept, vm.Spec.DataVolumeTemplates[i])
				}
			}
			vm.Spec.DataVolumeTemplates = kept
		}

		_, err = c.Clientset.KubevirtV1().VirtualMachines(ns).Update(ctx, vm, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return "", "", err
	}
	return oldPVCName, newPVCName, nil
}

// DeletePVC deletes a PersistentVolumeClaim by name. NotFound is success:
// disk PVCs are created ownerless by Harvester from the volumeClaimTemplates
// annotation, so explicit deletion is the only way they ever go away.
func (c *TypedClient) DeletePVC(ctx context.Context, ns, name string) error {
	err := c.KubeClient.CoreV1().PersistentVolumeClaims(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// osDiskNameForImage derives the revision-suffixed OS disk name for a repave
// target image, e.g. ("orders", "ubuntu-2204-postgres-v20260615") →
// "pg-orders-os-v20260615". Deterministic, so a repave re-entered after a
// crash resolves to the same name and becomes a no-op instead of another swap.
func osDiskNameForImage(instID, imgName string) string {
	rev := imgName
	if i := strings.LastIndex(imgName, "-"); i >= 0 && i < len(imgName)-1 {
		rev = imgName[i+1:]
	}
	return fmt.Sprintf("pg-%s-os-%s", instID, rev)
}

// isOSDiskName reports whether name is this instance's OS disk: the original
// fixed name (pg-<id>-os) or a revision-suffixed repave name
// (pg-<id>-os-<rev>). The exact-or-separator check keeps instance "orders"
// from matching the disks of an instance named "orders-os".
func isOSDiskName(name, osPVCPrefix string) bool {
	return name == osPVCPrefix || strings.HasPrefix(name, osPVCPrefix+"-")
}

// Helpers
func (c *TypedClient) resolveVMImage(ctx context.Context, ref string) (ns, name, sc string, err error) {
	if ref == "" {
		return ns, name, sc, fmt.Errorf("empty image reference")
	}

	ns, spec := "default", ref
	if i := strings.Index(ref, "/"); i > 0 {
		ns, spec = ref[:i], ref[i+1:]
	}
	if spec == "" {
		return ns, name, sc, fmt.Errorf("empty image name in reference %q", ref)
	}

	img, e := c.Clientset.HarvesterhciV1beta1().VirtualMachineImages(ns).Get(ctx, spec, metav1.GetOptions{})
	if e == nil {
		return readyVMImageFields(ns, spec, img)
	}
	if !apierrors.IsNotFound(e) {
		return ns, name, sc, e
	}

	// fallback: search by displayName
	list, e := c.Clientset.HarvesterhciV1beta1().VirtualMachineImages(ns).List(ctx, metav1.ListOptions{})
	if e != nil {
		return ns, name, sc, e
	}

	var matched []harvesterhciov1beta1.VirtualMachineImage
	for _, item := range list.Items {
		if item.Spec.DisplayName == spec {
			matched = append(matched, item)
		}
	}

	switch len(matched) {
	case 0:
		return ns, name, sc, fmt.Errorf("no VirtualMachineImage in namespace %q matching name or displayName %q", ns, spec)
	case 1:
		name = matched[0].Name
		return readyVMImageFields(ns, name, &matched[0])
	default:
		return ns, name, sc, fmt.Errorf("ambiguous: %d VirtualMachineImages in namespace %q share displayName %q", len(matched), ns, spec)
	}
}

func readyVMImageFields(ns, name string, img *harvesterhciov1beta1.VirtualMachineImage) (string, string, string, error) {
	if !isVMImageImported(img) {
		return ns, name, "", fmt.Errorf("VirtualMachineImage %s/%s is not imported yet (status.conditions missing ImageImported=True)", ns, name)
	}
	sc, err := resolveImageStorageClassName(img)
	if err != nil {
		return ns, name, sc, err
	}
	return ns, name, sc, nil
}

func isVMImageImported(image *harvesterhciov1beta1.VirtualMachineImage) bool {
	if image == nil {
		return false
	}
	return harvesterhciov1beta1.ImageImported.IsTrue(image)
}

func resolveImageStorageClassName(image *harvesterhciov1beta1.VirtualMachineImage) (string, error) {
	if image == nil {
		return "", fmt.Errorf("nil image")
	}
	if image.Status.StorageClassName != "" {
		return image.Status.StorageClassName, nil
	}
	return "", fmt.Errorf("VM image %s/%s does not have a StorageClass yet (not initialized)",
		image.Namespace, image.Name)
}

func (c *TypedClient) buildPostgresVM(p VMCreateParams, vmName, cloudInitSecretName, imageID, imageSC string, running bool) (*kubevirtv1.VirtualMachine, error) {
	annotations := map[string]string{}
	if c.MgmtLogicalSwitch != "" {
		annotations["ovn.kubernetes.io/logical_switch"] = c.MgmtLogicalSwitch
	}

	runStrategy := kubevirtv1.RunStrategyHalted
	if running {
		runStrategy = kubevirtv1.RunStrategyAlways
	}

	labels := map[string]string{dbaasv1.LabelInstance: p.ID, dbaasv1.LabelRole: "primary"}
	templateLabels := map[string]string{dbaasv1.LabelInstance: p.ID}
	osPVCName := fmt.Sprintf("pg-%s-os", p.ID)
	dataPVCName := p.DataVolumeRef
	if dataPVCName == "" {
		dataPVCName = fmt.Sprintf("pg-%s-data", p.ID)
	}
	dataSizeGB := p.DataVolumeSizeGB
	if dataSizeGB <= 0 {
		dataSizeGB = 1
	}
	dataStorageClass := p.DataVolumeStorageClass
	if dataStorageClass == "" {
		dataStorageClass = "longhorn"
	}

	osPVCOption := &harvesterbuilder.PersistentVolumeClaimOption{
		ImageID:          imageID,
		VolumeMode:       corev1.PersistentVolumeBlock,
		AccessMode:       corev1.ReadWriteMany,
		StorageClassName: &imageSC,
	}
	dataPVCOption := &harvesterbuilder.PersistentVolumeClaimOption{
		VolumeMode:       corev1.PersistentVolumeBlock,
		AccessMode:       corev1.ReadWriteMany, // to allow live migration all disks should be ReadWriteMany
		StorageClassName: &dataStorageClass,
	}

	vmBuilder := harvesterbuilder.NewVMBuilder("dbaas-operator").
		Name(vmName).
		Namespace(p.Namespace).
		Labels(labels).
		VirtualMachineInstanceTemplateLabels(templateLabels).
		CPU(p.CPUCores).                         // set spec.template.spec.domain.resources.limits.cpu
		Memory(fmt.Sprintf("%dMi", p.MemoryMB)). // set spec.template.spec.domain.resources.limits.memory
		RunStrategy(runStrategy).
		PVCDisk("os-disk", harvesterbuilder.DiskBusVirtio, false, false, 1, "20Gi", osPVCName, osPVCOption).
		PVCDisk("pgdata-disk", harvesterbuilder.DiskBusVirtio, false, false, 0, fmt.Sprintf("%dGi", dataSizeGB), dataPVCName, dataPVCOption).
		CloudInitDisk("cloudinit", harvesterbuilder.DiskBusVirtio, false, 0, harvesterbuilder.CloudInitSource{
			CloudInitType:         harvesterbuilder.CloudInitTypeNoCloud,
			UserDataSecretName:    cloudInitSecretName,
			NetworkDataSecretName: cloudInitSecretName,
		}).
		NetworkInterface(dataNetInterface, string(kubevirtv1.VirtIO), "", harvesterbuilder.NetworkInterfaceTypeBridge, typedVMNetworkName(p.Namespace, p.NADName))

	vm, err := vmBuilder.VM()
	if err != nil {
		return nil, fmt.Errorf("build VM with Harvester builder helpers: %w", err)
	}
	// Post build fixes
	vm.TypeMeta = metav1.TypeMeta{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine"}
	vm.Spec.Template.ObjectMeta.Annotations = mergeStringMap(vm.Spec.Template.ObjectMeta.Annotations, annotations) // VMI/launcher-pod annotations (e.g. Kube-OVN logical switch)
	// VM-object annotations read by Harvester's control plane (webhook + VM controller).
	// AnnotationRunStrategy: Harvester's patchRunStrategy webhook reads this on every
	// Halted→non-Halted transition and patches spec.runStrategy to match. Setting it to
	// Always here ensures the webhook confirms our intent instead of overriding to RerunOnFailure.
	if vm.Annotations == nil {
		vm.Annotations = map[string]string{}
	}
	vm.Annotations[util.AnnotationRunStrategy] = string(kubevirtv1.RunStrategyAlways)
	vm.Spec.Template.Spec.Domain.CPU.Sockets = 1
	vm.Spec.Template.Spec.Domain.CPU.Threads = 1

	// Readiness probe: pg_isready runs inside the guest via the QEMU guest agent
	// virtio channel — no pod-network port exposure required.
	vm.Spec.Template.Spec.ReadinessProbe = &kubevirtv1.Probe{
		Handler: kubevirtv1.Handler{
			Exec: &corev1.ExecAction{
				Command: []string{
					"/bin/sh", "-c",
					fmt.Sprintf("pg_isready -h 127.0.0.1 -p %d -U %s -d postgres", p.Port, p.MasterUser),
				},
			},
		},
		InitialDelaySeconds: 30,
		PeriodSeconds:       10,
		TimeoutSeconds:      5,
		// SuccessThreshold=3: require 3 consecutive passes (~30s) before
		// declaring Ready again — recovery-side hysteresis so a single lucky
		// probe doesn't flap the condition back to healthy.
		SuccessThreshold: 3,
		// FailureThreshold=12 @ PeriodSeconds=10 ≈ 2 min of sustained failure
		// before Ready flips False. This probe is the single debounce for
		// database liveness: the controller treats the resulting Ready condition
		// as authoritative and does no further counting (see phaseAvailable). A
		// guest-agent disconnect also trips it, since the probe execs pg_isready
		// in-guest via the agent.
		FailureThreshold: 12,
	}

	// on Kube-OVN/VPC networking, the default DNS inherited through KubeVirt/launcher behavior can be wrong for VM bootstrapping.
	// If DNS is wrong, cloud-init may fail during apt install postgresql.. This block forces the VM path to use the intended per-VPC DNS server.
	if p.DNSServerIP != "" { // Only set when Kube-OVN/VPC is used
		dnsIP := p.DNSServerIP
		if i := strings.Index(dnsIP, "/"); i > 0 {
			dnsIP = dnsIP[:i]
		}
		vm.Spec.Template.Spec.DNSPolicy = corev1.DNSNone // to opt out of inheriting cluster DNS in Kube-OVN setup
		vm.Spec.Template.Spec.DNSConfig = &corev1.PodDNSConfig{Nameservers: []string{dnsIP}}
	}

	return vm, nil
}

func (c *TypedClient) createOrUpdateService(ctx context.Context, id, ns, svcName string) error {
	desired := typedMonitoringService(id, ns, svcName)
	if _, err := c.KubeClient.CoreV1().Services(ns).Create(ctx, desired, metav1.CreateOptions{}); err == nil {
		return nil
	} else if !apierrors.IsAlreadyExists(err) {
		return err
	}
	existing, err := c.KubeClient.CoreV1().Services(ns).Get(ctx, svcName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	existing.Labels = desired.Labels
	existing.Spec.Type = desired.Spec.Type
	existing.Spec.ClusterIP = desired.Spec.ClusterIP
	existing.Spec.Ports = desired.Spec.Ports
	existing.Spec.Selector = nil
	_, err = c.KubeClient.CoreV1().Services(ns).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func (c *TypedClient) createOrUpdateEndpoints(ctx context.Context, id, ns, svcName, vmIP string) error {
	desired := typedMonitoringEndpoints(id, ns, svcName, vmIP)
	if _, err := c.KubeClient.CoreV1().Endpoints(ns).Create(ctx, desired, metav1.CreateOptions{}); err == nil {
		return nil
	} else if !apierrors.IsAlreadyExists(err) {
		return err
	}
	existing, err := c.KubeClient.CoreV1().Endpoints(ns).Get(ctx, svcName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	existing.Labels = desired.Labels
	existing.Subsets = desired.Subsets
	_, err = c.KubeClient.CoreV1().Endpoints(ns).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func (c *TypedClient) createOrUpdateServiceMonitor(ctx context.Context, id, ns, smName string) error {
	desired := typedServiceMonitor(id, ns, smName)
	if _, err := c.Clientset.MonitoringV1().ServiceMonitors(ns).Create(ctx, desired, metav1.CreateOptions{}); err == nil {
		return nil
	} else if !apierrors.IsAlreadyExists(err) {
		return err
	}
	existing, err := c.Clientset.MonitoringV1().ServiceMonitors(ns).Get(ctx, smName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	existing.Labels = desired.Labels
	existing.Spec.Selector = desired.Spec.Selector
	existing.Spec.Endpoints = desired.Spec.Endpoints
	_, err = c.Clientset.MonitoringV1().ServiceMonitors(ns).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func typedMonitoringService(id, ns, svcName string) *corev1.Service {
	// create a headless service for the Postgres exporter metrics endpoint
	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      svcName,
			Namespace: ns,
			Labels:    map[string]string{dbaasv1.LabelInstance: id, dbaasv1.LabelMetrics: "true"},
		},
		Spec: corev1.ServiceSpec{
			Type:      corev1.ServiceTypeClusterIP,
			ClusterIP: corev1.ClusterIPNone,
			Ports: []corev1.ServicePort{{
				Name:       "metrics",
				Port:       9187,
				TargetPort: intstr.FromInt(9187),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
}

func typedMonitoringEndpoints(id, ns, svcName, vmIP string) *corev1.Endpoints {
	return &corev1.Endpoints{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Endpoints"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      svcName,
			Namespace: ns,
			Labels:    map[string]string{dbaasv1.LabelInstance: id, dbaasv1.LabelMetrics: "true"},
		},
		Subsets: []corev1.EndpointSubset{{
			Addresses: []corev1.EndpointAddress{{IP: vmIP}},
			Ports: []corev1.EndpointPort{{
				Name:     "metrics",
				Port:     9187,
				Protocol: corev1.ProtocolTCP,
			}},
		}},
	}
}

func typedServiceMonitor(id, ns, smName string) *monitoringv1.ServiceMonitor {
	return &monitoringv1.ServiceMonitor{
		TypeMeta: metav1.TypeMeta{APIVersion: "monitoring.coreos.com/v1", Kind: "ServiceMonitor"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      smName,
			Namespace: ns,
			Labels:    map[string]string{dbaasv1.LabelInstance: id, "release": "prometheus"},
		},
		Spec: monitoringv1.ServiceMonitorSpec{
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{dbaasv1.LabelMetrics: "true", dbaasv1.LabelInstance: id},
			},
			Endpoints: []monitoringv1.Endpoint{{
				Port:     "metrics",
				Interval: monitoringv1.Duration("15s"),
				Path:     "/metrics",
			}},
		},
	}
}

func typedVMNetworkName(namespace, nadName string) string {
	if strings.Contains(nadName, "/") {
		return nadName
	}
	return fmt.Sprintf("%s/%s", namespace, nadName)
}

// typedVolumeClaimTemplates parses the VM's volumeClaimTemplates annotation
// into PVC templates. It returns pointers so callers can mutate entries in
// place, but callers should still index the slice (pvcs[i]) rather than
// range-copy so the mutation stays correct if this ever returns values.
func typedVolumeClaimTemplates(vm *kubevirtv1.VirtualMachine) ([]*corev1.PersistentVolumeClaim, error) {
	raw := vm.Annotations[util.AnnotationVolumeClaimTemplates]
	if raw == "" {
		return nil, fmt.Errorf("VM %s/%s has no %s annotation", vm.Namespace, vm.Name, util.AnnotationVolumeClaimTemplates)
	}
	var pvcs []*corev1.PersistentVolumeClaim
	if err := json.Unmarshal([]byte(raw), &pvcs); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", util.AnnotationVolumeClaimTemplates, err)
	}
	return pvcs, nil
}

func mergeStringMap(base map[string]string, overlay map[string]string) map[string]string {
	if len(base) == 0 && len(overlay) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		out[k] = v
	}
	return out
}

func ptr[T any](v T) *T {
	return &v
}
