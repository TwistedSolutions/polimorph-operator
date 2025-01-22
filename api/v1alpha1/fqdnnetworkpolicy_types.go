/*
Copyright 2023.

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
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// FqdnNetworkPolicySpec defines the desired state of FqdnNetworkPolicy
type FqdnNetworkPolicySpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Foo is an example field of FqdnNetworkPolicy. Edit fqdnnetworkpolicy_types.go to remove/update
	PodSelector metav1.LabelSelector      `json:"podSelector" protobuf:"bytes,1,opt,name=podSelector"`
	Egress      []NetworkPolicyEgressRule `json:"egress,omitempty" protobuf:"bytes,3,rep,name=egress"`
	Interval    *uint32                   `json:"interval,omitempty" protobuf:"bytes,4,opt,name=interval"`
}

type NetworkPolicyEgressRule struct {
	Ports []networking.NetworkPolicyPort `json:"ports,omitempty" protobuf:"bytes,1,rep,name=ports"`
	To    []NetworkPolicyPeer            `json:"to,omitempty" protobuf:"bytes,2,rep,name=to"`
}

type NetworkPolicyPeer struct {
	FQDN      string   `json:"FQDN,omitempty" protobuf:"bytes,1,name=FQDN"`
	Endpoint  string   `json:"endpoint,omitempty" protobuf:"bytes,2,name=endpoint"`
	JSONPaths []string `json:"jsonPaths,omitempty" protobuf:"bytes,3,rep,name=jsonPaths"`
}

// FqdnNetworkPolicyStatus defines the observed state of FqdnNetworkPolicy
type FqdnNetworkPolicyStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	// +operator-sdk:csv:customresourcedefinitions:type=status
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
	TTL        uint32             `json:"ttl,omitempty" patchStrategy:"merge"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// FqdnNetworkPolicy is the Schema for the fqdnnetworkpolicy API
type FqdnNetworkPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FqdnNetworkPolicySpec   `json:"spec,omitempty"`
	Status FqdnNetworkPolicyStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// FqdnNetworkPolicyList contains a list of FqdnNetworkPolicy
type FqdnNetworkPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FqdnNetworkPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FqdnNetworkPolicy{}, &FqdnNetworkPolicyList{})
}
