/*
Copyright 2020 The Kubernetes Authors.
Copyright 2021 The Flux authors.
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

Adapted from github.com/fluxcd/pkg/runtime/patch/options.go at v0.111.0.
*/

package patch

// Option configures a Helper patch operation.
type Option interface {
	ApplyToHelper(*HelperOptions)
}

// HelperOptions contains status patch configuration.
type HelperOptions struct {
	OwnedConditions []string
}

// WithOwnedConditions option declares condition types owned by the caller.
type WithOwnedConditions struct {
	Conditions []string
}

// ApplyToHelper applies owned-condition configuration.
func (option WithOwnedConditions) ApplyToHelper(options *HelperOptions) {
	options.OwnedConditions = option.Conditions
}
