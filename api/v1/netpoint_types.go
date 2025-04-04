/*
Copyright 2024.

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

package v1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetPointSpec defines the desired state of NetPoint
type NetPointSpec struct {

	// Specify the number of replicas for the NetPoint deployment.
	// +kubebuilder:default:=1
	Replicas int32 `json:"replicas,omitempty" protobuf:"varint,1,opt,name=replicas"`
	// Specify custom version of the NetPoint image to be used.
	// +kubebuilder:default:=v1.0.0
	// +kubebuilder:validation:Pattern=^v[0-9]+.[0-9]+.[0-9]+$
	Version string `json:"version,omitempty"`
	// Specify gitops tracking requirement.
	// +kubebuilder:default:=""
	// +kubebuilder:validation:Enum=annotation;label;annotation+label;""
	GitOpsTrackingRequirement string `json:"gitOpsTrackingRequirement,omitempty"`
	// OpenShift specific configuration.
	// +kubebuilder:default:={}
	Openshift Openshift `json:"openshift"`

	// +kubebuilder:default:={requests:{cpu: "50m", memory: "128Mi"}, limits:{cpu: "200m", memory: "128Mi"}}
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

type Openshift struct {
	// Is the NetPoint deployment running on OpenShift?
	// +kubebuilder:default:=false
	// +kubebuilder:validation:Enum=true;false
	IsOpenshift bool `json:"isOpenshift,omitempty"`
	// Specify the route for the NetPoint deployment.
	OpenshiftRoute OpenshiftRoute `json:"route,omitempty"`
}

type OpenshiftRoute struct {
	// Create a route for the NetPoint deployment?
	// +kubebuilder:default:=false
	// +kubebuilder:validation:Enum=true;false
	Create bool `json:"create,omitempty"`
	// Specify the hostname for the route.
	// +kubebuilder:default:=""
	Host string `json:"host,omitempty"`
}

// NetPointStatus defines the observed state of NetPoint
type NetPointStatus struct {
	// +operator-sdk:csv:customresourcedefinitions:type=status
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// NetPoint is the Schema for the netpoints API
type NetPoint struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetPointSpec   `json:"spec,omitempty"`
	Status NetPointStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NetPointList contains a list of NetPoint
type NetPointList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetPoint `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetPoint{}, &NetPointList{})
}
