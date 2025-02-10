/*
Copyright 2025.

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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Image represents a container image with its name and optional tag.
//
// Fields:
//   - Name: The name of the Docker image (e.g., "nginx", "postgres"). This field is required.
//   - Tag: The version tag of the Docker image (e.g., "1.21", "latest"). If omitted, the default tag "latest" is assumed by Docker.
//   - PullPolicy: The image pulling policy. If omitted, the default is "Always" if tag is "latest", "IfNotPresent" otherwise.
type Image struct {
	// Name of the Docker image.
	Name string `json:"name"`

	// Version tag of the image (optional).
	Tag string `json:"tag,omitempty"`

	// PullPolicy (optional)
	PullPolicy corev1.PullPolicy `json:"pullPolicy,omitempty"`
}

// CopyEnv represents an instruction to copy an environment variable
// from a specific container within the same pod.
//
// Fields:
//   - VariableName: The name of the environment variable to copy. This field is required.
//   - ContainerName: The name of the source container where the variable is defined. If left empty, the variable will be copied from the pod's default container.
type CopyEnv struct {
	// Name of the environment variable.
	VariableName string `json:"variable,omitempty"`

	// NewName of the copied variable. If left empty, the original name is used.
	NewName string `json:"newName,omitempty"`

	// Source container name (optional).
	ContainerName string `json:"container,omitempty"`
}

// CopyVolumeMount specifies a volume mount that should be copied from another container within the same pod.
//
// Fields:
// - MountPath: The path where the volume is mounted inside the container. This field is required.
// - ContainerName: The name of the container from which the volume mount should be copied. This field is optional. If not specified, a default behavior (e.g., using the same pod's context) may apply.
type CopyVolumeMount struct {
	// Name of the environment variable.
	MountPath string `json:"mountPath,omitempty"`

	// Source container name (optional).
	ContainerName string `json:"container,omitempty"`
}

// PolicySpec defines the desired state of Policy.
type PolicySpec struct {
	// Image specifies the Docker image to use.
	Image Image `json:"image"`

	// Environment declares a list of environment variables to declare.
	Environment []corev1.EnvVar `json:"environment,omitempty"`

	// CopyEnv represents an instruction to copy an environment variable from an other container in the same pod.
	CopyEnv []CopyEnv `json:"copyEnv,omitempty"`

	// CopyVolumeMount represents an instruction to copy a volume mount from an other container in the same pod.
	CopyVolumeMount []CopyVolumeMount `json:"copyVolumeMounts,omitempty"`

	// AutoDetectVolumeMounts enables automatic detection of the volume mounts to be copied.
	// If set to true, the controller will attempt to identify and replicate the appropriate volume mounts.
	AutoDetectVolumeMounts bool `json:"autoDetectVolumeMounts,omitempty"`
}

// PolicyStatus defines the observed state of Policy.
type PolicyStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// Policy is the Schema for the policies API.
type Policy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PolicySpec   `json:"spec,omitempty"`
	Status PolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PolicyList contains a list of Policy.
type PolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Policy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Policy{}, &PolicyList{})
}
