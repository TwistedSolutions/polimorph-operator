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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	networkingv1alpha1 "github.com/TwistedSolutions/polimorph-operator/api/v1alpha1"
	"github.com/TwistedSolutions/polimorph-operator/internal/utils"

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
	typeAvailableFqdnNetworkPolicy = "Available"
	typeTTL                        = "TTL"
	DefaultTTLValue                = "80"
	lastUpdatedByOperator          = "fqdnnetworkpolicies.twistedsolutions.se/last-updated"
	timeLayout                     = "2006-01-02T15:04:05-07:00"
)

//+kubebuilder:rbac:groups=networking.twistedsolutions.se,resources=fqdnnetworkpolicies,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=networking.twistedsolutions.se,resources=fqdnnetworkpolicies/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=networking.twistedsolutions.se,resources=fqdnnetworkpolicies/finalizers,verbs=update

//+kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete

func (r *FqdnNetworkPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	fqdnnetworkpolicy := &networkingv1alpha1.FqdnNetworkPolicy{}
	err := r.Get(ctx, req.NamespacedName, fqdnnetworkpolicy)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("FqdnNetworkPolicy resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get FqdnNetworkPolicy")
		return ctrl.Result{}, err
	}

	// If FqdnNetworkPolicy exists, but has no status, set condition to available and status to reconciling
	if len(fqdnnetworkpolicy.Status.Conditions) == 0 {
		meta.SetStatusCondition(&fqdnnetworkpolicy.Status.Conditions, metav1.Condition{Type: typeAvailableFqdnNetworkPolicy, Status: metav1.ConditionUnknown, Reason: "Reconciling", Message: "Starting reconciliation"})
		if err = r.Status().Update(ctx, fqdnnetworkpolicy); err != nil {
			log.Error(err, "Failed to update FqdnNetworkPolicy status")
			return ctrl.Result{}, err
		}
		if err := r.Get(ctx, req.NamespacedName, fqdnnetworkpolicy); err != nil {
			log.Error(err, "Failed to re-fetch FqdnNetworkPolicy")
			return ctrl.Result{}, err
		}
	}

	var ttl uint32
	var requeueInterval time.Duration
	var net *networking.NetworkPolicy
	net, ttl, err = utils.ParseNetworkPolicy(fqdnnetworkpolicy)
	if err != nil {
		log.Error(err, "Failed to parse FqdnNetworkPolicy")
		meta.SetStatusCondition(&fqdnnetworkpolicy.Status.Conditions, metav1.Condition{
			Type:    typeAvailableFqdnNetworkPolicy,
			Status:  metav1.ConditionFalse,
			Reason:  "Error",
			Message: fmt.Sprintf("Error parsing FqdnNetworkPolicy: %v", err),
		})
		if err := r.Status().Update(ctx, fqdnnetworkpolicy); err != nil {
			log.Error(err, "Failed to update FqdnNetworkPolicy status")
		}
		return ctrl.Result{}, err
	}

	if fqdnnetworkpolicy.Spec.Interval != nil {
		requeueInterval = time.Duration(*fqdnnetworkpolicy.Spec.Interval) * time.Second
	} else {
		requeueInterval = time.Duration(ttl) * time.Second
	}

	found := &networking.NetworkPolicy{}
	err = r.Get(ctx, types.NamespacedName{Name: fqdnnetworkpolicy.Name, Namespace: fqdnnetworkpolicy.Namespace}, found)
	if err != nil && apierrors.IsNotFound(err) {
		if err := controllerutil.SetControllerReference(fqdnnetworkpolicy, net, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		net.Annotations[lastUpdatedByOperator] = time.Now().Format(timeLayout)
		if err = r.Create(ctx, net); err != nil {
			log.Error(err, "Failed to create NetworkPolicy")
			return ctrl.Result{}, err
		}
		retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if err := r.Get(ctx, req.NamespacedName, fqdnnetworkpolicy); err != nil {
				return err
			}
			fqdnnetworkpolicy.Status.TTL = ttl
			return r.Status().Update(ctx, fqdnnetworkpolicy)
		})
		if retryErr != nil {
			log.Error(retryErr, "Failed to update FqdnNetworkPolicy status")
			return ctrl.Result{}, retryErr
		}
		log.Info("Successfully created NetworkPolicy")
		return ctrl.Result{RequeueAfter: requeueInterval}, nil
	} else if err != nil {
		log.Error(err, "Failed to get existing NetworkPolicy")
		return ctrl.Result{}, err
	}

	changed := !equality.Semantic.DeepDerivative(net.Spec, found.Spec)
	if changed {
		net.Annotations[lastUpdatedByOperator] = time.Now().Format(timeLayout)
		if err := r.Update(ctx, net); err != nil {
			log.Error(err, "Failed to update NetworkPolicy")
			return ctrl.Result{}, err
		}
		log.Info("Successfully updated NetworkPolicy")
	} else {
		log.Info("NetworkPolicy is up to date")
	}

	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := r.Get(ctx, req.NamespacedName, fqdnnetworkpolicy); err != nil {
			return err
		}
		fqdnnetworkpolicy.Status.TTL = ttl
		meta.SetStatusCondition(&fqdnnetworkpolicy.Status.Conditions, metav1.Condition{
			Type:    typeAvailableFqdnNetworkPolicy,
			Status:  metav1.ConditionTrue,
			Reason:  "Success",
			Message: "Successfully reconciled FqdnNetworkPolicy",
		})
		return r.Status().Update(ctx, fqdnnetworkpolicy)
	})
	if retryErr != nil {
		log.Error(retryErr, "Failed to update FqdnNetworkPolicy status")
		return ctrl.Result{}, retryErr
	}

	return ctrl.Result{RequeueAfter: requeueInterval}, nil
}

func (r *FqdnNetworkPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&networkingv1alpha1.FqdnNetworkPolicy{}).
		Owns(&networking.NetworkPolicy{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Complete(r)
}
