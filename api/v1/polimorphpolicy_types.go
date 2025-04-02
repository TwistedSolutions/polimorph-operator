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
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PoliMorphPolicySpec defines the desired state of PoliMorphPolicy
type PoliMorphPolicySpec struct {
	PodSelector metav1.LabelSelector        `json:"podSelector" protobuf:"bytes,1,opt,name=podSelector"`
	Egress      []PoliMorphPolicyEgressRule `json:"egress" protobuf:"bytes,3,rep,name=egress"`
	Interval    *uint32                     `json:"interval,omitempty" protobuf:"bytes,4,opt,name=interval"`
	// +kubebuilder:default:=0
	// Cache is the number of minutes to cache the results of the FQDN resolution
	Cache *uint32 `json:"cache" protobuf:"bytes,5,opt,name=cache"`
}

type PoliMorphPolicyEgressRule struct {
	Ports []networking.NetworkPolicyPort `json:"ports,omitempty" protobuf:"bytes,1,rep,name=ports"`
	To    []PoliMorphNetworkPolicyPeer   `json:"to,omitempty" protobuf:"bytes,2,rep,name=to"`
}

type PoliMorphNetworkPolicyPeer struct {
	FQDN      string   `json:"FQDN,omitempty" protobuf:"bytes,1,name=FQDN"`
	Endpoint  string   `json:"endpoint,omitempty" protobuf:"bytes,2,name=endpoint"`
	JSONPaths []string `json:"jsonPaths,omitempty" protobuf:"bytes,3,name=jsonPaths"`
}

// PoliMorphPolicyStatus defines the observed state of PoliMorphPolicy
type PoliMorphPolicyStatus struct {
	// +operator-sdk:csv:customresourcedefinitions:type=status
	Conditions  []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
	Interval    uint32             `json:"interval,omitempty" patchStrategy:"merge"`
	LastUpdated string             `json:"lastUpdated,omitempty" patchStrategy:"merge"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=morph;polimorph;pmorph
// PoliMorphPolicy is the Schema for the polimorphpolicies API
type PoliMorphPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PoliMorphPolicySpec   `json:"spec,omitempty"`
	Status PoliMorphPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PoliMorphPolicyList contains a list of PoliMorphPolicy
type PoliMorphPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PoliMorphPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PoliMorphPolicy{}, &PoliMorphPolicyList{})
}
