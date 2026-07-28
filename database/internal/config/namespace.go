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

package config

import (
	"fmt"
	"os"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

const podNamespaceEnvironment = "POD_NAMESPACE"

// PodNamespace returns the namespace Kubernetes assigned to the controller
// Pod. Installation namespace is deployment metadata, not user configuration,
// and is injected by the Downward API.
func PodNamespace() (string, error) {
	namespace := strings.TrimSpace(os.Getenv(podNamespaceEnvironment))
	if namespace == "" {
		return "", fmt.Errorf("%s must be set from the controller Pod namespace", podNamespaceEnvironment)
	}
	if problems := validation.IsDNS1123Label(namespace); len(problems) > 0 {
		return "", fmt.Errorf("%s %q is invalid: %s",
			podNamespaceEnvironment, namespace, strings.Join(problems, ", "))
	}
	return namespace, nil
}
