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

// Package resource holds the declarative-child builders: owned Kubernetes
// objects (metrics Service/Endpoints, ServiceMonitor; Secrets from PR8 on) that
// the controller reconciles to a desired shape with CreateOrUpdate and
// controller owner references. Builders apply ONLY to owned declarative
// children — the VirtualMachine and other stateful/transitional resources stay
// with the provider and their ensure steps.
package resource

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// Builder describes one owned declarative child.
type Builder interface {
	// Build returns the object's identity only: type, name, namespace. Desired
	// fields belong in Update so they are asserted on both create and update.
	Build() (client.Object, error)
	// Update asserts the owned fields (spec fields we manage, labels) on the
	// object. It must not clobber server-owned or user-owned fields — e.g. a
	// Service's allocated clusterIP.
	Update(obj client.Object) error
}

// Apply reconciles the builder's object via CreateOrUpdate, asserting the owned
// fields and a controller owner reference on every pass. Same-namespace children
// only (SetControllerReference rejects cross-namespace owners); the owner ref
// makes Owns() watches live and lets GC back up the finalizer teardown.
func Apply(ctx context.Context, c client.Client, scheme *runtime.Scheme, owner client.Object, b Builder) (controllerutil.OperationResult, error) {
	obj, err := b.Build()
	if err != nil {
		return controllerutil.OperationResultNone, err
	}
	return controllerutil.CreateOrUpdate(ctx, c, obj, func() error {
		if err := b.Update(obj); err != nil {
			return err
		}
		return controllerutil.SetControllerReference(owner, obj, scheme)
	})
}

// wrongTypeErr standardises the Update type-assertion failure message.
func wrongTypeErr(builder string, obj client.Object) error {
	return fmt.Errorf("%s builder: unexpected object type %T", builder, obj)
}
