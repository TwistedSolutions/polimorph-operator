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

package controller

import (
	"context"
	"fmt"
	"time"

	networking "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	networkingv1alpha1 "github.com/TwistedSolutions/fqdn-operator/api/v1alpha1"
	"github.com/TwistedSolutions/fqdn-operator/internal/utils"

	"k8s.io/apimachinery/pkg/api/equality"
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
	DefaultTTLValue                = "80"
	lastUpdatedByOperator          = "fqdnnetworkpolicies.twistedsolutions.se/last-updated"
	timeLayout                     = "2006-01-02T15:04:05-07:00"
	// typeDegradedFqdnNetworkPolicy represents the status used when the custom resource is deleted and the finalizer operations are to occur.
	// typeDegradedFqdnNetworkPolicy = "Degraded"
)

//+kubebuilder:rbac:groups=networking.twistedsolutions.se,resources=fqdnnetworkpolicies,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=networking.twistedsolutions.se,resources=fqdnnetworkpolicies/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=networking.twistedsolutions.se,resources=fqdnnetworkpolicies/finalizers,verbs=update

//+kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.14.1/pkg/reconcile
func (r *FqdnNetworkPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	fqdnnetworkpolicy := &networkingv1alpha1.FqdnNetworkPolicy{}
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

	var ttl uint32 = 0
	var net *networking.NetworkPolicy
	// Check if the NetworkPolicy already exists, if not create a new one
	found := &networking.NetworkPolicy{}
	err = r.Get(ctx, types.NamespacedName{Name: fqdnnetworkpolicy.Name, Namespace: fqdnnetworkpolicy.Namespace}, found)
	if err != nil && apierrors.IsNotFound(err) {
		// Define a new fqdnnetworkpolicy
		net, ttl, err = utils.ParseNetworkPolicy(fqdnnetworkpolicy)

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

		net.Annotations[lastUpdatedByOperator] = time.Now().Format(timeLayout)

		if err = r.Create(ctx, net); err != nil {
			log.Error(err, "Failed to create new NetworkPolicy",
				"NetworkPolicy.Namespace", net.Namespace, "NetworkPolicy.Name", net.Name)
			return ctrl.Result{}, err
		}

		retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			// Fetch the latest version of FqdnNetworkPolicy
			err := r.Get(ctx, req.NamespacedName, fqdnnetworkpolicy)
			if err != nil {
				return err
			}

			fqdnnetworkpolicy.Status.TTL = ttl

			// Try updating the FqdnNetworkPolicy status
			return r.Status().Update(ctx, fqdnnetworkpolicy)
		})

		if retryErr != nil {
			log.Error(retryErr, "Failed to update FqdnNetworkPolicy after retries")
			return ctrl.Result{}, retryErr
		}

		log.Info("Successfully created NetworkPolicy",
			"NetworkPolicy.Namespace", net.Namespace, "NetworkPolicy.Name", net.Name, "Egress", net.Spec.Egress)
		// FqdnNetworkPolicy & NetworkPolicy created successfully
		// We will requeue the reconciliation so that we can ensure the state
		// and move forward for the next operations
		return ctrl.Result{RequeueAfter: time.Duration(fqdnnetworkpolicy.Status.TTL) * time.Second}, nil
	} else if err != nil {
		log.Error(err, "Failed to get FqdnNetworkPolicy")
		// Let's return the error for the reconciliation be re-trigged again
		return ctrl.Result{}, err
	} else { //Update after TTL
		net, ttl, err = utils.ParseNetworkPolicy(fqdnnetworkpolicy)

		if err != nil {
			log.Error(err, "Failed to define new NetworkPolicy resource for FqdnNetworkPolicy")

			// The following implementation will update the status
			meta.SetStatusCondition(&fqdnnetworkpolicy.Status.Conditions, metav1.Condition{Type: typeAvailableFqdnNetworkPolicy,
				Status: metav1.ConditionFalse, Reason: "Reconciling",
				Message: fmt.Sprintf("Failed to update NetworkPolicy for the custom resource (%s): (%s)", fqdnnetworkpolicy.Name, err)})

			if err := r.Status().Update(ctx, fqdnnetworkpolicy); err != nil {
				log.Error(err, "Failed to update FqdnNetworkPolicy status")
				return ctrl.Result{}, err
			}

			return ctrl.Result{}, err
		}

		// Add owner reference on NetworkPolicy
		if err := controllerutil.SetControllerReference(fqdnnetworkpolicy, net, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}

		changed := !equality.Semantic.DeepDerivative(net.Spec, found.Spec)

		if changed {
			net.Annotations[lastUpdatedByOperator] = time.Now().Format(timeLayout)

			if err = r.Update(ctx, net); err != nil {
				log.Error(err, "Failed to update NetworkPolicy",
					"NetworkPolicy.Namespace", net.Namespace, "NetworkPolicy.Name", net.Name)
				return ctrl.Result{}, err
			}
			log.Info("Successfully updated NetworkPolicy",
				"NetworkPolicy.Namespace", net.Namespace, "NetworkPolicy.Name", net.Name, "Egress", net.Spec.Egress)
		} else {
			log.V(1).Info("NetworkPolicy already up to date",
				"NetworkPolicy.Namespace", net.Namespace, "NetworkPolicy.Name", net.Name)
		}

		retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			// Fetch the latest version of FqdnNetworkPolicy
			err := r.Get(ctx, req.NamespacedName, fqdnnetworkpolicy)
			if err != nil {
				return err
			}

			meta.SetStatusCondition(&fqdnnetworkpolicy.Status.Conditions, metav1.Condition{Type: typeAvailableFqdnNetworkPolicy,
				Status: metav1.ConditionTrue, Reason: "Success",
				Message: fmt.Sprintf("NetworkPolicy for FqdnNetworkPolicy (%s) updated successfully", fqdnnetworkpolicy.Name)})

			fqdnnetworkpolicy.Status.TTL = ttl

			// Try updating the FqdnNetworkPolicy status
			return r.Status().Update(ctx, fqdnnetworkpolicy)
		})

		if retryErr != nil {
			log.Error(retryErr, "Failed to update FqdnNetworkPolicy after retries")
			return ctrl.Result{}, retryErr
		}

	}

	// Requeue after TTL.
	return ctrl.Result{RequeueAfter: time.Duration(fqdnnetworkpolicy.Status.TTL+1) * time.Second}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *FqdnNetworkPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&networkingv1alpha1.FqdnNetworkPolicy{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Owns(&networking.NetworkPolicy{}, builder.WithPredicates(ignoreOwnUpdatesPredicate(), predicate.GenerationChangedPredicate{})).
		Complete(r)
}

func ignoreOwnUpdatesPredicate() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			// Skip reconcile if the NetworkPolicy was updated by the operator
			if lastUpdated, ok := e.ObjectOld.GetAnnotations()[lastUpdatedByOperator]; ok {
				// Parse the date string
				t, err := time.Parse(timeLayout, lastUpdated)
				if err != nil {
					return true // Proceed with reconcile for other updates
				}
				// Add 5 seconds to the date string
				tmore := t.Add(5 * time.Second)
				// Format the updated date string
				tmoreString := tmore.Format(timeLayout)
				// Compare the updated date string with the current date string and skip reconcile if the NetworkPolicy was probably updated by the operator
				if tmoreString <= e.ObjectNew.GetAnnotations()[lastUpdatedByOperator] {
					return false // Skip reconcile
				}
			}
			return true // Proceed with reconcile for other updates
		},
	}
}
