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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Setter is implemented by Kubernetes objects that expose mutable conditions.
type Setter interface {
	Getter
	SetConditions([]metav1.Condition)
}

// Set adds or updates a condition using Kubernetes condition transition
// semantics. Unlike Flux's setter, this intentionally does not change the
// condition's ObservedGeneration or sort the condition list.
func Set(to Setter, condition *metav1.Condition) {
	if to == nil || condition == nil {
		return
	}
	current := to.GetConditions()
	meta.SetStatusCondition(&current, *condition)
	to.SetConditions(current)
}

// Delete removes a condition by type.
func Delete(to Setter, conditionType string) {
	if to == nil {
		return
	}
	current := to.GetConditions()
	meta.RemoveStatusCondition(&current, conditionType)
	to.SetConditions(current)
}

func hasSameState(left, right *metav1.Condition) bool {
	return left != nil &&
		right != nil &&
		left.Type == right.Type &&
		left.Status == right.Status &&
		left.Reason == right.Reason &&
		left.Message == right.Message
}
