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
	"encoding/base64"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

func TestCreatePostgresVMDoesNotCreateSecretWhenImageResolutionFails(t *testing.T) {
	ctx := context.Background()
	// client without the fake image — image resolution fails before any Secret is created
	client := newTestHarvesterClient()

	_, credSecretName, cloudInitSecretName, _, err := client.CreatePostgresVM(ctx, testVMCreateParams())
	if err == nil {
		t.Fatalf("CreatePostgresVM returned nil error, want image resolution error")
	}

	// Neither Secret should exist after an image-resolution failure.
	if _, err := client.Dynamic.Resource(secretGVR).Namespace("tenant-a").Get(ctx, credSecretName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("credentials Secret should not exist after image-resolution failure, got: %v", err)
	}
	if _, err := client.Dynamic.Resource(secretGVR).Namespace("tenant-a").Get(ctx, cloudInitSecretName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("cloudinit Secret should not exist after image-resolution failure, got: %v", err)
	}
}

func TestCreatePostgresVMCreatesBothSecretsAndReturnsCA(t *testing.T) {
	ctx := context.Background()
	client := newTestHarvesterClient(testVMImage())

	vmName, credSecretName, cloudInitSecretName, caCertPEM, err := client.CreatePostgresVM(ctx, testVMCreateParams())
	if err != nil {
		t.Fatalf("CreatePostgresVM returned error: %v", err)
	}
	if vmName != "pg-orders" {
		t.Fatalf("VM name = %q, want pg-orders", vmName)
	}
	if credSecretName != "pg-orders-credentials" {
		t.Fatalf("credentials Secret name = %q, want pg-orders-credentials", credSecretName)
	}
	if cloudInitSecretName != "pg-orders-cloudinit" {
		t.Fatalf("cloudinit Secret name = %q, want pg-orders-cloudinit", cloudInitSecretName)
	}
	if caCertPEM == "" {
		t.Fatalf("CA cert is empty")
	}

	// credentials Secret must exist and contain the CA.
	// Fake client stores stringData as-is (no base64 encoding like the real API server).
	credSecret, err := client.Dynamic.Resource(secretGVR).Namespace("tenant-a").Get(ctx, credSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get credentials Secret: %v", err)
	}
	secretCA, _, _ := unstructured.NestedString(credSecret.Object, "stringData", "ca_cert")
	if secretCA == "" {
		t.Fatalf("credentials Secret has no stringData.ca_cert")
	}
	if caCertPEM != secretCA {
		t.Fatalf("returned CA does not match Secret CA")
	}

	// cloudinit Secret must exist and contain userdata.
	cloudInitSecret, err := client.Dynamic.Resource(secretGVR).Namespace("tenant-a").Get(ctx, cloudInitSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get cloudinit Secret: %v", err)
	}
	userdata, _, _ := unstructured.NestedString(cloudInitSecret.Object, "stringData", "userdata")
	if userdata == "" {
		t.Fatalf("cloudinit Secret has no stringData.userdata")
	}
	if !strings.Contains(userdata, "systemctl enable postgresql") {
		t.Fatalf("cloudinit userdata does not enable postgresql for reboot survival")
	}

	// VM must exist.
	if _, err := client.Dynamic.Resource(vmGVR).Namespace("tenant-a").Get(ctx, vmName, metav1.GetOptions{}); err != nil {
		t.Fatalf("get created VM: %v", err)
	}
}

func TestCreatePostgresVMPreservesKubeOVNSettings(t *testing.T) {
	ctx := context.Background()
	client := newTestHarvesterClient(testVMImage())
	client.MgmtLogicalSwitch = "ovn-default"
	params := testVMCreateParams()
	params.DNSServerIP = "10.96.0.10/32"

	vmName, _, _, _, err := client.CreatePostgresVM(ctx, params)
	if err != nil {
		t.Fatalf("CreatePostgresVM returned error: %v", err)
	}
	vm, err := client.Dynamic.Resource(vmGVR).Namespace("tenant-a").Get(ctx, vmName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get created VM: %v", err)
	}

	logicalSwitch, _, _ := unstructured.NestedString(vm.Object, "spec", "template", "metadata", "annotations", "ovn.kubernetes.io/logical_switch")
	if logicalSwitch != "ovn-default" {
		t.Fatalf("logical switch annotation = %q, want ovn-default", logicalSwitch)
	}
	dnsPolicy, _, _ := unstructured.NestedString(vm.Object, "spec", "template", "spec", "dnsPolicy")
	if dnsPolicy != "None" {
		t.Fatalf("dnsPolicy = %q, want None", dnsPolicy)
	}
	nameservers, _, _ := unstructured.NestedStringSlice(vm.Object, "spec", "template", "spec", "dnsConfig", "nameservers")
	if len(nameservers) != 1 || nameservers[0] != "10.96.0.10" {
		t.Fatalf("nameservers = %v, want [10.96.0.10]", nameservers)
	}
}

func TestDeployMonitoringIsIdempotent(t *testing.T) {
	client := NewClient(fake.NewSimpleDynamicClient(runtime.NewScheme()), "https://grafana.example")

	for i := range 2 {
		svcName, smName, grafanaURL, promTarget, err := client.DeployMonitoring(context.Background(), "orders", "tenant-a", "192.168.40.50")
		if err != nil {
			t.Fatalf("DeployMonitoring call %d returned error: %v", i+1, err)
		}
		if svcName != "pg-orders-metrics" {
			t.Fatalf("service name = %q, want pg-orders-metrics", svcName)
		}
		if smName != "pg-orders-monitor" {
			t.Fatalf("ServiceMonitor name = %q, want pg-orders-monitor", smName)
		}
		if grafanaURL != "https://grafana.example/d/dbaas-orders/postgresql-orders" {
			t.Fatalf("Grafana URL = %q", grafanaURL)
		}
		if promTarget != "pg-orders-metrics.tenant-a.svc:9187" {
			t.Fatalf("Prometheus target = %q", promTarget)
		}
	}

	svc, err := client.Dynamic.Resource(serviceGVR).Namespace("tenant-a").Get(context.Background(), "pg-orders-metrics", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Service: %v", err)
	}
	if selector, found, err := unstructured.NestedMap(svc.Object, "spec", "selector"); err != nil || found || len(selector) != 0 {
		t.Fatalf("Service selector = %v, found=%t, err=%v; want no selector", selector, found, err)
	}

	ep, err := client.Dynamic.Resource(endpointsGVR).Namespace("tenant-a").Get(context.Background(), "pg-orders-metrics", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Endpoints: %v", err)
	}
	subsets, found, err := unstructured.NestedSlice(ep.Object, "subsets")
	if err != nil || !found || len(subsets) != 1 {
		t.Fatalf("Endpoint subsets = %v, found=%t, err=%v", subsets, found, err)
	}
	subset := subsets[0].(map[string]any)
	addresses := subset["addresses"].([]any)
	address := addresses[0].(map[string]any)
	if address["ip"] != "192.168.40.50" {
		t.Fatalf("Endpoint IP = %v, want 192.168.40.50", address["ip"])
	}
}

// Helpers
func newTestHarvesterClient(objs ...runtime.Object) *Client {
	listKinds := map[schema.GroupVersionResource]string{
		vmImageGVR: "VirtualMachineImageList",
	}
	return NewClient(fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, objs...), "")
}

func testVMCreateParams() VMCreateParams {
	return VMCreateParams{
		ID:                     "orders",
		Namespace:              "tenant-a",
		CPUCores:               2,
		MemoryMB:               4096,
		ImageName:              "ubuntu-2204-postgres-v20260615",
		EngineVersion:          "16",
		DataVolumeRef:          "pg-orders-data",
		DataVolumeSizeGB:       20,
		DataVolumeStorageClass: "harvester-longhorn",
		NADName:                "tenant-a/vm-network",
		MasterUser:             "dbadmin",
		DBName:                 "orders",
		Port:                   5432,
		MaxConnections:         100,
	}
}

func testVMImage() *unstructured.Unstructured {
	img := newUnstructured("harvesterhci.io/v1beta1", "VirtualMachineImage", "ubuntu-2204-postgres-v20260615", "default")
	// set status.storageClassName to indicate the fake VM Image is ready
	_ = unstructured.SetNestedField(img.Object, "longhorn-image-ubuntu", "status", "storageClassName")
	return img
}

func testCredentialSecret(caCert string) *unstructured.Unstructured {
	secret := newUnstructured("v1", "Secret", "pg-orders-credentials", "tenant-a")
	_ = unstructured.SetNestedField(secret.Object, "Opaque", "type")
	_ = unstructured.SetNestedStringMap(secret.Object, map[string]string{
		"ca_cert": base64.StdEncoding.EncodeToString([]byte(caCert)),
	}, "data")
	return secret
}
