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
	"k8s.io/apimachinery/pkg/runtime"
)

type WorkloadAnnotation struct {
	// Name of Kubernetes workload to schedule according to Annotations
	Name string `json:"name,omitempty"`
	// List of annotations to associate with the named workload
	Annotations []string `json:"annotations,omitempty"`
}

// P2CodeSchedulingManifestSpec defines the desired state of P2CodeSchedulingManifest
type P2CodeSchedulingManifestSpec struct {
	// Optional list of annotations applied to all manifests under Spec.Manifests
	// Annotations must be of the form p2code.xx.yy=zz
	// Filter annotations may be used to schedule to a clusters with a given feature
	// Clusters can be filtered based on:
	// - physical location (Europe, Greece, Italy)
	// - Kubernetes distribution installed on the cluster (Kubernetes, OpenShift)
	// - resource availability (GPUs, presence of edge devices)
	GlobalAnnotations []string `json:"globalAnnotations,omitempty"`
	// Optional list of more granular annotations
	// If a particular workload requires a specific annotation it should be specified here
	// WorkloadAnnotations must follow the same format as GlobalAnnotations
	WorkloadAnnotations []WorkloadAnnotation `json:"workloadAnnotations,omitempty"`
	// Required list of related manifests, much like a Helm Chart
	Manifests []runtime.RawExtension `json:"manifests"`
}

type SchedulingDecision struct {
	// Name of workload scheduled
	WorkloadName string `json:"workloadName"`
	// Name of managed cluster where the workload and its ancillary resource have been scheduled
	ClusterSelected string `json:"clusterSelected"`
}

// P2CodeSchedulingManifestStatus defines the observed state of P2CodeSchedulingManifest
type P2CodeSchedulingManifestStatus struct {
	// List of conditions for P2CodeSchedulingManifest
	Conditions []metav1.Condition `json:"conditions"`
	// List with scheduling decision made for each manifest taking into account the annotations specified
	Decisions []SchedulingDecision `json:"decisions"`
	// State of the P2CodeSchedulingManifest indicating if scheduling is in progress, failed or successful
	State string `json:"state"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="State",type="string",JSONPath=".status.state"

// P2CodeSchedulingManifest is the Schema for the p2codeschedulingmanifests API
type P2CodeSchedulingManifest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   P2CodeSchedulingManifestSpec   `json:"spec,omitempty"`
	Status P2CodeSchedulingManifestStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// P2CodeSchedulingManifestList contains a list of P2CodeSchedulingManifests
type P2CodeSchedulingManifestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []P2CodeSchedulingManifest `json:"items"`
}

func init() {
	SchemeBuilder.Register(&P2CodeSchedulingManifest{}, &P2CodeSchedulingManifestList{})
}
