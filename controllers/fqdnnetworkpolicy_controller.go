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
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"sort"
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
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	fqdnv1alpha1 "github.com/TwistedSolutions/fqdn-operator/api/v1alpha1"

	"github.com/miekg/dns"

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
	TTL                            = "fqdnnetworkpolicies.twistedsolutions.se/ttl"
	DefaultTTLValue                = "80"
	lastUpdatedByOperator          = "fqdnnetworkpolicies.twistedsolutions.se/last-updated"
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

	var ttl uint32 = 0
	var net *networking.NetworkPolicy
	// Check if the NetworkPolicy already exists, if not create a new one
	found := &networking.NetworkPolicy{}
	err = r.Get(ctx, types.NamespacedName{Name: fqdnnetworkpolicy.Name, Namespace: fqdnnetworkpolicy.Namespace}, found)
	if err != nil && apierrors.IsNotFound(err) {
		// Define a new fqdnnetworkpolicy
		net, ttl, err = parseNetworkPolicy(fqdnnetworkpolicy)

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

		net.Annotations[lastUpdatedByOperator] = time.Now().Format(time.RFC3339)

		if err = r.Create(ctx, net); err != nil {
			log.Error(err, "Failed to create new NetworkPolicy",
				"NetworkPolicy.Namespace", net.Namespace, "NetworkPolicy.Name", net.Name)
			return ctrl.Result{}, err
		}

		// Set TTL in Status conditions
		fqdnnetworkpolicy.Status.TTL = ttl
		if err := r.Status().Update(ctx, fqdnnetworkpolicy); err != nil {
			return ctrl.Result{}, err
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
		net, ttl, err = parseNetworkPolicy(fqdnnetworkpolicy)

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

		fmt.Print(changed)

		if changed {
			net.Annotations[lastUpdatedByOperator] = time.Now().Format(time.RFC3339)

			if err = r.Update(ctx, net); err != nil {
				log.Error(err, "Failed to update NetworkPolicy",
					"NetworkPolicy.Namespace", net.Namespace, "NetworkPolicy.Name", net.Name)
				return ctrl.Result{}, err
			}
			log.Info("Successfully updated NetworkPolicy",
				"NetworkPolicy.Namespace", net.Namespace, "NetworkPolicy.Name", net.Name, "Egress", net.Spec.Egress)

			meta.SetStatusCondition(&fqdnnetworkpolicy.Status.Conditions, metav1.Condition{Type: typeAvailableFqdnNetworkPolicy,
				Status: metav1.ConditionTrue, Reason: "Success",
				Message: fmt.Sprintf("NetworkPolicy for FqdnNetworkPolicy (%s) updated successfully", fqdnnetworkpolicy.Name)})

			fqdnnetworkpolicy.Status.TTL = ttl

			if err := r.Status().Update(ctx, fqdnnetworkpolicy); err != nil {
				log.Error(err, "Failed to update FqdnNetworkPolicy status")
				return ctrl.Result{}, err
			}

		}
	}

	// Requeue after TTL.
	return ctrl.Result{RequeueAfter: time.Duration(fqdnnetworkpolicy.Status.TTL+1) * time.Second}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *FqdnNetworkPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&fqdnv1alpha1.FqdnNetworkPolicy{}).
		Owns(&networking.NetworkPolicy{}, builder.WithPredicates(ignoreOwnUpdatesPredicate())).
		Complete(r)
}

func ignoreOwnUpdatesPredicate() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			// Skip reconcile if the NetworkPolicy was updated by the operator
			if lastUpdated, ok := e.ObjectOld.GetAnnotations()[lastUpdatedByOperator]; ok {
				if lastUpdated == e.ObjectNew.GetAnnotations()[lastUpdatedByOperator] {
					return false // Skip reconcile
				}
			}
			return true // Proceed with reconcile for other updates
		},
	}
}

// Parse a NetworkPolicy CR from the FqdnNetworkPolicy with DNS lookups on FQDN
func parseNetworkPolicy(
	fqdnnetworkpolicy *fqdnv1alpha1.FqdnNetworkPolicy) (*networking.NetworkPolicy, uint32, error) {

	var ttl uint32
	var err error

	egress := []networking.NetworkPolicyEgressRule{}
	for _, e := range fqdnnetworkpolicy.Spec.Egress {
		peers := []networking.NetworkPolicyPeer{}
		for _, peer := range e.To {
			var ips []string
			ips, ttl, err = lookupFqdn(peer.FQDN)
			if err != nil {
				log.Log.Error(err, "Failed to lookup FQDN: %s", "FQDN", peer.FQDN)
				return nil, 0, err
			}
			for _, ip := range ips {
				cidr := ip
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
				TTL: strconv.FormatUint(uint64(ttl), 10),
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

	return net, ttl, nil
}

func lookupFqdn(fqdn string) ([]string, uint32, error) {

	dnsServer, set := os.LookupEnv("DNS_SERVER")
	if !set {
		dnsServer = "kube-dns.kube-system.svc.cluster.local:53" // Default Kubernetes DNS FQDN
	}

	c := new(dns.Client)
	m := new(dns.Msg)

	m.SetQuestion(dns.Fqdn(fqdn), dns.TypeA)
	r, _, err := c.Exchange(m, dnsServer)
	if err != nil {
		return nil, 0, err
	}
	if len(r.Answer) == 0 {
		return nil, 0, fmt.Errorf("no dns records found for fqdn: %s", fqdn)
	}

	var ips []string
	var lowestTTL uint32
	for i, ans := range r.Answer {
		if a, ok := ans.(*dns.A); ok {
			ips = append(ips, a.A.String())
			if i == 0 || a.Hdr.Ttl < lowestTTL {
				lowestTTL = a.Hdr.Ttl
			}
		}
	}

	if len(ips) == 0 {
		return nil, 0, fmt.Errorf("no a records found for fqdn: %s", fqdn)
	}

	sort.Slice(ips, func(i, j int) bool {
		// Parse IPs
		ip1 := net.ParseIP(ips[i])
		ip2 := net.ParseIP(ips[j])

		// Compare byte-wise
		return bytes.Compare(ip1, ip2) < 0
	})

	return ips, lowestTTL, nil
}
