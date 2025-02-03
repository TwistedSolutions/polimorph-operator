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

package controller

import (
	"context"
	"fmt"
	"time"

	networking "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
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

	polimorphv1 "github.com/TwistedSolutions/polimorph-operator/api/v1"
	"github.com/TwistedSolutions/polimorph-operator/internal/morph"
)

// PoliMorphPolicyReconciler reconciles a PoliMorphPolicy object
type PoliMorphPolicyReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// Definitions
const (
	typeAvailablePoliMorphPolicy = "Available"
	lastUpdatedByOperator        = "polimorphpolicies.twistedsolutions.se/last-updated"
	intervalAnnotation           = "polimorphpolicies.twistedsolutions.se/interval"
	timeLayout                   = "2006-01-02T15:04:05-07:00"
)

// +kubebuilder:rbac:groups=networking.twistedsolutions.se,resources=polimorphpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.twistedsolutions.se,resources=polimorphpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=networking.twistedsolutions.se,resources=polimorphpolicies/finalizers,verbs=update

//+kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete

func (r *PoliMorphPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	polimorphpolicy := &polimorphv1.PoliMorphPolicy{}
	if err := r.Get(ctx, req.NamespacedName, polimorphpolicy); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("PoliMorphPolicy resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get PoliMorphPolicy")
		return ctrl.Result{}, err
	}

	if len(polimorphpolicy.Status.Conditions) == 0 {
		meta.SetStatusCondition(&polimorphpolicy.Status.Conditions, metav1.Condition{Type: typeAvailablePoliMorphPolicy, Status: metav1.ConditionUnknown, Reason: "PoliMorphPolicyCreated", Message: "PoliMorphPolicy has been created"})
		if err := r.Status().Update(ctx, polimorphpolicy); err != nil {
			log.Error(err, "Failed to update PoliMorphPolicy status")
			return ctrl.Result{}, err
		}
	}

	var ttl uint32
	var requeueInterval time.Duration
	var net *networking.NetworkPolicy
	net, ttl, err := morph.ParseNetworkPolicy(polimorphpolicy)
	if err != nil {
		log.Error(err, "Failed to parse PoliMorphPolicy")
		meta.SetStatusCondition(&polimorphpolicy.Status.Conditions, metav1.Condition{
			Type:    typeAvailablePoliMorphPolicy,
			Status:  metav1.ConditionFalse,
			Reason:  "Error",
			Message: fmt.Sprintf("Error parsing PoliMorphPolicy: %v", err),
		})
		if err := r.Status().Update(ctx, polimorphpolicy); err != nil {
			log.Error(err, "Failed to update PoliMorphPolicy status")
		}
		return ctrl.Result{}, err
	}

	getInterval := func() time.Duration {
		if polimorphpolicy.Spec.Interval != nil {
			return time.Duration(*polimorphpolicy.Spec.Interval) * time.Second
		}
		return time.Duration(ttl) * time.Second
	}

	requeueInterval = getInterval()

	found := &networking.NetworkPolicy{}
	err = r.Get(ctx, types.NamespacedName{
		Name:      polimorphpolicy.Name,
		Namespace: polimorphpolicy.Namespace,
	}, found)

	switch {
	case err == nil:
	case apierrors.IsNotFound(err):
		if err := controllerutil.SetControllerReference(polimorphpolicy, net, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		if net.Annotations == nil {
			net.Annotations = map[string]string{}
		}
		net.Annotations[lastUpdatedByOperator] = time.Now().Format(timeLayout)
		net.Annotations[intervalAnnotation] = fmt.Sprintf("%d", int(requeueInterval.Seconds()))
		if err := r.Create(ctx, net); err != nil {
			log.Error(err, "Failed to create NetworkPolicy")
			return ctrl.Result{}, err
		}
		if retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if err := r.Get(ctx, req.NamespacedName, polimorphpolicy); err != nil {
				return err
			}
			polimorphpolicy.Status.Interval = uint32(requeueInterval.Seconds())
			return r.Status().Update(ctx, polimorphpolicy)
		}); retryErr != nil {
			log.Error(retryErr, "Failed to update PoliMorphPolicy status")
			return ctrl.Result{}, retryErr
		}
		log.Info("Successfully created NetworkPolicy")
		return ctrl.Result{RequeueAfter: requeueInterval}, nil
	default:
		log.Error(err, "Failed to get existing NetworkPolicy")
		return ctrl.Result{}, err
	}

	changed := !equality.Semantic.DeepEqual(net.Spec, found.Spec)
	if changed {
		if net.Annotations == nil {
			net.Annotations = map[string]string{}
		}
		net.Annotations[lastUpdatedByOperator] = time.Now().Format(timeLayout)
		net.Annotations[intervalAnnotation] = fmt.Sprintf("%d", int(requeueInterval.Seconds()))
		if err := controllerutil.SetControllerReference(polimorphpolicy, net, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Update(ctx, net); err != nil {
			log.Error(err, "Failed to update NetworkPolicy")
			return ctrl.Result{}, err
		}
		log.Info("Successfully updated NetworkPolicy")
	} else {
		log.V(1).Info("NetworkPolicy is up to date")
	}

	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := r.Get(ctx, req.NamespacedName, polimorphpolicy); err != nil {
			return err
		}
		polimorphpolicy.Status.Interval = uint32(requeueInterval.Seconds())
		meta.SetStatusCondition(&polimorphpolicy.Status.Conditions, metav1.Condition{
			Type:    typeAvailablePoliMorphPolicy,
			Status:  metav1.ConditionTrue,
			Reason:  "Success",
			Message: "Successfully reconciled PoliMorphPolicy",
		})
		return r.Status().Update(ctx, polimorphpolicy)
	})
	if retryErr != nil {
		log.Error(retryErr, "Failed to update PoliMorphPolicy status")
		return ctrl.Result{}, retryErr
	}

	return ctrl.Result{RequeueAfter: requeueInterval}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PoliMorphPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&polimorphv1.PoliMorphPolicy{}).
		Owns(&networking.NetworkPolicy{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Complete(r)
}
