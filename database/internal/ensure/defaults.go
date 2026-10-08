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

package ensure

import (
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/api/equality"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/catalog"
	operatorconfig "github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/config"
)

func specPortWithDefault(port, configuredDefault int) int {
	if port == 0 {
		return configuredDefault
	}
	return port
}

// effectiveEngineVersion resolves spec.EngineVersion to the concrete version
// pg_createcluster boots with, falling back to entry.DefaultEngineVersion
// when unset (spec.engineVersion was never enforced pre-catalog, so this
// stays lenient rather than rejecting existing instances). ok is false when
// there's nothing valid to resolve to. Shared by preflight/vm/repave so they
// can't drift on this independently.
func effectiveEngineVersion(specEngineVersion string, entry catalog.BakedImageEntry) (version string, ok bool) {
	if specEngineVersion != "" {
		return specEngineVersion, slices.Contains(entry.SupportedEngineVersions, specEngineVersion)
	}
	if entry.DefaultEngineVersion == "" {
		return "", false
	}
	return entry.DefaultEngineVersion, true
}

// EffectiveSettings is what an instance actually runs with, for the settings
// a restore inherits — as opposed to its spec as written, which leaves every
// defaulted field empty.
type EffectiveSettings struct {
	DBName         string
	MasterUsername string
	EngineVersion  string
	Port           int
	StorageType    string
}

// EffectiveSettingsFor prefers recorded AppliedSpec values, falling back to
// spec and current operator defaults for settings not yet recorded. This
// preserves provisioned settings when operator defaults change.
// An omitted engine version resolves from the recorded image revision, or
// remains empty if that revision is unknown.
//
// TODO(defaulting-webhook): Persist dynamic defaults at creation to avoid
// recomputing values from operator config, instance name, and image catalog.
func EffectiveSettingsFor(inst *dbaasv1.DBInstance, defaults operatorconfig.DatabaseDefaults) EffectiveSettings {
	defaults = withBuiltInDatabaseDefaults(defaults)
	s := EffectiveSettings{
		DBName:         inst.EffectiveDBName(),
		MasterUsername: inst.Spec.MasterUsername,
		Port:           specPortWithDefault(inst.Spec.Port, defaults.Port),
		StorageType:    inst.Spec.StorageType,
		EngineVersion:  inst.Spec.EngineVersion,
	}
	if s.MasterUsername == "" {
		s.MasterUsername = defaults.MasterUsername
	}
	if s.StorageType == "" {
		s.StorageType = defaults.StorageClass
	}
	if a := inst.Status.AppliedSpec; a != nil {
		if a.DBName != "" {
			s.DBName = a.DBName
		}
		if a.MasterUsername != "" {
			s.MasterUsername = a.MasterUsername
		}
		if a.Port != 0 {
			s.Port = a.Port
		}
		if a.StorageType != "" {
			s.StorageType = a.StorageType
		}
	}
	if s.EngineVersion == "" {
		if entry, ok := catalog.BakedImages[inst.Status.CurrentImageRevision]; ok {
			s.EngineVersion = entry.DefaultEngineVersion
		}
	}
	return s
}

// resolveBakedImage looks up the current validated revision for
// defaults.OSVersion and its catalog entry. ok is false when the stream is
// unknown, not yet Validated, or points at a revision missing from
// BakedImages (the two maps are hand-maintained together; this guards
// against them drifting out of sync) — callers treat that as "nothing to
// resolve against yet" and reject Terminal (preflight/vm) or no-op (repave).
// Catalog data is compiled into the binary, so this can only ever change via
// a rebuild+redeploy; there's no live state a caller could usefully wait out,
// so ok collapses every non-Validated case into one signal.
func resolveBakedImage(defaults operatorconfig.DatabaseDefaults) (entry catalog.BakedImageEntry, stream catalog.BakedImageStream, ok bool) {
	stream, found := catalog.LatestBakedImages[defaults.OSVersion]
	if !found || stream.ValidationState != catalog.ValidationValidated {
		return catalog.BakedImageEntry{}, catalog.BakedImageStream{}, false
	}
	entry, found = catalog.BakedImages[stream.Revision]
	if !found {
		return catalog.BakedImageEntry{}, catalog.BakedImageStream{}, false
	}
	return entry, stream, true
}

func immutableDriftWithDefaults(inst *dbaasv1.DBInstance, defaults operatorconfig.DatabaseDefaults) string {
	applied := inst.Status.AppliedSpec
	if applied == nil {
		return ""
	}

	dbName := inst.EffectiveDBName()
	masterUser := inst.Spec.MasterUsername
	if masterUser == "" {
		masterUser = defaults.MasterUsername
	}
	storageType := inst.Spec.StorageType
	if storageType == "" {
		storageType = defaults.StorageClass
	}
	appliedDBName := applied.DBName
	if appliedDBName == "" {
		appliedDBName = dbaasv1.DefaultDBName(inst.Name)
	}
	appliedMasterUser := applied.MasterUsername
	if appliedMasterUser == "" {
		appliedMasterUser = defaults.MasterUsername
	}
	appliedPort := applied.Port
	if appliedPort == 0 {
		appliedPort = defaults.Port
	}
	appliedStorageType := applied.StorageType
	if appliedStorageType == "" {
		appliedStorageType = defaults.StorageClass
	}

	var changed []string
	if applied.NetworkRef != inst.Spec.NetworkRef {
		changed = append(changed, "networkRef")
	}
	if appliedDBName != dbName {
		changed = append(changed, "dbName")
	}
	if appliedMasterUser != masterUser {
		changed = append(changed, "masterUsername")
	}
	if applied.EngineVersion != inst.Spec.EngineVersion {
		changed = append(changed, "engineVersion")
	}
	if appliedPort != specPortWithDefault(inst.Spec.Port, defaults.Port) {
		changed = append(changed, "port")
	}
	if appliedStorageType != storageType {
		changed = append(changed, "storageType")
	}
	if applied.VMPassword != inst.Spec.VMPassword {
		changed = append(changed, "vmPassword")
	}
	if !equality.Semantic.DeepEqual(applied.StaticNetwork, inst.Spec.StaticNetwork) {
		changed = append(changed, "staticNetwork")
	}
	return strings.Join(changed, ",")
}
