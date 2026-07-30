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

package catalog

import "testing"

func TestLatestBakedImagesSeededStreamsResolve(t *testing.T) {
	for _, osVersion := range []string{"22.04", "24.04"} {
		stream, ok := LatestBakedImages[osVersion]
		if !ok {
			t.Fatalf("LatestBakedImages[%q] missing, want a seeded stream", osVersion)
		}
		if stream.Revision == "" {
			t.Fatalf("LatestBakedImages[%q].Revision is empty", osVersion)
		}
		if _, ok := BakedImages[stream.Revision]; !ok {
			t.Fatalf("LatestBakedImages[%q] points at revision %q, which is not in BakedImages",
				osVersion, stream.Revision)
		}
	}
}

// TestLatestBakedImagesSeededStreamsArePending guards against seeding a
// placeholder revision as Validated: no Packer build has been produced or
// uploaded to Harvester yet, so nothing here is actually safe to resolve.
// This test should start failing the moment Task 10 legitimately validates
// a real, imported image — at which point delete or narrow this test rather
// than "fixing" it back to green.
func TestLatestBakedImagesSeededStreamsArePending(t *testing.T) {
	for osVersion, stream := range LatestBakedImages {
		if stream.ValidationState != ValidationPending {
			t.Fatalf("LatestBakedImages[%q].ValidationState = %q, want %q (no image has been built/uploaded yet)",
				osVersion, stream.ValidationState, ValidationPending)
		}
	}
}

func TestBakedImagesEntriesAreSelfConsistent(t *testing.T) {
	for name, entry := range BakedImages {
		if entry.ImageName != name {
			t.Fatalf("BakedImages[%q].ImageName = %q, want it to match its own map key "+
				"(ResolveVMImage is called with ImageName, so a mismatch here silently resolves the wrong image)",
				name, entry.ImageName)
		}
		if entry.OSVersion == "" {
			t.Fatalf("BakedImages[%q].OSVersion is empty", name)
		}
		if len(entry.SupportedEngineVersions) == 0 {
			t.Fatalf("BakedImages[%q].SupportedEngineVersions is empty", name)
		}
	}
}
