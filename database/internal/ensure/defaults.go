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
)

const (
	defaultOSImage     = "ubuntu-22.04-server-cloudimg-amd64.img"
	defaultStorageType = "longhorn"
	defaultMasterUser  = "dbadmin"
	defaultPort        = 5432
)

func specPort(port int) int {
	if port == 0 {
		return defaultPort
	}
	return port
}

func immutableDrift(inst *dbaasv1.DBInstance) string {
	applied := inst.Status.AppliedSpec
	if applied == nil {
		return ""
	}

	osImage := inst.Spec.OSImage
	if osImage == "" {
		osImage = defaultOSImage
	}
	dbName := inst.Spec.DBName
	if dbName == "" {
		dbName = inst.Name
	}
	masterUser := inst.Spec.MasterUsername
	if masterUser == "" {
		masterUser = defaultMasterUser
	}
	storageType := inst.Spec.StorageType
	if storageType == "" {
		storageType = defaultStorageType
	}
	appliedOSImage := applied.OSImage
	if appliedOSImage == "" {
		appliedOSImage = defaultOSImage
	}
	appliedDBName := applied.DBName
	if appliedDBName == "" {
		appliedDBName = inst.Name
	}
	appliedMasterUser := applied.MasterUsername
	if appliedMasterUser == "" {
		appliedMasterUser = defaultMasterUser
	}
	appliedPort := applied.Port
	if appliedPort == 0 {
		appliedPort = defaultPort
	}
	appliedStorageType := applied.StorageType
	if appliedStorageType == "" {
		appliedStorageType = defaultStorageType
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
	if appliedPort != specPort(inst.Spec.Port) {
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
