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

package v1alpha1

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// SetupDBInstanceWebhookWithManager registers the defaulting and validating webhooks.
func SetupDBInstanceWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&DBInstance{}).
		WithDefaulter(&DBInstanceDefaulter{}).
		WithValidator(&DBInstanceValidator{}).
		Complete()
}

// DBInstanceDefaulter sets engineVersion to the latest PG version in the current
// baked image stream when the user leaves the field empty.
type DBInstanceDefaulter struct{}

func (d *DBInstanceDefaulter) Default(_ context.Context, obj runtime.Object) error {
	inst, ok := obj.(*DBInstance)
	if !ok {
		return fmt.Errorf("expected *DBInstance, got %T", obj)
	}
	if inst.Spec.EngineVersion != "" {
		return nil
	}
	osVersion := os.Getenv("BACKING_IMAGE_OS_VERSION")
	stream, ok := LatestBakedImages[osVersion]
	if !ok || !stream.Validated {
		return nil
	}
	entry, ok := BakedImages[stream.Revision]
	if !ok {
		return nil
	}
	inst.Spec.EngineVersion = latestPGVersion(entry.PGVersions)
	return nil
}

// DBInstanceValidator rejects engineVersion values that are not in the current
// baked image catalog — catches EOL versions and unknown versions at admission.
type DBInstanceValidator struct{}

func (v *DBInstanceValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	inst, ok := obj.(*DBInstance)
	if !ok {
		return nil, fmt.Errorf("expected *DBInstance, got %T", obj)
	}
	return validateEngineVersion(inst)
}

func (v *DBInstanceValidator) ValidateUpdate(_ context.Context, _, newObj runtime.Object) (admission.Warnings, error) {
	inst, ok := newObj.(*DBInstance)
	if !ok {
		return nil, fmt.Errorf("expected *DBInstance, got %T", newObj)
	}
	return validateEngineVersion(inst)
}

func (v *DBInstanceValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func validateEngineVersion(inst *DBInstance) (admission.Warnings, error) {
	if inst.Spec.EngineVersion == "" {
		// Still empty after defaulting — no valid stream exists yet.
		// Let the controller surface a clear error at reconcile time.
		return nil, nil
	}
	osVersion := os.Getenv("BACKING_IMAGE_OS_VERSION")
	stream, ok := LatestBakedImages[osVersion]
	if !ok {
		return nil, fmt.Errorf("operator OS stream %q not configured in catalog; contact your administrator", osVersion)
	}
	if !stream.Validated {
		return nil, fmt.Errorf("OS stream %q is not yet validated for provisioning; contact your administrator", osVersion)
	}
	entry, ok := BakedImages[stream.Revision]
	if !ok {
		return nil, fmt.Errorf("image revision %q not found in catalog; contact your administrator", stream.Revision)
	}
	for _, v := range entry.PGVersions {
		if v == inst.Spec.EngineVersion {
			return nil, nil
		}
	}
	return nil, fmt.Errorf(
		"engineVersion %q is not supported in image %q (available: %v); "+
			"if this version has reached EOL it has been removed from the catalog",
		inst.Spec.EngineVersion, stream.Revision, entry.PGVersions,
	)
}

// latestPGVersion returns the highest PG major version number from the list.
func latestPGVersion(versions []string) string {
	best, bestN := "", -1
	for _, v := range versions {
		n, err := strconv.Atoi(v)
		if err != nil {
			continue
		}
		if n > bestN {
			bestN, best = n, v
		}
	}
	return best
}
