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

package testutil

import (
	"encoding/json"
	"fmt"

	"github.com/harvester/harvester/pkg/util"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtv1 "kubevirt.io/api/core/v1"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

func VM(name, namespace string) *kubevirtv1.VirtualMachine {
	return ShapedVM(name, namespace, "db.t3.small", 20, name+"-data", kubevirtv1.RunStrategyAlways)
}

func ShapedVM(name, namespace, class string, storageGB int, dataVolumeName string, strategy kubevirtv1.VirtualMachineRunStrategy) *kubevirtv1.VirtualMachine {
	classSpec := dbaasv1.InstanceClasses[class]
	pvcs := []*corev1.PersistentVolumeClaim{{
		ObjectMeta: metav1.ObjectMeta{Name: dataVolumeName},
		Spec: corev1.PersistentVolumeClaimSpec{
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(fmt.Sprintf("%dGi", storageGB)),
				},
			},
		},
	}}
	raw, _ := json.Marshal(pvcs)

	vm := &kubevirtv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Annotations: map[string]string{util.AnnotationVolumeClaimTemplates: string(raw)},
		},
	}
	vm.Spec.RunStrategy = &strategy
	vm.Spec.Template = &kubevirtv1.VirtualMachineInstanceTemplateSpec{}
	vm.Spec.Template.Spec.Domain.Resources.Limits = corev1.ResourceList{
		corev1.ResourceCPU:    *resource.NewQuantity(int64(classSpec.CPUCores), resource.DecimalSI),
		corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", classSpec.MemoryMB)),
	}
	return vm
}
