/*
Copyright 2020 The Kubernetes Authors.
Copyright 2021 The Flux authors.
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

Adapted from github.com/fluxcd/pkg/runtime/patch/utils.go at v0.111.0.
*/

package patch

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

var preserveUnstructuredKeys = map[string]bool{
	"kind":       true,
	"apiVersion": true,
	"metadata":   true,
}

// toUnstructured converts an object to an independent unstructured value.
func toUnstructured(obj runtime.Object) (*unstructured.Unstructured, error) {
	if _, ok := obj.(runtime.Unstructured); ok {
		obj = obj.DeepCopyObject()
	}
	rawMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}
	return &unstructured.Unstructured{Object: rawMap}, nil
}

// focusedStatusCopy keeps identity metadata and status, and excludes conditions
// when they are managed by the condition transaction.
//
// Like the Flux helper this is a shallow copy. Removing conditions mutates the
// source status map, so condition patching must run before this helper is used.
func focusedStatusCopy(obj *unstructured.Unstructured, removeConditions bool) *unstructured.Unstructured {
	result := &unstructured.Unstructured{Object: make(map[string]interface{}, len(obj.Object))}
	for key, value := range obj.Object {
		if key == "status" || preserveUnstructuredKeys[key] {
			result.Object[key] = value
		}
	}
	if removeConditions {
		unstructured.RemoveNestedField(result.Object, "status", "conditions")
	}
	return result
}
