/*
Copyright 2022 The Flux authors.
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

Adapted from github.com/fluxcd/pkg/runtime/patch/serial.go at v0.111.0.
*/

package patch

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SerialPatcher remembers the last successfully patched object and uses it as
// the base for the next status patch.
type SerialPatcher struct {
	client       client.Client
	beforeObject client.Object
}

// NewSerialPatcher initializes a serial patcher from the current object.
func NewSerialPatcher(obj client.Object, patchClient client.Client) *SerialPatcher {
	return &SerialPatcher{
		client:       patchClient,
		beforeObject: obj.DeepCopyObject().(client.Object),
	}
}

// Patch persists status changes and advances the base only after all patch
// operations succeed.
func (patcher *SerialPatcher) Patch(ctx context.Context, obj client.Object, options ...Option) error {
	helper, err := NewHelper(patcher.beforeObject, patcher.client)
	if err != nil {
		return err
	}
	if err := helper.Patch(ctx, obj, options...); err != nil {
		return err
	}
	// Advance to the successfully persisted local state; this is not a fresh server snapshot.
	patcher.beforeObject = obj.DeepCopyObject().(client.Object)
	return nil
}
