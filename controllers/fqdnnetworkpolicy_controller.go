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

package controllers

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	networking "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	fqdnv1alpha1 "github.com/TwistedSolutions/fqdn-operator/api/v1alpha1"
)

// FqdnNetworkPolicyReconciler reconciles a FqdnNetworkPolicy object
type FqdnNetworkPolicyReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// Definitions
const (
	// typeAvailableFqdnNetworkPolicy represents the status of the fqdnnetworkpolicy reconciliation
	typeAvailableFqdnNetworkPolicy = "Available"
	typeTTL                        = "TTL"
	TTL                            = "fqdnnetworkpolicies.twistedsolutions.se/ttl"
	DefaultTTLValue                = "80"
	// typeDegradedFqdnNetworkPolicy represents the status used when the custom resource is deleted and the finalizer operations are to occur.
	// typeDegradedFqdnNetworkPolicy = "Degraded"
)

//+kubebuilder:rbac:groups=twistedsolutions.se,resources=fqdnnetworkpolicy,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=twistedsolutions.se,resources=fqdnnetworkpolicy/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=twistedsolutions.se,resources=fqdnnetworkpolicy/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.14.1/pkg/reconcile
func (r *FqdnNetworkPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	fqdnnetworkpolicy := &fqdnv1alpha1.FqdnNetworkPolicy{}
	err := r.Get(ctx, req.NamespacedName, fqdnnetworkpolicy)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// If the custom resource is not found then, it usually means that it was deleted or not created
			// In this way, we will stop the reconciliation
			log.Info("FqdnNetworkPolicy resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		log.Error(err, "Failed to get FqdnNetworkPolicy")
		return ctrl.Result{}, err
	}

	// If FqdnNetworkPolicy exists, but has no status, set condition to available and status to reconciling
	if fqdnnetworkpolicy.Status.Conditions == nil || len(fqdnnetworkpolicy.Status.Conditions) == 0 {
		meta.SetStatusCondition(&fqdnnetworkpolicy.Status.Conditions, metav1.Condition{Type: typeAvailableFqdnNetworkPolicy, Status: metav1.ConditionUnknown, Reason: "Reconciling", Message: "Starting reconciliation"})
		// Persist status with above StatusCondition
		if err = r.Status().Update(ctx, fqdnnetworkpolicy); err != nil {
			log.Error(err, "Failed to update FqdnNetworkPolicy status")
			return ctrl.Result{}, err
		}

		// Let's re-fetch the FqdnNetworkPolicy Custom Resource after update the status
		// so that we have the latest state of the resource on the cluster and we will avoid
		// raise the issue "the object has been modified, please apply
		// your changes to the latest version and try again" which would re-trigger the reconciliation
		// if we try to update it again in the following operations
		if err := r.Get(ctx, req.NamespacedName, fqdnnetworkpolicy); err != nil {
			log.Error(err, "Failed to re-fetch FqdnNetworkPolicy")
			return ctrl.Result{}, err
		}
	}

	// Check if the NetworkPolicy already exists, if not create a new one
	found := &networking.NetworkPolicy{}
	err = r.Get(ctx, types.NamespacedName{Name: fqdnnetworkpolicy.Name, Namespace: fqdnnetworkpolicy.Namespace}, found)
	if err != nil && apierrors.IsNotFound(err) {
		// Define a new fqdnnetworkpolicy
		net, err := r.parseNetworkPolicy(fqdnnetworkpolicy)
		if err != nil {
			log.Error(err, "Failed to define new NetworkPolicy resource for FqdnNetworkPolicy")

			// The following implementation will update the status
			meta.SetStatusCondition(&fqdnnetworkpolicy.Status.Conditions, metav1.Condition{Type: typeAvailableFqdnNetworkPolicy,
				Status: metav1.ConditionFalse, Reason: "Reconciling",
				Message: fmt.Sprintf("Failed to create NetworkPolicy for the custom resource (%s): (%s)", fqdnnetworkpolicy.Name, err)})

			if err := r.Status().Update(ctx, fqdnnetworkpolicy); err != nil {
				log.Error(err, "Failed to update FqdnNetworkPolicy status")
				return ctrl.Result{}, err
			}

			return ctrl.Result{}, err
		}

		// Add owner reference on NetworkPolicy
		if err := controllerutil.SetControllerReference(fqdnnetworkpolicy, net, r.Scheme); err != nil {
			return ctrl.Result{}, err
		} else {
			log.Info("Added owner reference", "NetworkPolicy.Namespace", net.Namespace, "NetworkPolicy.Name", net.Name)
		}

		if err = r.Create(ctx, net); err != nil {
			log.Error(err, "Failed to create new NetworkPolicy",
				"NetworkPolicy.Namespace", net.Namespace, "NetworkPolicy.Name", net.Name)
			return ctrl.Result{}, err
		}

		log.Info("Successfully created NetworkPolicy",
			"NetworkPolicy.Namespace", net.Namespace, "NetworkPolicy.Name", net.Name, "Egress", net.Spec.Egress)
		// FqdnNetworkPolicy & NetworkPolicy created successfully
		// We will requeue the reconciliation so that we can ensure the state
		// and move forward for the next operations
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	} else if err != nil {
		log.Error(err, "Failed to get FqdnNetworkPolicy")
		// Let's return the error for the reconciliation be re-trigged again
		return ctrl.Result{}, err
	}

	// The following implementation will update the status
	meta.SetStatusCondition(&fqdnnetworkpolicy.Status.Conditions, metav1.Condition{Type: typeAvailableFqdnNetworkPolicy,
		Status: metav1.ConditionTrue, Reason: "Success",
		Message: fmt.Sprintf("NetworkPolicy for FqdnNetworkPolicy (%s) created successfully", fqdnnetworkpolicy.Name)})

	if err := r.Status().Update(ctx, fqdnnetworkpolicy); err != nil {
		log.Error(err, "Failed to update FqdnNetworkPolicy status")
		return ctrl.Result{}, err
	}

	// Look for ttl annotation on NetworkPolicy
	// If annotation does not exist, set it to DefaultTTLValue
	ttlStr, exists := found.Annotations[TTL]
	if !exists {
		ttlStr = DefaultTTLValue
	}

	// The following implementation will update the status with TTL
	meta.SetStatusCondition(&fqdnnetworkpolicy.Status.Conditions, metav1.Condition{Type: typeTTL,
		Status: metav1.ConditionTrue, Reason: "Success",
		Message: fmt.Sprintf("NetworkPolicy for FqdnNetworkPolicy (%s) updated with TTL: "+ttlStr, fqdnnetworkpolicy.Name)})

	if err := r.Status().Update(ctx, fqdnnetworkpolicy); err != nil {
		log.Error(err, "Failed to update FqdnNetworkPolicy status")
		return ctrl.Result{}, err
	}

	// Convert TTL Annotation to duration.
	ttl, err := strconv.Atoi(ttlStr)
	if err != nil {
		log.Error(err, "Failed to convert ttl to duration")
		return ctrl.Result{}, err
	}

	// Requeue after TTL.
	return ctrl.Result{RequeueAfter: time.Duration(ttl) * time.Second}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *FqdnNetworkPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&fqdnv1alpha1.FqdnNetworkPolicy{}).
		Owns(&networking.NetworkPolicy{}).
		Complete(r)
}

// Parse a NetworkPolicy CR from the FqdnNetworkPolicy with DNS lookups on FQDN
func (r *FqdnNetworkPolicyReconciler) parseNetworkPolicy(
	fqdnnetworkpolicy *fqdnv1alpha1.FqdnNetworkPolicy) (*networking.NetworkPolicy, error) {

	egress := []networking.NetworkPolicyEgressRule{}
	for _, e := range fqdnnetworkpolicy.Spec.Egress {
		peers := []networking.NetworkPolicyPeer{}
		for _, p := range e.To {
			ips, err := net.LookupIP(p.FQDN)
			if err != nil {
				log.Log.Error(err, "Failed to lookup FQDN: %s", "FQDN", p.FQDN)
				return nil, err
			}
			for _, ip := range ips {
				cidr := ip.String()
				cidr = cidr + "/32"
				peer := networking.NetworkPolicyPeer{
					IPBlock: &networking.IPBlock{
						CIDR: cidr,
					},
				}
				peers = append(peers, peer)
			}
		}
		rule := networking.NetworkPolicyEgressRule{
			Ports: e.Ports,
			To:    peers,
		}
		egress = append(egress, rule)
	}

	net := &networking.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fqdnnetworkpolicy.Name,
			Namespace: fqdnnetworkpolicy.Namespace,
			Annotations: map[string]string{
				TTL: "30",
			},
		},
		Spec: networking.NetworkPolicySpec{
			PodSelector: fqdnnetworkpolicy.Spec.PodSelector,
			Egress:      egress,
			PolicyTypes: []networking.PolicyType{
				"Egress",
			},
		},
	}

	return net, nil
}
