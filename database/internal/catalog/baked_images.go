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

// Package catalog holds the compiled-in registry of baked PostgreSQL VM
// images. It is intentionally separate from api/v1alpha1: these types are
// never part of the DBInstance wire API (no CRD field or status ever
// exposes them), so they don't belong in the package controller-gen scans
// for CRD schema and deepcopy generation.
package catalog

// BakedImageEntry describes one registered Harvester image revision.
type BakedImageEntry struct {
	// ImageName is the Harvester VirtualMachineImage name/displayName,
	// resolved via harvester.ClientInterface.ResolveVMImage.
	ImageName string
	// OSVersion is the stream key this entry belongs under, e.g. "24.04".
	OSVersion string
	// SupportedEngineVersions are the spec.engineVersion values this
	// revision supports.
	SupportedEngineVersions []string
}

// BakedImageStream is the currently-active revision for one OS stream.
type BakedImageStream struct {
	// Revision is the key into BakedImages.
	Revision string
	// ValidationState gates whether this revision may be resolved by
	// preflight/repave.
	ValidationState ValidationState
}

// ValidationState is the smoke-test state of a BakedImageStream's current
// revision. Only "Validated" streams are ever resolved by preflight/repave;
// "Pending" covers everything else not yet safe to use, including a build
// that failed testing — a failed build is removed from the catalog rather
// than marked, so there's nothing to distinguish it from "not tested yet".
type ValidationState string

const (
	ValidationPending   ValidationState = "Pending"
	ValidationValidated ValidationState = "Validated"
)

// BakedImages is the registry of every known image revision, keyed by
// revision name. Entries are added here once a Packer build has actually
// been produced and uploaded to Harvester (see database/images/packer/) —
// registering a name here does not itself make an image usable; the
// LatestBakedImages entry pointing at it must also be Validated.
var BakedImages = map[string]BakedImageEntry{
	"ubuntu-2204-postgres-v20260515": {
		ImageName:               "ubuntu-2204-postgres-v20260515",
		OSVersion:               "22.04",
		SupportedEngineVersions: []string{"15", "16", "17"},
	},
	"ubuntu-2404-postgres-v20260701": {
		ImageName:               "ubuntu-2404-postgres-v20260701",
		OSVersion:               "24.04",
		SupportedEngineVersions: []string{"15", "16", "17", "18"},
	},
	"ubuntu-2404-postgres-v20260815": {
		ImageName:               "ubuntu-2404-postgres-v20260815",
		OSVersion:               "24.04",
		SupportedEngineVersions: []string{"18"},
	},
}

// LatestBakedImages is the currently-active revision for each OS stream.
//
// Both entries below are seeded Pending, not Validated: as of this
// revision, no Packer build has been produced or uploaded to Harvester for
// either stream (database/images/packer/ is still empty). Flip a stream to
// ValidationValidated only after a real image has been built, imported,
// and manually smoke-tested — never as a placeholder. Until then, preflight
// and repave both treat every stream here as unusable and no-op, exactly
// as if the catalog were empty.
var LatestBakedImages = map[string]BakedImageStream{
	"22.04": {Revision: "ubuntu-2204-postgres-v20260515", ValidationState: ValidationValidated},
	"24.04": {Revision: "ubuntu-2404-postgres-v20260701", ValidationState: ValidationValidated},
}

// RevisionForImageName reverse-looks-up the BakedImages revision key whose
// ImageName matches an observed Harvester VirtualMachineImage name.
func RevisionForImageName(imageName string) (revision string, ok bool) {
	for rev, entry := range BakedImages {
		if entry.ImageName == imageName {
			return rev, true
		}
	}
	return "", false
}
