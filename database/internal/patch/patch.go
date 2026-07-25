/*
Copyright 2017 The Kubernetes Authors.
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

Adapted from github.com/fluxcd/pkg/runtime/patch/patch.go at v0.111.0.
*/

package patch

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/conditions"
)

// Helper calculates and persists status changes made after its construction.
type Helper struct {
	client       client.Client
	gvk          schema.GroupVersionKind
	beforeObject client.Object
	before       *unstructured.Unstructured
	after        *unstructured.Unstructured
	changes      map[string]bool

	isConditionsSetter bool
}

// NewHelper captures the object's current state as the patch base.
func NewHelper(obj client.Object, patchClient client.Client) (*Helper, error) {
	gvk, err := apiutil.GVKForObject(obj, patchClient.Scheme())
	if err != nil {
		return nil, err
	}
	before, err := toUnstructured(obj)
	if err != nil {
		return nil, err
	}
	_, supportsConditions := obj.(conditions.Setter)
	return &Helper{
		client:             patchClient,
		gvk:                gvk,
		beforeObject:       obj.DeepCopyObject().(client.Object),
		before:             before,
		isConditionsSetter: supportsConditions,
	}, nil
}

// Patch persists condition changes and then all remaining status changes.
func (helper *Helper) Patch(ctx context.Context, obj client.Object, options ...Option) error {
	gvk, err := apiutil.GVKForObject(obj, helper.client.Scheme())
	if err != nil {
		return err
	}
	if gvk != helper.gvk {
		return fmt.Errorf("unmatched GroupVersionKind: expected %q, got %q", helper.gvk, gvk)
	}

	patchOptions := &HelperOptions{}
	for _, option := range options {
		option.ApplyToHelper(patchOptions)
	}

	helper.after, err = toUnstructured(obj)
	if err != nil {
		return err
	}
	helper.changes, err = helper.calculateChanges(obj)
	if err != nil {
		return err
	}

	return kerrors.NewAggregate([]error{
		helper.patchStatusConditions(ctx, obj, patchOptions.OwnedConditions),
		helper.patchStatus(ctx, obj),
	})
}

func (helper *Helper) patchStatus(ctx context.Context, obj client.Object) error {
	if !helper.shouldPatch("status") {
		return nil
	}
	before, after, err := helper.calculateStatusPatch(obj)
	if err != nil {
		return err
	}
	return helper.client.Status().Patch(ctx, after, client.MergeFrom(before))
}

// patchStatusConditions performs a Flux-style condition transaction. It
// retries optimistic-lock conflicts while the informer cache catches up.
func (helper *Helper) patchStatusConditions(ctx context.Context, obj client.Object, ownedConditions []string) error {
	if !helper.isConditionsSetter {
		return nil
	}

	before, ok := helper.beforeObject.(conditions.Getter)
	if !ok {
		return fmt.Errorf("before object %T does not implement conditions.Getter", helper.beforeObject)
	}
	after, ok := obj.(conditions.Getter)
	if !ok {
		return fmt.Errorf("after object %T does not implement conditions.Getter", obj)
	}

	diff := conditions.NewPatch(before, after)
	if diff.IsZero() {
		return nil
	}

	key := client.ObjectKeyFromObject(after)
	backoff := wait.Backoff{
		Steps:    5,
		Duration: 100 * time.Millisecond,
		Jitter:   1.0,
	}

	return wait.ExponentialBackoff(backoff, func() (bool, error) {
		latest, ok := before.DeepCopyObject().(conditions.Setter)
		if !ok {
			return false, fmt.Errorf("object %T does not implement conditions.Setter", before)
		}
		if err := helper.client.Get(ctx, key, latest); err != nil {
			return false, err
		}

		base := latest.DeepCopyObject().(conditions.Setter)
		conditionPatch := client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})
		if err := diff.Apply(latest, conditions.WithOwnedConditions(ownedConditions...)); err != nil {
			return false, err
		}

		err := helper.client.Status().Patch(ctx, latest, conditionPatch)
		switch {
		case apierrors.IsConflict(err):
			return false, nil
		case err != nil:
			return false, err
		default:
			return true, nil
		}
	})
}

func (helper *Helper) calculateStatusPatch(afterObj client.Object) (client.Object, client.Object, error) {
	before := focusedStatusCopy(helper.before, helper.isConditionsSetter)
	after := focusedStatusCopy(helper.after, helper.isConditionsSetter)

	beforeObj := helper.beforeObject.DeepCopyObject().(client.Object)
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(before.Object, beforeObj); err != nil {
		return nil, nil, err
	}
	afterObj = afterObj.DeepCopyObject().(client.Object)
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(after.Object, afterObj); err != nil {
		return nil, nil, err
	}
	return beforeObj, afterObj, nil
}

func (helper *Helper) shouldPatch(field string) bool {
	return helper.changes[field]
}

func (helper *Helper) calculateChanges(after client.Object) (map[string]bool, error) {
	mergePatch := client.MergeFrom(helper.beforeObject)
	diff, err := mergePatch.Data(after)
	if err != nil {
		return nil, fmt.Errorf("calculate patch data: %w", err)
	}
	patchDiff := map[string]interface{}{}
	if err := json.Unmarshal(diff, &patchDiff); err != nil {
		return nil, fmt.Errorf("decode patch data: %w", err)
	}
	changes := make(map[string]bool, len(patchDiff))
	for key := range patchDiff {
		changes[key] = true
	}
	return changes, nil
}
