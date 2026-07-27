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

Adapted from github.com/fluxcd/pkg/runtime/conditions at v0.111.0.
*/

package conditions

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Getter is implemented by Kubernetes objects that expose metav1 conditions.
type Getter interface {
	client.Object
	GetConditions() []metav1.Condition
}

// Get returns the condition with the given type, or nil when it is absent.
func Get(from Getter, conditionType string) *metav1.Condition {
	if from == nil {
		return nil
	}
	conditionList := from.GetConditions()
	for i := range conditionList {
		condition := conditionList[i]
		if condition.Type == conditionType {
			return &condition
		}
	}
	return nil
}
