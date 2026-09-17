package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=reg
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Project",type=string,JSONPath=`.status.harborProject`
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.registryURL`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Registry requests a container registry for the namespace it is created in.
//
// Every Registry, in every namespace, becomes a project inside one central
// Harbor that the operator does not deploy or own. A Registry names no Harbor:
// there is no field to point at one, so a Registry cannot reach another
// tenant's registry by configuration.
//
// Project names are therefore global. A name already held by another Registry
// is refused rather than shared — see status.harborProject.
type Registry struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RegistrySpec   `json:"spec,omitempty"`
	Status RegistryStatus `json:"status,omitempty"`
}

// RegistrySpec is the desired state of one registry.
type RegistrySpec struct {
	// Plan sets this registry's storage quota inside Harbor
	// (starter=5Gi, professional=20Gi, enterprise=100Gi). It can be raised or
	// lowered; Harbor rejects a decrease below current usage until images are
	// removed. It does not size the underlying Harbor deployment.
	// +kubebuilder:validation:Enum=starter;professional;enterprise
	// +kubebuilder:default=starter
	Plan string `json:"plan,omitempty"`
}

// RegistryStatus is the observed state of one registry.
type RegistryStatus struct {
	// Phase is the lifecycle state. Empty until the first reconcile.
	// +kubebuilder:validation:Enum=Provisioning;Ready;Failed;Terminating
	Phase string `json:"phase,omitempty"`

	// ObservedGeneration is the .metadata.generation last reconciled.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions holds standard Kubernetes status conditions.
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// HarborProject is the project created in Harbor for this Registry, and the
	// record that one exists: the finalizer reads it to decide whether there is
	// anything in Harbor to clean up.
	HarborProject string `json:"harborProject,omitempty"`

	// RegistryURL is the Harbor URL to log in and push to.
	RegistryURL string `json:"registryURL,omitempty"`

	// PullSecretName is the Secret in this namespace holding pull-only
	// credentials, in kubernetes.io/dockerconfigjson form. This is the one to
	// copy onto clusters that run these images.
	PullSecretName string `json:"pullSecretName,omitempty"`

	// PushSecretName is the Secret in this namespace holding credentials that
	// can also publish images, in kubernetes.io/dockerconfigjson form. It
	// belongs to a build pipeline, not to a workload.
	PushSecretName string `json:"pushSecretName,omitempty"`

	// Message describes the current phase, including why it is not Ready.
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true

// RegistryList is a list of Registry objects.
type RegistryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Registry `json:"items"`
}

// init registers Registry and its list type with the scheme.
func init() {
	SchemeBuilder.Register(&Registry{}, &RegistryList{})
}
