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

package testutil

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

func NewProvisionInstance() *dbaasv1.DBInstance {
	return &dbaasv1.DBInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "orders",
			Namespace:       "tenant-a",
			UID:             "orders-uid",
			Generation:      3,
			Finalizers:      []string{dbaasv1.FinalizerName},
			ResourceVersion: "1",
		},
		Spec: dbaasv1.DBInstanceSpec{
			DBInstanceClass:  "db.t3.small",
			AllocatedStorage: 20,
			NetworkRef:       "tenant-a/data-net",
		},
	}
}
