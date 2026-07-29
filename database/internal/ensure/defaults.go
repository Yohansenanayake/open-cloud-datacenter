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
	"strings"

	"k8s.io/apimachinery/pkg/api/equality"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	operatorconfig "github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/config"
)

func specPortWithDefault(port, configuredDefault int) int {
	if port == 0 {
		return configuredDefault
	}
	return port
}

func immutableDriftWithDefaults(inst *dbaasv1.DBInstance, defaults operatorconfig.DatabaseDefaults) string {
	applied := inst.Status.AppliedSpec
	if applied == nil {
		return ""
	}

	osImage := inst.Spec.OSImage
	if osImage == "" {
		osImage = defaults.OSImage
	}
	dbName := inst.Spec.DBName
	if dbName == "" {
		dbName = inst.Name
	}
	masterUser := inst.Spec.MasterUsername
	if masterUser == "" {
		masterUser = defaults.MasterUsername
	}
	storageType := inst.Spec.StorageType
	if storageType == "" {
		storageType = defaults.StorageClass
	}
	appliedOSImage := applied.OSImage
	if appliedOSImage == "" {
		appliedOSImage = defaults.OSImage
	}
	appliedDBName := applied.DBName
	if appliedDBName == "" {
		appliedDBName = inst.Name
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
	if appliedOSImage != osImage {
		changed = append(changed, "osImage")
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
