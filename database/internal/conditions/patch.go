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

Adapted from github.com/fluxcd/pkg/runtime/conditions/patch.go at v0.111.0.
*/

package conditions

import (
	"fmt"
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Patch defines a list of operations to change a list of conditions from "before" into "after".
type Patch []PatchOperation

// PatchOperation describes one condition add, change, or removal.
type PatchOperation struct {
	Before *metav1.Condition
	After  *metav1.Condition
	Op     PatchOperationType
}

// PatchOperationType identifies a condition patch operation.
type PatchOperationType string

const (
	AddConditionPatch    PatchOperationType = "Add"
	ChangeConditionPatch PatchOperationType = "Change"
	RemoveConditionPatch PatchOperationType = "Remove"
)

// NewPatch returns the operations required to align before with after.
func NewPatch(before, after Getter) Patch {
	var patch Patch
	afterConditions := after.GetConditions()
	for i := range afterConditions {
		target := afterConditions[i]
		current := Get(before, target.Type)
		if current == nil {
			patch = append(patch, PatchOperation{Op: AddConditionPatch, After: &target})
			continue
		}
		if !reflect.DeepEqual(&target, current) {
			patch = append(patch, PatchOperation{Op: ChangeConditionPatch, Before: current, After: &target})
		}
	}

	beforeConditions := before.GetConditions()
	for i := range beforeConditions {
		base := beforeConditions[i]
		if Get(after, base.Type) == nil {
			patch = append(patch, PatchOperation{Op: RemoveConditionPatch, Before: &base})
		}
	}
	return patch
}

// applyOptions allows to set strategies for patch apply.
type applyOptions struct {
	ownedConditions []string
}

func (o *applyOptions) isOwned(conditionType string) bool {
	for _, owned := range o.ownedConditions {
		if owned == conditionType {
			return true
		}
	}
	return false
}

// ApplyOption configures three-way condition merge behavior.
type ApplyOption func(*applyOptions)

// WithOwnedConditions makes the reconciled value authoritative for the given
// condition types.
func WithOwnedConditions(conditionTypes ...string) ApplyOption {
	return func(options *applyOptions) {
		options.ownedConditions = conditionTypes
	}
}

// Apply merges the saved operations into latest. Concurrent changes to
// unowned condition types are reported instead of overwritten.
func (patch Patch) Apply(latest Setter, options ...ApplyOption) error {
	if len(patch) == 0 {
		return nil
	}

	applyOpts := &applyOptions{}
	for _, option := range options {
		option(applyOpts)
	}

	for _, operation := range patch {
		switch operation.Op {
		case AddConditionPatch:
			// If we owned the condition , always keep the after value.
			if applyOpts.isOwned(operation.After.Type) {
				Set(latest, operation.After)
				continue
			}
			// If the condition already exists on latest, check that it has the same value as the after value. If it does not, report a concurrent change.
			if current := Get(latest, operation.After.Type); current != nil {
				if !hasSameState(current, operation.After) {
					return fmt.Errorf("condition %q was concurrently added with a different value by another process", operation.After.Type)
				}
				// condition already exists and both have the same value
				continue
			}
			// If the condition does not exist on latest, add it.
			Set(latest, operation.After)

		case ChangeConditionPatch:
			// Owned condition → our After value wins.
			if applyOpts.isOwned(operation.After.Type) {
				Set(latest, operation.After)
				continue
			}
			current := Get(latest, operation.After.Type)

			// Unowned + deleted concurrently → error.
			if current == nil {
				return fmt.Errorf("condition %q was concurrently deleted", operation.After.Type)
			}
			if !reflect.DeepEqual(current, operation.Before) {
				// Unowned + changed to a different value concurrently → error.
				if !hasSameState(current, operation.After) {
					return fmt.Errorf("condition %q was concurrently changed to a different value", operation.After.Type)
				}
				// Unowned + already changed to our desired value → no-op success.
				continue
			}
			// Unowned + unchanged since our cache read → safely apply After.
			Set(latest, operation.After)

		case RemoveConditionPatch:
			// Owned condition → delete it regardless of concurrent changes.
			if applyOpts.isOwned(operation.Before.Type) {
				Delete(latest, operation.Before.Type)
				continue
			}

			current := Get(latest, operation.Before.Type)
			// Unowned + already absent → no-op success.
			if current == nil {
				continue
			}
			// Unowned + changed concurrently → error.
			if !hasSameState(current, operation.Before) {
				return fmt.Errorf("condition %q was concurrently changed before removal", operation.Before.Type)
			}
			// Unowned + still matches Before → safely delete it.
			Delete(latest, operation.Before.Type)

		default:
			return fmt.Errorf("unsupported condition patch operation %q", operation.Op)
		}
	}
	return nil
}

// IsZero reports whether the patch has no operations.
func (patch Patch) IsZero() bool {
	return len(patch) == 0
}
