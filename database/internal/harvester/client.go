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
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
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
	pvcGVR = schema.GroupVersionResource{
		Group: "", Version: "v1", Resource: "persistentvolumeclaims",
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
	// cluster this keeps the launcher pod's DEFAULT network on a shared,
	// controller-reachable subnet (e.g. "ovn-default") instead of inheriting
	// the project namespace's tenant-VPC default — which is isolated and
	// unreachable from the controller. The data-net NIC still attaches to the
	// tenant subnet via Multus. Empty = don't set the annotation (correct for
	// non-OVN clusters and for unit tests).
	MgmtLogicalSwitch string
}

var _ ClientInterface = (*Client)(nil)

func NewClient(dyn dynamic.Interface, grafanaURL string) *Client {
	return &Client{Dynamic: dyn, GrafanaURL: grafanaURL}
}

// VMCreateParams bundles everything needed to create a PostgreSQL VM.
type VMCreateParams struct {
	ID        string
	Namespace string
	CPUCores  int
	MemoryMB  int
	// ImageName is the Harvester VirtualMachineImage name resolved from the
	// BakedImages catalog by the controller. e.g. "ubuntu-2204-postgres-v20260615".
	ImageName string
	// EngineVersion is the PostgreSQL major version to activate via cloud-init.
	// e.g. "16". All supported versions are pre-installed in the baked image;
	// bootstrap.sh activates only this one.
	EngineVersion          string
	DataVolumeRef          string
	DataVolumeSizeGB       int
	DataVolumeStorageClass string
	NADName                string
	MasterUser             string
	DBName                 string
	Port                   int
	MaxConnections         int
	BackupEnabled          bool
	BackupWindow           string
	S3Config               *dbaasv1.S3BackupConfig
	VMPassword             string
	// StaticNetwork, when non-nil, makes the cloud-init netplan use a
	// static IPv4 config instead of DHCP. Used on VLANs without a DHCP
	// server.
	StaticNetwork *dbaasv1.NetworkConfig
	// DNSServerIP, when non-empty, pins the VM's resolver via KubeVirt
	// dnsPolicy=None + dnsConfig.nameservers. Required on Kube-OVN VPC
	// subnets to defeat the virt-launcher internal-DHCP DNS race.
	DNSServerIP string
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

func (c *Client) CreatePostgresVM(ctx context.Context, p VMCreateParams) (vmName, credSecretName, cloudInitSecretName, caCertPEM string, err error) {
	vmName = fmt.Sprintf("pg-%s", p.ID)
	credSecretName = fmt.Sprintf("pg-%s-credentials", p.ID)
	cloudInitSecretName = fmt.Sprintf("pg-%s-cloudinit", p.ID)

	// Generate credentials
	adminPw := randomString(32)
	replPw := randomString(32)
	exporterPw := randomString(24)

	// Generate per-instance TLS: ephemeral CA + server cert signed by that CA.
	// CA key is stored in the credentials Secret alongside DB credentials.
	tls, tlsErr := generateTLS(vmName)
	if tlsErr != nil {
		err = fmt.Errorf("TLS generation: %w", tlsErr)
		return vmName, credSecretName, cloudInitSecretName, caCertPEM, err
	}
	caCertPEM = tls.CACertPEM

	// Resolve the Harvester VirtualMachineImage before creating any resources
	// so that a missing image causes an early error without leaving orphan Secrets.
	imgNs, imgName, imgSC, err := c.resolveVMImage(ctx, p.ImageName)
	if err != nil {
		return vmName, credSecretName, cloudInitSecretName, caCertPEM, err
	}

	// pg-<id>-credentials: long-lived Secret that holds the DB credentials and
	// TLS material. Shown-once fetch by dc-api; deleted after the fetch.
	credSecret := newUnstructured("v1", "Secret", credSecretName, p.Namespace)
	_ = unstructured.SetNestedField(credSecret.Object, "Opaque", "type")
	_ = unstructured.SetNestedField(credSecret.Object, map[string]any{
		"admin_user":        p.MasterUser,
		"admin_password":    adminPw,
		"repl_password":     replPw,
		"exporter_password": exporterPw,
		"ca_cert":           tls.CACertPEM,
		"ca_key":            tls.CAKeyPEM,
		"server_cert":       tls.ServerCertPEM,
		"server_key":        tls.ServerKeyPEM,
	}, "stringData")
	if _, e := c.Dynamic.Resource(secretGVR).Namespace(p.Namespace).Create(ctx, credSecret, metav1.CreateOptions{}); e != nil {
		if err = ignoreAlreadyExists(e); err != nil {
			return vmName, credSecretName, cloudInitSecretName, caCertPEM, err
		}
	}

	// pg-<id>-cloudinit: ephemeral Secret that holds cloud-init userdata and
	// networkdata. KubeVirt's cloudInitNoCloud datasource reads `userdata` and
	// `networkdata` from this Secret and feeds them to the VM at the init-local
	// stage — applying networkData early enough that systemd-networkd sees the
	// right IP/gateway/DNS before it times out. Deleted by the controller once
	// the VM reaches Available so the installation script and embedded passwords
	// are not left on-cluster indefinitely.
	cloudInit := buildCloudInit(p, adminPw, replPw, exporterPw, tls)
	networkData := buildNetworkData(p)
	cloudInitSecret := newUnstructured("v1", "Secret", cloudInitSecretName, p.Namespace)
	_ = unstructured.SetNestedField(cloudInitSecret.Object, "Opaque", "type")
	_ = unstructured.SetNestedField(cloudInitSecret.Object, map[string]any{
		"userdata":    cloudInit,
		"networkdata": networkData,
	}, "stringData")
	if _, e := c.Dynamic.Resource(secretGVR).Namespace(p.Namespace).Create(ctx, cloudInitSecret, metav1.CreateOptions{}); e != nil {
		if err = ignoreAlreadyExists(e); err != nil {
			return vmName, credSecretName, cloudInitSecretName, caCertPEM, err
		}
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
						"secretRef":            map[string]any{"name": cloudInitSecretName},
						"networkDataSecretRef": map[string]any{"name": cloudInitSecretName},
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
	return vmName, credSecretName, cloudInitSecretName, caCertPEM, err
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
		{pvcGVR, ns, refs.DataVolumeName},
		{secretGVR, ns, refs.SecretName},
		{secretGVR, ns, refs.CloudInitSecretName},
	}
	// OS disk cleanup. The original fixed-name DV (pg-<id>-os) is VM-owned and
	// cascade-deletes with the VM, but revision-suffixed disks from repaves —
	// and any PVC that outlived its DV (e.g. via CDI claim adoption) — need
	// explicit deletion. Scan the namespace for this instance's OS disk names;
	// the exact-or-separator match (isOSDiskName) avoids collisions with
	// instances whose name shares the prefix.
	osPrefix := fmt.Sprintf("pg-%s-os", id)
	osDiskNames := map[string]bool{osPrefix: true}
	if list, err := c.Dynamic.Resource(pvcGVR).Namespace(ns).List(ctx, metav1.ListOptions{}); err == nil {
		for i := range list.Items {
			if name := list.Items[i].GetName(); isOSDiskName(name, osPrefix) {
				osDiskNames[name] = true
			}
		}
	}
	for name := range osDiskNames {
		tasks = append(tasks,
			deleteTask{dvGVR, ns, name},
			deleteTask{pvcGVR, ns, name},
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

func (c *Client) PrepareCloudInitForRepave(ctx context.Context, p VMCreateParams, vmName, credSecretName, cloudInitSecretName string) error {
	secret, err := c.Dynamic.Resource(secretGVR).Namespace(p.Namespace).Get(ctx, credSecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get credentials Secret %s/%s for repave cloud-init: %w", p.Namespace, credSecretName, err)
	}
	get := func(key string) (string, error) {
		if v, ok, _ := unstructured.NestedString(secret.Object, "data", key); ok {
			b, err := base64.StdEncoding.DecodeString(v)
			if err != nil {
				return "", fmt.Errorf("decode credentials Secret key %q: %w", key, err)
			}
			return string(b), nil
		}
		if v, ok, _ := unstructured.NestedString(secret.Object, "stringData", key); ok {
			return v, nil
		}
		return "", fmt.Errorf("credentials Secret %s/%s is missing key %q", p.Namespace, credSecretName, key)
	}
	adminPw, err := get("admin_password")
	if err != nil {
		return err
	}
	replPw, err := get("repl_password")
	if err != nil {
		return err
	}
	exporterPw, err := get("exporter_password")
	if err != nil {
		return err
	}
	caCert, err := get("ca_cert")
	if err != nil {
		return err
	}
	caKey, err := get("ca_key")
	if err != nil {
		return err
	}
	serverCert, err := get("server_cert")
	if err != nil {
		return err
	}
	serverKey, err := get("server_key")
	if err != nil {
		return err
	}
	tls := &TLSBundle{CACertPEM: caCert, CAKeyPEM: caKey, ServerCertPEM: serverCert, ServerKeyPEM: serverKey}

	cloudInitSecret := newUnstructured("v1", "Secret", cloudInitSecretName, p.Namespace)
	_ = unstructured.SetNestedField(cloudInitSecret.Object, "Opaque", "type")
	_ = unstructured.SetNestedField(cloudInitSecret.Object, map[string]any{
		"userdata":    buildCloudInit(p, adminPw, replPw, exporterPw, tls),
		"networkdata": buildNetworkData(p),
	}, "stringData")
	if _, err := c.Dynamic.Resource(secretGVR).Namespace(p.Namespace).Create(ctx, cloudInitSecret, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
		existing, getErr := c.Dynamic.Resource(secretGVR).Namespace(p.Namespace).Get(ctx, cloudInitSecretName, metav1.GetOptions{})
		if getErr != nil {
			return getErr
		}
		_ = unstructured.SetNestedField(existing.Object, map[string]any{
			"userdata":    buildCloudInit(p, adminPw, replPw, exporterPw, tls),
			"networkdata": buildNetworkData(p),
		}, "stringData")
		_, err = c.Dynamic.Resource(secretGVR).Namespace(p.Namespace).Update(ctx, existing, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
	}

	vm, err := c.Dynamic.Resource(vmGVR).Namespace(p.Namespace).Get(ctx, vmName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	disks, _, _ := unstructured.NestedSlice(vm.Object, "spec", "template", "spec", "domain", "devices", "disks")
	if !namedSliceContains(disks, "cloudinit") {
		disks = append(disks, map[string]any{"name": "cloudinit", "disk": map[string]any{"bus": "virtio"}})
		_ = unstructured.SetNestedSlice(vm.Object, disks, "spec", "template", "spec", "domain", "devices", "disks")
	}
	volumes, _, _ := unstructured.NestedSlice(vm.Object, "spec", "template", "spec", "volumes")
	cloudInitVolume := map[string]any{
		"name": "cloudinit",
		"cloudInitNoCloud": map[string]any{
			"secretRef":            map[string]any{"name": cloudInitSecretName},
			"networkDataSecretRef": map[string]any{"name": cloudInitSecretName},
		},
	}
	replaced := false
	for i := range volumes {
		if volume, ok := volumes[i].(map[string]any); ok && volume["name"] == "cloudinit" {
			volumes[i] = cloudInitVolume
			replaced = true
		}
	}
	if !replaced {
		volumes = append(volumes, cloudInitVolume)
	}
	_ = unstructured.SetNestedSlice(vm.Object, volumes, "spec", "template", "spec", "volumes")
	_, err = c.Dynamic.Resource(vmGVR).Namespace(p.Namespace).Update(ctx, vm, metav1.UpdateOptions{})
	return err
}

func namedSliceContains(items []any, name string) bool {
	for _, item := range items {
		m, ok := item.(map[string]any)
		if ok && m["name"] == name {
			return true
		}
	}
	return false
}

// ClearDataVolumeOwnerRef patches the DataVolume to remove all ownerReferences.
// This must be called before deleting the VM CR during repave so the pgdata
// DataVolume is not cascade-deleted by the garbage collector.
func (c *Client) ClearDataVolumeOwnerRef(ctx context.Context, ns, dvName string) error {
	patch := []byte(`{"metadata":{"ownerReferences":[]}}`)
	_, err := c.Dynamic.Resource(dvGVR).Namespace(ns).Patch(
		ctx, dvName, types.MergePatchType, patch, metav1.PatchOptions{},
	)
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// DeleteDataVolume deletes a DataVolume by name. NotFound is treated as success
// so the call is idempotent across reconcile retries.
func (c *Client) DeleteDataVolume(ctx context.Context, ns, dvName string) error {
	err := c.Dynamic.Resource(dvGVR).Namespace(ns).Delete(ctx, dvName, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// SwapVMOSDisk replaces the os-disk DataVolumeTemplate in the VM spec with a
// revision-suffixed one (pg-<id>-os-<rev>) backed by the new image's
// BackingImage StorageClass, and repoints the os-disk volume at it. It
// returns the name of the DataVolume it replaced ("" when the VM is already
// on the target disk) so the caller can delete the old disk afterwards —
// distinct old/new names are what make the swap race-free.
//
// The dynamic client builds VMs with real dataVolumeTemplates (unlike the
// typed client's Harvester volumeClaimTemplates annotation), so the swap here
// is a template replacement. NOTE: this client is legacy — kept correct and
// compiling until it is removed; the typed client is the real implementation.
func (c *Client) SwapVMOSDisk(ctx context.Context, ns, vmName, instID, imgRef string) (string, error) {
	imgNs, imgName, imgSC, err := c.resolveVMImage(ctx, imgRef)
	if err != nil {
		return "", fmt.Errorf("SwapVMOSDisk: resolve image %q: %w", imgRef, err)
	}
	osPrefix := fmt.Sprintf("pg-%s-os", instID)
	newDVName := osDiskNameForImage(instID, imgName)
	newTemplate := map[string]any{
		"apiVersion": "cdi.kubevirt.io/v1beta1",
		"kind":       "DataVolume",
		"metadata": map[string]any{
			"name": newDVName,
			"annotations": map[string]any{
				"harvesterhci.io/imageId": fmt.Sprintf("%s/%s", imgNs, imgName),
			},
		},
		"spec": map[string]any{
			"source": map[string]any{"blank": map[string]any{}},
			"pvc": map[string]any{
				"accessModes":      []any{"ReadWriteMany"},
				"volumeMode":       "Block",
				"storageClassName": imgSC,
				"resources": map[string]any{
					"requests": map[string]any{"storage": "20Gi"},
				},
			},
		},
	}

	vm, err := c.Dynamic.Resource(vmGVR).Namespace(ns).Get(ctx, vmName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("SwapVMOSDisk: get VM %s/%s: %w", ns, vmName, err)
	}

	oldDVName := ""
	templates, _, _ := unstructured.NestedSlice(vm.Object, "spec", "dataVolumeTemplates")
	replaced := false
	for i := range templates {
		tpl, ok := templates[i].(map[string]any)
		if !ok {
			continue
		}
		name, _, _ := unstructured.NestedString(tpl, "metadata", "name")
		if !isOSDiskName(name, osPrefix) {
			continue
		}
		if name == newDVName {
			// Already swapped (re-entered after a crash or requeue).
			return "", nil
		}
		oldDVName = name
		templates[i] = newTemplate
		replaced = true
		break
	}
	if !replaced {
		templates = append(templates, newTemplate)
	}
	if err := unstructured.SetNestedSlice(vm.Object, templates, "spec", "dataVolumeTemplates"); err != nil {
		return "", fmt.Errorf("SwapVMOSDisk: set dataVolumeTemplates: %w", err)
	}

	// Repoint the os-disk volume at the new DataVolume.
	volumes, _, _ := unstructured.NestedSlice(vm.Object, "spec", "template", "spec", "volumes")
	for i := range volumes {
		vol, ok := volumes[i].(map[string]any)
		if !ok {
			continue
		}
		dv, ok := vol["dataVolume"].(map[string]any)
		if !ok {
			continue
		}
		if name, _ := dv["name"].(string); isOSDiskName(name, osPrefix) {
			dv["name"] = newDVName
		}
	}
	if err := unstructured.SetNestedSlice(vm.Object, volumes, "spec", "template", "spec", "volumes"); err != nil {
		return "", fmt.Errorf("SwapVMOSDisk: set volumes: %w", err)
	}

	if _, err := c.Dynamic.Resource(vmGVR).Namespace(ns).Update(ctx, vm, metav1.UpdateOptions{}); err != nil {
		return "", fmt.Errorf("SwapVMOSDisk: update VM %s/%s: %w", ns, vmName, err)
	}
	return oldDVName, nil
}

// DeletePVC deletes a PersistentVolumeClaim by name, ignoring NotFound.
func (c *Client) DeletePVC(ctx context.Context, ns, name string) error {
	err := c.Dynamic.Resource(pvcGVR).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

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

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.URLEncoding.EncodeToString(b)[:n]
}
