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

// Package harvester wraps the Kubernetes dynamic client to drive the Harvester
// HCI APIs (KubeVirt, CDI, Prometheus Operator) that back a DBInstance: the
// PostgreSQL VM, storage, monitoring, and teardown. Networking is provided by
// an existing Multus NetworkAttachmentDefinition (spec.networkRef); the
// controller does not create or own NADs, VPCs, or subnets.
package harvester

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

// GVRs for Harvester resources.
var (
	vmGVR = schema.GroupVersionResource{
		Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines",
	}
	vmiGVR = schema.GroupVersionResource{
		Group: "kubevirt.io", Version: "v1", Resource: "virtualmachineinstances",
	}
	dvGVR = schema.GroupVersionResource{
		Group: "cdi.kubevirt.io", Version: "v1beta1", Resource: "datavolumes",
	}
	secretGVR = schema.GroupVersionResource{
		Group: "", Version: "v1", Resource: "secrets",
	}
	serviceGVR = schema.GroupVersionResource{
		Group: "", Version: "v1", Resource: "services",
	}
	endpointsGVR = schema.GroupVersionResource{
		Group: "", Version: "v1", Resource: "endpoints",
	}
	smGVR = schema.GroupVersionResource{
		Group: "monitoring.coreos.com", Version: "v1", Resource: "servicemonitors",
	}
	vmImageGVR = schema.GroupVersionResource{
		Group: "harvesterhci.io", Version: "v1beta1", Resource: "virtualmachineimages",
	}
)

const (
	vmiPhaseRunning = "Running"
	// dataNetInterface is the VM's tenant-facing NIC, bridged onto the
	// Multus NAD from spec.networkRef. Tenant clients (psql / app pods
	// on the same VLAN) reach the DB through this interface; the
	// published status.endpoint.address is this interface's IP.
	dataNetInterface = "data-net"
	// mgmtNetInterface is the VM's controller-facing NIC. It runs in
	// KubeVirt masquerade mode on the cluster's default pod network
	// with the Postgres port exposed via KubeVirt port-forwarding. The
	// controller (which lives on the same pod network) dials the
	// launcher pod's IP at this port to verify readiness — no Multus
	// probe pod, no DHCP on the data VLAN. It also gives the VM
	// cluster egress at first boot so `apt install` doesn't depend on
	// the data VLAN's upstream connectivity.
	mgmtNetInterface = "mgmt-net"
)

// Client wraps the Kubernetes dynamic client for Harvester API calls.
type Client struct {
	Dynamic    dynamic.Interface
	GrafanaURL string
	// MgmtLogicalSwitch, when non-empty, is stamped onto the VM launcher pod
	// as the `ovn.kubernetes.io/logical_switch` annotation. On a Kube-OVN
	// cluster this keeps the launcher pod's DEFAULT network (and therefore the
	// mgmt-net masquerade NIC the controller probes) on a shared,
	// controller-reachable subnet (e.g. "ovn-default") instead of inheriting
	// the project namespace's tenant-VPC default — which is isolated and
	// unreachable from the controller. The data-net NIC still attaches to the
	// tenant subnet via Multus. Empty = don't set the annotation (correct for
	// non-OVN clusters and for unit tests). See DialVMListener.
	MgmtLogicalSwitch string
}

var _ ClientInterface = (*Client)(nil)

func NewClient(dyn dynamic.Interface, grafanaURL string) *Client {
	return &Client{Dynamic: dyn, GrafanaURL: grafanaURL}
}

// VMCreateParams bundles everything needed to create a PostgreSQL VM. Fields
// used only for credential/cloud-init generation (DBName, MaxConnections,
// backup/S3, VMPassword, StaticNetwork) moved to internal/credentials.
// BootstrapParams in PR8 — the provider only builds VM shape and consumes an
// already-provisioned cloud-init Secret. Port and MasterUser stay: the VMI
// readiness probe embeds them directly (see buildPostgresVM).
type VMCreateParams struct {
	ID                     string
	Namespace              string
	CPUCores               int
	MemoryMB               int
	OSImage                string
	DataVolumeRef          string
	DataVolumeSizeGB       int
	DataVolumeStorageClass string
	NADName                string
	MasterUser             string
	Port                   int
	// CloudInitSecretName is the pre-created ephemeral Secret (userdata +
	// networkdata) this VM's cloudInitNoCloud volume references.
	CloudInitSecretName string
	// DNSServerIP, when non-empty, pins the VM's resolver via KubeVirt
	// dnsPolicy=None + dnsConfig.nameservers. Required on Kube-OVN VPC
	// subnets to defeat the virt-launcher internal-DHCP DNS race (it would
	// otherwise inject unreachable cluster DNS, breaking apt during
	// cloud-init). Supplied by the control plane (per-VPC CoreDNS address).
	DNSServerIP string
	// Owner, when non-nil, is stamped as the controller owner reference on the
	// VM this call creates, so Owns() watches fire and GC backs up the
	// finalizer teardown (PR7). Typed client only; the legacy dynamic client
	// ignores it.
	Owner *metav1.OwnerReference
}

// VMIReadiness bundles the VMI state fields needed for phase gating and
// liveness monitoring from a single VMI fetch.
//
//   - Running: VMI phase is Running.
//   - IP: data-network IP reported by the guest agent; empty until QGA populates it.
//   - Ready: VMI readiness condition is True (readiness probe has passed).
//   - AgentConnected: QGA virtio channel is active.
//   - VMIUID: VMI object UID; a change across reconciles indicates an unplanned restart.
type VMIReadiness struct {
	Running        bool
	IP             string
	Ready          bool
	AgentConnected bool
	VMIUID         string
}

// ============================================================
// Storage: CDI DataVolume
// ============================================================

func (c *Client) CreateDataVolume(ctx context.Context, id, ns string, sizeGB int, storageClass string) (string, error) {
	dvName := fmt.Sprintf("pg-%s-data", id)
	dv := newUnstructured("cdi.kubevirt.io/v1beta1", "DataVolume", dvName, ns)
	dv.SetLabels(map[string]string{dbaasv1.LabelInstance: id, dbaasv1.LabelRole: "pgdata"})

	_ = unstructured.SetNestedMap(dv.Object, map[string]any{}, "spec", "source", "blank")
	_ = unstructured.SetNestedStringSlice(dv.Object, []string{"ReadWriteOnce"}, "spec", "pvc", "accessModes")
	_ = unstructured.SetNestedField(dv.Object, "Block", "spec", "pvc", "volumeMode")
	_ = unstructured.SetNestedField(dv.Object, fmt.Sprintf("%dGi", sizeGB), "spec", "pvc", "resources", "requests", "storage")
	_ = unstructured.SetNestedField(dv.Object, storageClass, "spec", "pvc", "storageClassName")

	if _, e := c.Dynamic.Resource(dvGVR).Namespace(ns).Create(ctx, dv, metav1.CreateOptions{}); e != nil {
		return dvName, ignoreAlreadyExists(e)
	}
	return dvName, nil
}

func (c *Client) ResizeDataVolume(ctx context.Context, ns, vmName, dvName string, newSizeGB int) error {
	dv, err := c.Dynamic.Resource(dvGVR).Namespace(ns).Get(ctx, dvName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	_ = unstructured.SetNestedField(dv.Object, fmt.Sprintf("%dGi", newSizeGB), "spec", "pvc", "resources", "requests", "storage")
	_, err = c.Dynamic.Resource(dvGVR).Namespace(ns).Update(ctx, dv, metav1.UpdateOptions{})
	return err
}

// ============================================================
// VM: KubeVirt VirtualMachine + cloud-init + credentials Secret
// ============================================================

// resolveVMImage maps a user-supplied image reference to the underlying
// Harvester VirtualMachineImage and its image-managed StorageClass.
//
// The reference accepts:
//   - "<name>"           — looked up in the "default" namespace
//   - "<ns>/<name>"      — explicit namespace
//   - "<displayName>"    — fall-back search by VirtualMachineImage.spec.displayName
//
// Returns the resolved namespace, VMImage name, and StorageClass that the
// DataVolume must use. The DataVolume should also carry annotation
// harvesterhci.io/imageId=<ns>/<name>.
func (c *Client) resolveVMImage(ctx context.Context, ref string) (ns, name, sc string, err error) {
	ns, spec := "default", ref
	if i := strings.Index(ref, "/"); i > 0 {
		ns, spec = ref[:i], ref[i+1:]
	}

	if img, e := c.Dynamic.Resource(vmImageGVR).Namespace(ns).Get(ctx, spec, metav1.GetOptions{}); e == nil {
		name = spec
		sc, _, _ = unstructured.NestedString(img.Object, "status", "storageClassName")
		if sc == "" {
			err = fmt.Errorf("VirtualMachineImage %s/%s has no status.storageClassName yet (image not ready)", ns, name)
		}
		return ns, name, sc, err
	} else if !apierrors.IsNotFound(e) {
		err = e
		return ns, name, sc, err
	}

	list, e := c.Dynamic.Resource(vmImageGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	if e != nil {
		err = e
		return ns, name, sc, err
	}
	for _, item := range list.Items {
		dn, _, _ := unstructured.NestedString(item.Object, "spec", "displayName")
		if dn == spec {
			name = item.GetName()
			sc, _, _ = unstructured.NestedString(item.Object, "status", "storageClassName")
			if sc == "" {
				err = fmt.Errorf("VirtualMachineImage %s/%s (displayName=%s) has no status.storageClassName yet", ns, name, spec)
			}
			return ns, name, sc, err
		}
	}
	err = fmt.Errorf("no VirtualMachineImage in namespace %s matching name or displayName %q", ns, spec)
	return ns, name, sc, err
}

func (c *Client) CreatePostgresVM(ctx context.Context, p VMCreateParams) (vmName string, err error) {
	vmName = fmt.Sprintf("pg-%s", p.ID)

	// Resolve the Harvester VirtualMachineImage before creating the VM so a
	// missing image causes an early error.
	imgNs, imgName, imgSC, err := c.resolveVMImage(ctx, p.OSImage)
	if err != nil {
		return vmName, err
	}

	// Build VirtualMachine CR
	vm := newUnstructured("kubevirt.io/v1", "VirtualMachine", vmName, p.Namespace)
	vm.SetLabels(map[string]string{dbaasv1.LabelInstance: p.ID, dbaasv1.LabelRole: "primary"})

	// Pod-template annotations. On Kube-OVN clusters, pinning the launcher
	// pod's logical switch keeps its DEFAULT network (the mgmt-net masquerade
	// NIC the controller dials) on a shared, reachable subnet rather than the
	// project namespace's isolated tenant-VPC default. Empty MgmtLogicalSwitch
	// → no annotation (non-OVN clusters, tests). See Client.MgmtLogicalSwitch.
	templateAnnotations := map[string]any{}
	if c.MgmtLogicalSwitch != "" {
		templateAnnotations["ovn.kubernetes.io/logical_switch"] = c.MgmtLogicalSwitch
	}

	spec := map[string]any{
		"running": true,
		"dataVolumeTemplates": []any{
			map[string]any{
				"apiVersion": "cdi.kubevirt.io/v1beta1",
				"kind":       "DataVolume",
				"metadata": map[string]any{
					"name": fmt.Sprintf("pg-%s-os", p.ID),
					"annotations": map[string]any{
						"harvesterhci.io/imageId": fmt.Sprintf("%s/%s", imgNs, imgName),
					},
				},
				"spec": map[string]any{
					"source": map[string]any{
						"blank": map[string]any{},
					},
					"pvc": map[string]any{
						"accessModes":      []any{"ReadWriteMany"},
						"volumeMode":       "Block",
						"storageClassName": imgSC,
						"resources": map[string]any{
							"requests": map[string]any{"storage": "20Gi"},
						},
					},
				},
			},
		},
		"template": map[string]any{
			"metadata": map[string]any{
				"labels":      map[string]any{dbaasv1.LabelInstance: p.ID},
				"annotations": templateAnnotations,
			},
			"spec": map[string]any{
				"domain": map[string]any{
					"cpu":    map[string]any{"cores": int64(p.CPUCores), "sockets": int64(1), "threads": int64(1)},
					"memory": map[string]any{"guest": fmt.Sprintf("%dMi", p.MemoryMB)},
					"devices": map[string]any{
						"disks": []any{
							map[string]any{"name": "os-disk", "disk": map[string]any{"bus": "virtio"}, "bootOrder": int64(1)},
							map[string]any{"name": "pgdata-disk", "disk": map[string]any{"bus": "virtio"}},
							map[string]any{"name": "cloudinit", "disk": map[string]any{"bus": "virtio"}},
						},
						"interfaces": vmInterfaces(p.Port),
					},
				},
				"networks": vmNetworks(p.Namespace, p.NADName),
				"volumes": []any{
					map[string]any{"name": "os-disk", "dataVolume": map[string]any{"name": fmt.Sprintf("pg-%s-os", p.ID)}},
					map[string]any{"name": "pgdata-disk", "dataVolume": map[string]any{"name": p.DataVolumeRef}},
					// Harvester's VM mutating webhook silently strips the
					// newer `userDataSecretRef` field while leaving
					// `networkDataSecretRef` intact — the VM ends up with
					// network config but NO user data, so cloud-init never
					// runs our packages / runcmd / ssh_pwauth and the database
					// never installs. Use the legacy `secretRef` (which
					// Harvester recognises) for the userdata key, plus
					// `networkDataSecretRef` for the networkdata key. Both
					// point at the ephemeral cloud-init Secret.
					map[string]any{"name": "cloudinit", "cloudInitNoCloud": map[string]any{
						"secretRef":            map[string]any{"name": p.CloudInitSecretName},
						"networkDataSecretRef": map[string]any{"name": p.CloudInitSecretName},
					}},
				},
			},
		},
	}
	// DNS pinning (Kube-OVN VPC subnets). Without this, KubeVirt's bridge-mode
	// virt-launcher internal DHCP server copies the launcher pod's cluster
	// resolv.conf into the VM, overriding what OVN's DHCP advertises — the VM
	// then can't resolve the apt archive and cloud-init's postgres install
	// fails. dnsPolicy=None + dnsConfig.nameservers=[per-VPC CoreDNS] silences
	// it. Empty DNSServerIP (cluster-routable VLANs) keeps KubeVirt's default.
	if p.DNSServerIP != "" {
		dnsIP := p.DNSServerIP
		if i := strings.Index(dnsIP, "/"); i > 0 { // strip any CIDR suffix (INET → "x.x.x.x/32")
			dnsIP = dnsIP[:i]
		}
		tmplSpec := spec["template"].(map[string]any)["spec"].(map[string]any)
		tmplSpec["dnsPolicy"] = "None"
		tmplSpec["dnsConfig"] = map[string]any{"nameservers": []any{dnsIP}}
	}

	_ = unstructured.SetNestedField(vm.Object, spec, "spec")

	if _, e := c.Dynamic.Resource(vmGVR).Namespace(p.Namespace).Create(ctx, vm, metav1.CreateOptions{}); e != nil {
		err = ignoreAlreadyExists(e)
	}
	return vmName, err
}

// GetVMIReadiness fetches the VMI once and returns phase, IP, and postgres-readiness.
//
// NOTE: Ready, AgentConnected, and VMIUID are not yet populated by the dynamic
// client. They will be added when the dynamic client is updated (see migration plan
// Step 3 — dynamic client). The TypedClient implementation populates all fields.
func (c *Client) GetVMIReadiness(ctx context.Context, ns, vmName string) (VMIReadiness, error) {
	vmi, err := c.Dynamic.Resource(vmiGVR).Namespace(ns).Get(ctx, vmName, metav1.GetOptions{})
	if err != nil {
		return VMIReadiness{}, err
	}

	phase, _, _ := unstructured.NestedString(vmi.Object, "status", "phase")
	running := phase == vmiPhaseRunning

	var ip string
	interfaces, _, _ := unstructured.NestedSlice(vmi.Object, "status", "interfaces")
	// The published endpoint MUST be the data-net (Multus bridge) IP — that's
	// the only address tenant clients on the VLAN can reach. The mgmt-net
	// (pod masquerade) IP is controller-only, not reachable from the VLAN,
	// and changes on every restart, so it must never be returned here.
	//
	// We deliberately do NOT fall back to any other interface's address:
	// after a stop/start, Harvester assigns the data-net IP a few seconds
	// later than the mgmt-net pod IP, and a fallback would publish the
	// unreachable pod IP as the endpoint (and it would then stick). If
	// data-net has no IP yet, return empty so the reconciler keeps waiting.
	for _, iface := range interfaces {
		ifMap, ok := iface.(map[string]any)
		if !ok {
			continue
		}
		if name, _ := ifMap["name"].(string); name != dataNetInterface {
			continue
		}
		if addr, _ := ifMap["ipAddress"].(string); addr != "" {
			ip = addr
		}
		break
	}

	return VMIReadiness{Running: running, IP: ip}, nil
}

// setVMRunning sets spec.running on the VM.
func (c *Client) setVMRunning(ctx context.Context, ns, vmName string, running bool) error {
	vm, err := c.Dynamic.Resource(vmGVR).Namespace(ns).Get(ctx, vmName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	_ = unstructured.SetNestedField(vm.Object, running, "spec", "running")
	_, err = c.Dynamic.Resource(vmGVR).Namespace(ns).Update(ctx, vm, metav1.UpdateOptions{})
	return err
}

// GetSecret returns the Secret's data map (values are raw bytes).
func (c *Client) GetSecret(ctx context.Context, ns, name string) (map[string][]byte, error) {
	obj, err := c.Dynamic.Resource(secretGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	rawData, _, _ := unstructured.NestedMap(obj.Object, "data")
	out := make(map[string][]byte, len(rawData))
	for k, v := range rawData {
		if s, ok := v.(string); ok {
			decoded, err := base64.StdEncoding.DecodeString(s)
			if err == nil {
				out[k] = decoded
			}
		}
	}
	return out, nil
}

func (c *Client) StopVM(ctx context.Context, ns, vmName string) error {
	return c.setVMRunning(ctx, ns, vmName, false)
}

func (c *Client) StartVM(ctx context.Context, ns, vmName string) error {
	return c.setVMRunning(ctx, ns, vmName, true)
}

// ResizeVM updates CPU/memory on the VM spec.
func (c *Client) ResizeVM(ctx context.Context, ns, vmName string, cpuCores, memoryMB int) error {
	vm, err := c.Dynamic.Resource(vmGVR).Namespace(ns).Get(ctx, vmName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	_ = unstructured.SetNestedField(vm.Object, int64(cpuCores), "spec", "template", "spec", "domain", "cpu", "cores")
	_ = unstructured.SetNestedField(vm.Object, fmt.Sprintf("%dMi", memoryMB), "spec", "template", "spec", "domain", "memory", "guest")
	_, err = c.Dynamic.Resource(vmGVR).Namespace(ns).Update(ctx, vm, metav1.UpdateOptions{})
	return err
}

// ============================================================
// Monitoring
// ============================================================

// DeployMonitoring creates the headless metrics Service, its manual Endpoints,
// and a Prometheus
// ServiceMonitor for the instance. It returns the Service name, the
// ServiceMonitor name, the Grafana dashboard URL, and the Prometheus
// scrape target. The caller stores both names in status.resources so the
// finalizer can delete them later.
func (c *Client) DeployMonitoring(ctx context.Context, id, ns, vmIP string) (svcName, smName, grafanaURL, promTarget string, err error) {
	smName = fmt.Sprintf("pg-%s-monitor", id)
	svcName = fmt.Sprintf("pg-%s-metrics", id)
	grafanaURL = fmt.Sprintf("%s/d/dbaas-%s/postgresql-%s", c.GrafanaURL, id, id)
	promTarget = fmt.Sprintf("%s.%s.svc:9187", svcName, ns)
	if vmIP == "" {
		err = fmt.Errorf("monitoring endpoint IP is required")
		return svcName, smName, grafanaURL, promTarget, err
	}

	// Headless service. Do not use a selector: the KubeVirt virt-launcher pod
	// has the instance label, but the exporter runs inside the VM at vmIP.
	// A manual Endpoints object below points Prometheus at the guest address.
	svc := newUnstructured("v1", "Service", svcName, ns)
	svc.SetLabels(map[string]string{dbaasv1.LabelInstance: id, dbaasv1.LabelMetrics: "true"})
	_ = unstructured.SetNestedField(svc.Object, "ClusterIP", "spec", "type")
	_ = unstructured.SetNestedField(svc.Object, "None", "spec", "clusterIP")
	_ = unstructured.SetNestedSlice(svc.Object, []any{
		map[string]any{"name": "metrics", "port": int64(9187), "targetPort": int64(9187), "protocol": "TCP"},
	}, "spec", "ports")
	unstructured.RemoveNestedField(svc.Object, "spec", "selector")
	if err = c.createOrUpdate(ctx, serviceGVR, ns, svc, func(existing *unstructured.Unstructured) {
		existing.SetLabels(svc.GetLabels())
		_ = unstructured.SetNestedField(existing.Object, "ClusterIP", "spec", "type")
		_ = unstructured.SetNestedField(existing.Object, "None", "spec", "clusterIP")
		_ = unstructured.SetNestedSlice(existing.Object, []any{
			map[string]any{"name": "metrics", "port": int64(9187), "targetPort": int64(9187), "protocol": "TCP"},
		}, "spec", "ports")
		unstructured.RemoveNestedField(existing.Object, "spec", "selector")
	}); err != nil {
		return svcName, smName, grafanaURL, promTarget, err
	}

	ep := newUnstructured("v1", "Endpoints", svcName, ns)
	ep.SetLabels(map[string]string{dbaasv1.LabelInstance: id, dbaasv1.LabelMetrics: "true"})
	_ = unstructured.SetNestedSlice(ep.Object, monitoringEndpointSubsets(vmIP), "subsets")
	if err = c.createOrUpdate(ctx, endpointsGVR, ns, ep, func(existing *unstructured.Unstructured) {
		existing.SetLabels(ep.GetLabels())
		_ = unstructured.SetNestedSlice(existing.Object, monitoringEndpointSubsets(vmIP), "subsets")
	}); err != nil {
		return svcName, smName, grafanaURL, promTarget, err
	}

	// ServiceMonitor
	sm := newUnstructured("monitoring.coreos.com/v1", "ServiceMonitor", smName, ns)
	sm.SetLabels(map[string]string{dbaasv1.LabelInstance: id, "release": "prometheus"})
	_ = unstructured.SetNestedField(sm.Object, map[string]any{
		"matchLabels": map[string]any{dbaasv1.LabelMetrics: "true", dbaasv1.LabelInstance: id},
	}, "spec", "selector")
	_ = unstructured.SetNestedSlice(sm.Object, []any{
		map[string]any{"port": "metrics", "interval": "15s", "path": "/metrics"},
	}, "spec", "endpoints")
	err = c.createOrUpdate(ctx, smGVR, ns, sm, func(existing *unstructured.Unstructured) {
		existing.SetLabels(sm.GetLabels())
		_ = unstructured.SetNestedField(existing.Object, map[string]any{
			"matchLabels": map[string]any{dbaasv1.LabelMetrics: "true", dbaasv1.LabelInstance: id},
		}, "spec", "selector")
		_ = unstructured.SetNestedSlice(existing.Object, []any{
			map[string]any{"port": "metrics", "interval": "15s", "path": "/metrics"},
		}, "spec", "endpoints")
	})

	return svcName, smName, grafanaURL, promTarget, err
}

// ============================================================
// Teardown
// ============================================================

// TeardownAll deletes every Harvester resource the controller created for
// this DBInstance, in parallel, and returns an aggregated error if any
// delete failed for a reason other than NotFound. The caller (the
// reconcileDelete finalizer path) must keep the finalizer in place until
// this returns nil — otherwise orphan VMs / DataVolumes / Secrets / Services
// can be left behind after the CR is gone.
//
// The NetworkAttachmentDefinition is always assumed to be owned by the
// cluster operator (referenced via spec.networkRef) and is never deleted.
func (c *Client) TeardownAll(ctx context.Context, id, ns string, refs dbaasv1.ResourceRefs) error {
	type deleteTask struct {
		gvr       schema.GroupVersionResource
		namespace string
		name      string
	}
	tasks := []deleteTask{
		{smGVR, ns, refs.ServiceMonitor},
		{endpointsGVR, ns, refs.MetricsServiceName},
		{serviceGVR, ns, refs.MetricsServiceName},
		{vmGVR, ns, refs.VMName},
		{dvGVR, ns, refs.DataVolumeName},
		{secretGVR, ns, refs.SecretName},
		{secretGVR, ns, refs.CloudInitSecretName},
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
			var err error
			if dt.namespace != "" {
				err = c.Dynamic.Resource(dt.gvr).Namespace(dt.namespace).Delete(ctx, dt.name, metav1.DeleteOptions{})
			} else {
				err = c.Dynamic.Resource(dt.gvr).Delete(ctx, dt.name, metav1.DeleteOptions{})
			}
			if err == nil || apierrors.IsNotFound(err) {
				return
			}
			mu.Lock()
			errs = append(errs, fmt.Sprintf("%s/%s: %v", dt.gvr.Resource, dt.name, err))
			mu.Unlock()
		}(t)
	}
	wg.Wait()
	if len(errs) > 0 {
		return fmt.Errorf("teardown: %s", strings.Join(errs, "; "))
	}
	return nil
}

// RemoveCloudInitDisk is a no-op on the dynamic client pending implementation.
// The typed client (default) has the full implementation.
func (c *Client) RemoveCloudInitDisk(_ context.Context, _, _ string) error { return nil }

// DeleteSecret deletes a Secret, ignoring NotFound. Used by the controller to
// clean up the ephemeral cloud-init Secret once the VM reaches Available.
func (c *Client) DeleteSecret(ctx context.Context, ns, name string) error {
	err := c.Dynamic.Resource(secretGVR).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// ============================================================
// Helpers
// ============================================================

// vmInterfaces and vmNetworks describe the VM's two NICs. They must
// match one-for-one (same names, same order), and the order also
// determines PCI slot assignment inside the VM:
//
//   - data-net (bridge on Multus NAD) → enp1s0  ─ tenant client traffic
//   - mgmt-net (pod-network masquerade) → enp2s0 ─ controller probe + egress
//
// vmInterfaces takes the Postgres port so KubeVirt's port-forwarding
// rule exposes it on the launcher pod's IP for the controller to dial.
func vmInterfaces(port int) []any {
	return []any{
		map[string]any{"name": dataNetInterface, "bridge": map[string]any{}},
		map[string]any{
			"name":       mgmtNetInterface,
			"masquerade": map[string]any{},
			"ports": []any{
				map[string]any{"port": int64(port), "protocol": "TCP"},
			},
		},
	}
}

func vmNetworks(namespace, nadName string) []any {
	networkName := nadName
	if !strings.Contains(nadName, "/") {
		networkName = fmt.Sprintf("%s/%s", namespace, nadName)
	}
	return []any{
		map[string]any{
			"name":   dataNetInterface,
			"multus": map[string]any{"networkName": networkName},
		},
		map[string]any{
			"name": mgmtNetInterface,
			"pod":  map[string]any{},
		},
	}
}

func newUnstructured(apiVersion, kind, name, namespace string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata": map[string]any{
			"name": name,
		},
	}}
	if namespace != "" {
		obj.SetNamespace(namespace)
	}
	return obj
}

func (c *Client) createOrUpdate(ctx context.Context, gvr schema.GroupVersionResource, ns string, desired *unstructured.Unstructured, mutateExisting func(*unstructured.Unstructured)) error {
	if _, err := c.Dynamic.Resource(gvr).Namespace(ns).Create(ctx, desired, metav1.CreateOptions{}); err == nil {
		return nil
	} else if !apierrors.IsAlreadyExists(err) {
		return err
	}
	existing, err := c.Dynamic.Resource(gvr).Namespace(ns).Get(ctx, desired.GetName(), metav1.GetOptions{})
	if err != nil {
		return err
	}
	mutateExisting(existing)
	_, err = c.Dynamic.Resource(gvr).Namespace(ns).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func monitoringEndpointSubsets(vmIP string) []any {
	return []any{
		map[string]any{
			"addresses": []any{
				map[string]any{"ip": vmIP},
			},
			"ports": []any{
				map[string]any{"name": "metrics", "port": int64(9187), "protocol": "TCP"},
			},
		},
	}
}

// ignoreAlreadyExists returns nil if err is an AlreadyExists API error, otherwise err.
func ignoreAlreadyExists(err error) error {
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}
