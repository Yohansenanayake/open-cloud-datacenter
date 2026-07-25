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

package controller

import (
	"testing"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	sharedconditions "github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/conditions"
)

var _ sharedconditions.Setter = (*dbaasv1.DBInstance)(nil)

func TestDBInstanceOwnedConditions(t *testing.T) {
	expected := []string{
		dbaasv1.ConditionAccepted,
		dbaasv1.ConditionPreflightReady,
		dbaasv1.ConditionCredentialsReady,
		dbaasv1.ConditionVMReady,
		dbaasv1.ConditionPowerStateReady,
		dbaasv1.ConditionStorageReady,
		dbaasv1.ConditionStorageChangeRejected,
		dbaasv1.ConditionResizeInProgress,
		dbaasv1.ConditionDatabaseReady,
		dbaasv1.ConditionMonitoringReady,
		dbaasv1.ConditionReady,
		dbaasv1.ConditionInterventionRequired,
		dbaasv1.ConditionCrashLoopHalted,
		dbaasv1.ConditionDegraded,
		dbaasv1.ConditionDeletionBlocked,
	}

	if len(dbInstanceOwnedConditions) != len(expected) {
		t.Fatalf("owned condition count = %d, want %d", len(dbInstanceOwnedConditions), len(expected))
	}
	seen := make(map[string]bool, len(dbInstanceOwnedConditions))
	for _, conditionType := range dbInstanceOwnedConditions {
		if seen[conditionType] {
			t.Fatalf("owned condition %q is duplicated", conditionType)
		}
		seen[conditionType] = true
	}
	for _, conditionType := range expected {
		if !seen[conditionType] {
			t.Errorf("condition %q is not owned by the DBInstance controller", conditionType)
		}
	}
}
