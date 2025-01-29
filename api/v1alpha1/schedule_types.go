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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// ScheduleSpec defines the desired state of Schedule.
type ScheduleSpec struct {
	// Schedule specifies the backup frequency using a crontab expression.
	// This value should follow the standard crontab format with five space-separated fields:
	// minute (0-59), hour (0-23), day of the month (1-31), month (1-12), and day of the week (0-6, where 0 = Sunday).
	//
	// Examples:
	//   - "0 3 * * *" for a backup every day at 3 AM.
	//   - "*/15 * * * *" for a backup every 15 minutes.
	//   - "0 0 * * 0" for a backup every Sunday at midnight.
	//
	// This field is required and must be a valid crontab string.
	//
	// Docs: https://man7.org/linux/man-pages/man5/crontab.5.html
	Schedule string `json:"schedule,omitempty"`
}

// ScheduleStatus defines the observed state of Schedule.
type ScheduleStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// Schedule is the Schema for the schedules API.
type Schedule struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ScheduleSpec   `json:"spec,omitempty"`
	Status ScheduleStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ScheduleList contains a list of Schedule.
type ScheduleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Schedule `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Schedule{}, &ScheduleList{})
}
